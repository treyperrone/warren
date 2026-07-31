package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sso"
	"github.com/aws/aws-sdk-go-v2/service/ssooidc"
)

// Session holds a resolved set of AWS credentials and metadata.
type Session struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	Region          string
	AccountID       string
	AccountName     string
	RoleName        string
	Label           string // display label for TUI
}

// SSOSessionConfig is a parsed [sso-session] block from ~/.aws/config.
type SSOSessionConfig struct {
	Name     string
	StartURL string
	Region   string
}

// ProfileConfig is a named [profile] block.
type ProfileConfig struct {
	Name string
}

// Account is an SSO-enumerated account.
type Account struct {
	ID   string
	Name string
}

// ParseConfig reads ~/.aws/config and returns all sso-session blocks and named profiles.
func ParseConfig() ([]SSOSessionConfig, []ProfileConfig, error) {
	path := filepath.Join(os.Getenv("HOME"), ".aws", "config")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("reading ~/.aws/config: %w", err)
	}

	var sessions []SSOSessionConfig
	var profiles []ProfileConfig
	var cur map[string]string
	var curHeader string

	flush := func() {
		if cur == nil {
			return
		}
		switch {
		case strings.HasPrefix(curHeader, "sso-session "):
			name := strings.TrimPrefix(curHeader, "sso-session ")
			sessions = append(sessions, SSOSessionConfig{
				Name:     name,
				StartURL: cur["sso_start_url"],
				Region:   cur["sso_region"],
			})
		case strings.HasPrefix(curHeader, "profile "):
			name := strings.TrimPrefix(curHeader, "profile ")
			if name != "default" {
				profiles = append(profiles, ProfileConfig{Name: name})
			}
		}
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			flush()
			curHeader = line[1 : len(line)-1]
			cur = map[string]string{}
			continue
		}
		if cur != nil && strings.Contains(line, "=") {
			parts := strings.SplitN(line, "=", 2)
			cur[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	flush()
	return sessions, profiles, nil
}

// cachedToken reads the SSO token cache for the given start URL.
func cachedToken(startURL string) (string, error) {
	cacheDir := filepath.Join(os.Getenv("HOME"), ".aws", "sso", "cache")
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return "", nil
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(cacheDir, e.Name()))
		if err != nil {
			continue
		}
		var obj struct {
			StartURL    string `json:"startUrl"`
			AccessToken string `json:"accessToken"`
			ExpiresAt   string `json:"expiresAt"`
		}
		if json.Unmarshal(data, &obj) != nil {
			continue
		}
		if obj.StartURL != startURL || obj.AccessToken == "" {
			continue
		}
		// check expiry
		if t, err := time.Parse(time.RFC3339, obj.ExpiresAt); err == nil {
			if time.Now().After(t) {
				continue
			}
		}
		return obj.AccessToken, nil
	}
	return "", nil
}

// Login performs the SSO device-auth flow and returns a live access token.
func Login(ctx context.Context, sess SSOSessionConfig) (string, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(sess.Region))
	if err != nil {
		return "", err
	}
	oidc := ssooidc.NewFromConfig(cfg)

	reg, err := oidc.RegisterClient(ctx, &ssooidc.RegisterClientInput{
		ClientName: aws.String("ssm-tool"),
		ClientType: aws.String("public"),
	})
	if err != nil {
		return "", fmt.Errorf("register client: %w", err)
	}

	auth, err := oidc.StartDeviceAuthorization(ctx, &ssooidc.StartDeviceAuthorizationInput{
		ClientId:     reg.ClientId,
		ClientSecret: reg.ClientSecret,
		StartUrl:     aws.String(sess.StartURL),
	})
	if err != nil {
		return "", fmt.Errorf("start device auth: %w", err)
	}

	fmt.Fprintf(os.Stderr, "\n[sso] Opening browser for %s\n", sess.StartURL)
	fmt.Fprintf(os.Stderr, "[sso] If browser doesn't open, visit: %s\n", aws.ToString(auth.VerificationUriComplete))
	fmt.Fprintf(os.Stderr, "[sso] User code: %s\n\n", aws.ToString(auth.UserCode))

	// attempt to open browser
	openBrowser(aws.ToString(auth.VerificationUriComplete))

	// poll for token
	interval := time.Duration(auth.Interval) * time.Second
	if interval == 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(time.Duration(auth.ExpiresIn) * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(interval)
		tok, err := oidc.CreateToken(ctx, &ssooidc.CreateTokenInput{
			ClientId:     reg.ClientId,
			ClientSecret: reg.ClientSecret,
			GrantType:    aws.String("urn:ietf:params:oauth:grant-type:device_code"),
			DeviceCode:   auth.DeviceCode,
		})
		if err != nil {
			if strings.Contains(err.Error(), "AuthorizationPendingException") ||
				strings.Contains(err.Error(), "SlowDownException") {
				continue
			}
			return "", fmt.Errorf("create token: %w", err)
		}
		// write to cache
		writeTokenCache(sess.StartURL, aws.ToString(tok.AccessToken), int(tok.ExpiresIn))
		return aws.ToString(tok.AccessToken), nil
	}
	return "", fmt.Errorf("login timed out")
}

func writeTokenCache(startURL, token string, expiresIn int) {
	cacheDir := filepath.Join(os.Getenv("HOME"), ".aws", "sso", "cache")
	_ = os.MkdirAll(cacheDir, 0700)
	exp := time.Now().Add(time.Duration(expiresIn) * time.Second).UTC().Format(time.RFC3339)
	obj := map[string]string{
		"startUrl":    startURL,
		"accessToken": token,
		"expiresAt":   exp,
	}
	data, _ := json.Marshal(obj)
	// use a hash of the start URL as filename, matching AWS CLI convention
	name := fmt.Sprintf("ssm-tool-%x.json", hashString(startURL))
	_ = os.WriteFile(filepath.Join(cacheDir, name), data, 0600)
}

func hashString(s string) uint32 {
	var h uint32 = 2166136261
	for _, c := range []byte(s) {
		h ^= uint32(c)
		h *= 16777619
	}
	return h
}

// LiveToken returns a cached token if valid, otherwise runs the login flow.
func LiveToken(ctx context.Context, sess SSOSessionConfig) (string, error) {
	token, err := cachedToken(sess.StartURL)
	if err != nil {
		return "", err
	}
	if token != "" {
		// quick validation
		cfg, _ := config.LoadDefaultConfig(ctx, config.WithRegion(sess.Region))
		client := sso.NewFromConfig(cfg)
		_, err := client.ListAccounts(ctx, &sso.ListAccountsInput{
			AccessToken: aws.String(token),
			MaxResults:  aws.Int32(1),
		})
		if err == nil {
			return token, nil
		}
	}
	return Login(ctx, sess)
}

// ListAccounts returns all SSO accounts for the given token.
func ListAccounts(ctx context.Context, sess SSOSessionConfig, token string) ([]Account, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(sess.Region))
	if err != nil {
		return nil, err
	}
	client := sso.NewFromConfig(cfg)

	var accounts []Account
	var nextToken *string
	for {
		out, err := client.ListAccounts(ctx, &sso.ListAccountsInput{
			AccessToken: aws.String(token),
			NextToken:   nextToken,
		})
		if err != nil {
			return nil, fmt.Errorf("list accounts: %w", err)
		}
		for _, a := range out.AccountList {
			accounts = append(accounts, Account{
				ID:   aws.ToString(a.AccountId),
				Name: aws.ToString(a.AccountName),
			})
		}
		if out.NextToken == nil {
			break
		}
		nextToken = out.NextToken
	}
	sort.Slice(accounts, func(i, j int) bool {
		return accounts[i].Name < accounts[j].Name
	})
	return accounts, nil
}

// ListRoles returns all roles for the given account.
func ListRoles(ctx context.Context, sess SSOSessionConfig, token, accountID string) ([]string, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(sess.Region))
	if err != nil {
		return nil, err
	}
	client := sso.NewFromConfig(cfg)

	var roles []string
	var nextToken *string
	for {
		out, err := client.ListAccountRoles(ctx, &sso.ListAccountRolesInput{
			AccessToken: aws.String(token),
			AccountId:   aws.String(accountID),
			NextToken:   nextToken,
		})
		if err != nil {
			return nil, fmt.Errorf("list roles: %w", err)
		}
		for _, r := range out.RoleList {
			roles = append(roles, aws.ToString(r.RoleName))
		}
		if out.NextToken == nil {
			break
		}
		nextToken = out.NextToken
	}
	sort.Strings(roles)
	return roles, nil
}

// GetRoleCredentials fetches short-lived credentials for an account+role.
func GetRoleCredentials(ctx context.Context, sess SSOSessionConfig, token, accountID, role string) (*Session, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(sess.Region))
	if err != nil {
		return nil, err
	}
	client := sso.NewFromConfig(cfg)

	out, err := client.GetRoleCredentials(ctx, &sso.GetRoleCredentialsInput{
		AccessToken: aws.String(token),
		AccountId:   aws.String(accountID),
		RoleName:    aws.String(role),
	})
	if err != nil {
		return nil, fmt.Errorf("get role credentials: %w", err)
	}
	return &Session{
		AccessKeyID:     aws.ToString(out.RoleCredentials.AccessKeyId),
		SecretAccessKey: aws.ToString(out.RoleCredentials.SecretAccessKey),
		SessionToken:    aws.ToString(out.RoleCredentials.SessionToken),
		Region:          sess.Region,
	}, nil
}
