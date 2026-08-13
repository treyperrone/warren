package aws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sso"
	"github.com/aws/aws-sdk-go-v2/service/ssooidc"

	"github.com/treyperrone/warren/internal/homedir"
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
	// ProfileName is set only for sessions resolved from a named [profile] block, so a
	// consumer can name the profile instead of passing keys. Empty for SSO sessions.
	ProfileName string
	// Expires is when the credentials stop working, zero if not known — a profile backed by
	// long-lived IAM keys has no expiry to report.
	Expires time.Time
}

// SSOSessionConfig is a parsed [sso-session] block from ~/.aws/config.
type SSOSessionConfig struct {
	Name     string
	StartURL string
	Region   string
	// Scopes is sso_registration_scopes. Registering the OIDC client WITH scopes is
	// what makes AWS issue a refresh token, so this is required for silent renewal —
	// a scopeless registration only ever gets an access token.
	Scopes []string
}

// defaultScopes is what the AWS CLI uses when sso_registration_scopes is absent.
var defaultScopes = []string{"sso:account:access"}

func (s SSOSessionConfig) scopes() []string {
	if len(s.Scopes) == 0 {
		return defaultScopes
	}
	return s.Scopes
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

// ConfigPath is the shared AWS config file. It belongs to the `aws` CLI, Terraform, and
// every SDK on the machine — this package reads it freely but only ever appends to it.
func ConfigPath() string {
	// AWS_CONFIG_FILE is the documented override and the aws CLI honours it, so ignoring it meant
	// warren read a different file from the CLI on any machine that set it — while sharing the
	// same SSO token cache, which is a confusing way to fail.
	//
	// Except warren's own sentinel: `warren shell`/`exec` set this exact value on a child so that
	// child's AWS calls cannot see an ambient [default] profile. Running warren again from inside
	// that child — its ordinary, expected shell — inherits the same environment, and honouring
	// the sentinel here would make warren unable to find its own sso-sessions, showing first-run
	// setup on a machine that is already configured. A value the user set deliberately for their
	// own reasons is still honoured; only this one specific, warren-generated value is skipped.
	if p := os.Getenv("AWS_CONFIG_FILE"); p != "" {
		if sentinel, _ := NeutralizedProfilePaths(); p != sentinel {
			return p
		}
	}
	return filepath.Join(homedir.Dir(), ".aws", "config")
}

// ParseConfig reads ~/.aws/config and returns all sso-session blocks and named profiles.
//
// A missing file is not an error: it is the ordinary state of a machine that has never run
// `aws configure`, and the caller offers to create one. An unreadable file still is — that
// means a permissions or I/O problem the user needs told about, not an empty config.
func ParseConfig() ([]SSOSessionConfig, []ProfileConfig, error) {
	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
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
			var scopes []string
			for _, s := range strings.Split(cur["sso_registration_scopes"], ",") {
				if s = strings.TrimSpace(s); s != "" {
					scopes = append(scopes, s)
				}
			}
			sessions = append(sessions, SSOSessionConfig{
				Name:     name,
				StartURL: cur["sso_start_url"],
				Region:   cur["sso_region"],
				Scopes:   scopes,
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

// validSessionName reports whether name is safe to write inside an [sso-session <name>] header.
func validSessionName(name string) bool {
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// ValidateSSOSession checks a proposed sso-session block before it is written. The cost of
// a typo here is a confusing OIDC failure several screens later, so it is worth catching
// the obvious cases at the point of entry.
func ValidateSSOSession(s SSOSessionConfig) error {
	switch {
	case s.Name == "":
		return errors.New("name is required")
	// The name becomes the text inside [sso-session <name>], so anything that would break the
	// header has to be rejected rather than escaped. This is an allowlist because the previous
	// blocklist — " \t[]" — read as "whitespace or a bracket" but let \n, \r, \v and \f
	// through, and a newline splits the header across two lines. ~/.aws/config is shared with
	// the aws CLI, Terraform and every SDK on the machine, so corrupting it breaks all of them,
	// which is the same reason this file is only ever appended to.
	case !validSessionName(s.Name):
		return errors.New("name can contain only letters, digits, dots, dashes and underscores")
	case s.StartURL == "":
		return errors.New("start URL is required")
	case s.Region == "":
		return errors.New("region is required")
	}

	u, err := url.Parse(s.StartURL)
	if err != nil {
		return fmt.Errorf("start URL is not a URL: %w", err)
	}
	if u.Scheme != "https" || u.Host == "" {
		return errors.New("start URL must be a full https:// URL, e.g. https://my-org.awsapps.com/start")
	}
	return nil
}

// AddSSOSession appends an [sso-session] block to ~/.aws/config.
//
// It appends rather than rewriting, deliberately. ParseConfig is lossy — it discards
// comments, ordering, and every key it does not model — so serialising its output back to
// disk would silently destroy config belonging to the AWS CLI, Terraform, and every SDK
// that shares this file. Appending touches nothing that is already there, and a backup is
// taken first so even a botched append is recoverable.
func AddSSOSession(s SSOSessionConfig) error {
	if err := ValidateSSOSession(s); err != nil {
		return err
	}

	existing, _, err := ParseConfig()
	if err != nil {
		return err
	}
	for _, e := range existing {
		if e.Name == s.Name {
			return fmt.Errorf("an sso-session named %q already exists in ~/.aws/config", s.Name)
		}
	}

	path := ConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating ~/.aws: %w", err)
	}

	// Back up whatever is already there before touching it. A missing file needs no backup.
	//
	// #nosec G703,G304 -- gosec sees a non-literal path and reports traversal. path is
	// ConfigPath(): the user's own home directory, or AWS_CONFIG_FILE if they set it. No caller
	// supplies it and no request data reaches it. A user able to set their own environment can
	// already read and write their own files.
	if prev, err := os.ReadFile(path); err == nil {
		if err := os.WriteFile(path+".warren.bak", prev, 0o600); err != nil {
			return fmt.Errorf("backing up ~/.aws/config: %w", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("reading ~/.aws/config: %w", err)
	}

	scopes := strings.Join(s.scopes(), ",")
	block := fmt.Sprintf("\n# added by warren\n[sso-session %s]\nsso_start_url = %s\nsso_region = %s\nsso_registration_scopes = %s\n",
		s.Name, s.StartURL, s.Region, scopes)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opening ~/.aws/config: %w", err)
	}
	// One close, always reached, and its error always checked — rather than `defer f.Close()` or a
	// bare Close on the error path. For a file opened for writing, Close is where buffered data
	// actually reaches the disk, so discarding its error can report a successful append that never
	// landed, on a full disk or a network filesystem. Reporting success for a block that is not in
	// the file is the one outcome this function is built to avoid.
	//
	// The write error takes precedence when both fail: it says what went wrong, where a close
	// error on an already-failed write is a consequence rather than the cause.
	_, writeErr := f.WriteString(block)
	closeErr := f.Close()
	if writeErr != nil {
		return fmt.Errorf("writing ~/.aws/config: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("writing ~/.aws/config: %w", closeErr)
	}
	return nil
}

// tokenRecord mirrors the AWS CLI v2 SSO token cache schema field-for-field. Reading it
// means `aws sso login` already satisfies us; writing the same shape keeps the refresh
// material (client registration + refresh token) alongside the access token, which is the
// whole reason silent renewal is possible.
type tokenRecord struct {
	StartURL              string `json:"startUrl"`
	Region                string `json:"region,omitempty"`
	AccessToken           string `json:"accessToken"`
	ExpiresAt             string `json:"expiresAt"`
	RefreshToken          string `json:"refreshToken,omitempty"`
	ClientID              string `json:"clientId,omitempty"`
	ClientSecret          string `json:"clientSecret,omitempty"`
	RegistrationExpiresAt string `json:"registrationExpiresAt,omitempty"`
}

// expirySkew treats a token as dead slightly early, so we never hand out a token that
// expires mid-request.
const expirySkew = 60 * time.Second

func ssoCacheDir() string {
	return filepath.Join(homedir.Dir(), ".aws", "sso", "cache")
}

// live reports whether an RFC3339 timestamp is still in the future. An unparseable or
// absent timestamp counts as dead: we would rather re-auth than use a token we can't date.
func live(ts string) bool {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return false
	}
	return time.Now().Add(expirySkew).Before(t)
}

// canRefresh reports whether a record carries everything CreateToken needs for the
// refresh_token grant. The client registration expires independently of the token, and a
// refresh against a dead registration just fails.
func (r *tokenRecord) canRefresh() bool {
	return r != nil && r.RefreshToken != "" && r.ClientID != "" && r.ClientSecret != "" &&
		live(r.RegistrationExpiresAt)
}

// cachedRecord returns the best cached record for a start URL: a live access token if one
// exists, otherwise a refreshable record. Unlike a plain expiry filter, an expired record
// is still worth returning — its refresh token is what saves us a browser round trip.
func cachedRecord(startURL string) *tokenRecord {
	entries, err := os.ReadDir(ssoCacheDir())
	if err != nil {
		return nil
	}
	var refreshable *tokenRecord
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(ssoCacheDir(), e.Name()))
		if err != nil {
			continue
		}
		var rec tokenRecord
		if json.Unmarshal(data, &rec) != nil || rec.StartURL != startURL {
			continue
		}
		if rec.AccessToken != "" && live(rec.ExpiresAt) {
			return &rec // best case, stop looking
		}
		if refreshable == nil && rec.canRefresh() {
			r := rec
			refreshable = &r
		}
	}
	return refreshable
}

// Login performs the SSO device-auth flow and returns a live access token.
func Login(ctx context.Context, sess SSOSessionConfig) (string, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(sess.Region))
	if err != nil {
		return "", err
	}
	oidc := ssooidc.NewFromConfig(cfg)

	// Scopes are load-bearing: AWS only returns a refresh token from CreateToken if the
	// client was registered with them. Without this the tool re-runs the browser flow on
	// every expiry, which is what it used to do.
	//
	// Scopeless registration is the fallback, not the goal. If an Identity Center instance
	// rejects the scoped registration, losing silent renewal beats being unable to log in
	// at all — so warn and retry bare rather than failing outright.
	reg, err := oidc.RegisterClient(ctx, &ssooidc.RegisterClientInput{
		ClientName: aws.String("warren"),
		ClientType: aws.String("public"),
		Scopes:     sess.scopes(),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "[sso] scoped registration failed (%v); retrying without scopes — sessions will not auto-renew\n", err)
		reg, err = oidc.RegisterClient(ctx, &ssooidc.RegisterClientInput{
			ClientName: aws.String("warren"),
			ClientType: aws.String("public"),
		})
	}
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
		// Persist the refresh material, not just the access token — that is what lets the
		// next run renew silently instead of reopening the browser.
		writeRecord(&tokenRecord{
			StartURL:              sess.StartURL,
			Region:                sess.Region,
			AccessToken:           aws.ToString(tok.AccessToken),
			ExpiresAt:             expiryStamp(int(tok.ExpiresIn)),
			RefreshToken:          aws.ToString(tok.RefreshToken),
			ClientID:              aws.ToString(reg.ClientId),
			ClientSecret:          aws.ToString(reg.ClientSecret),
			RegistrationExpiresAt: time.Unix(reg.ClientSecretExpiresAt, 0).UTC().Format(time.RFC3339),
		})
		return aws.ToString(tok.AccessToken), nil
	}
	return "", fmt.Errorf("login timed out")
}

func expiryStamp(expiresIn int) string {
	return time.Now().Add(time.Duration(expiresIn) * time.Second).UTC().Format(time.RFC3339)
}

// refresh exchanges a refresh token for a new access token — no browser, no user action.
func refresh(ctx context.Context, sess SSOSessionConfig, rec *tokenRecord) (string, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(sess.Region))
	if err != nil {
		return "", err
	}
	out, err := ssooidc.NewFromConfig(cfg).CreateToken(ctx, &ssooidc.CreateTokenInput{
		ClientId:     aws.String(rec.ClientID),
		ClientSecret: aws.String(rec.ClientSecret),
		GrantType:    aws.String("refresh_token"),
		RefreshToken: aws.String(rec.RefreshToken),
	})
	if err != nil {
		return "", fmt.Errorf("refresh token: %w", err)
	}

	next := *rec
	next.AccessToken = aws.ToString(out.AccessToken)
	next.ExpiresAt = expiryStamp(int(out.ExpiresIn))
	// AWS may rotate the refresh token; if it doesn't, the old one stays valid.
	if rt := aws.ToString(out.RefreshToken); rt != "" {
		next.RefreshToken = rt
	}
	writeRecord(&next)
	return next.AccessToken, nil
}

// writeRecord stores a token record under our own filename.
//
// We deliberately never overwrite a cache file the AWS CLI wrote. The CLI derives its
// filename itself and owns that file's full schema; clobbering it risks dropping a field we
// don't model. The cost is that after we refresh a CLI-issued token, the CLI's copy still
// holds the pre-rotation refresh token — if AWS rotated it, the CLI falls back to a normal
// `aws sso login`. Losing a silent renewal on the CLI side beats corrupting its cache.
func writeRecord(rec *tokenRecord) {
	dir := ssoCacheDir()
	_ = os.MkdirAll(dir, 0700)
	// #nosec G117 -- gosec flags the marshalled AccessToken as a secret reaching disk, which it
	// is. This is ~/.aws/sso/cache, whose format and location AWS defines; the aws CLI writes
	// the same token to the same place. Writing it is the point — it is what makes silent
	// renewal possible instead of a browser prompt every hour. Written 0600 into a 0700 dir.
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	// hash of the start URL as filename, mirroring the AWS CLI convention
	name := fmt.Sprintf("warren-%x.json", hashString(rec.StartURL))
	_ = os.WriteFile(filepath.Join(dir, name), data, 0600)
}

func hashString(s string) uint32 {
	var h uint32 = 2166136261
	for _, c := range []byte(s) {
		h ^= uint32(c)
		h *= 16777619
	}
	return h
}

// ErrLoginRequired means the session cannot be renewed without the user completing the
// device-auth flow. It exists so a caller that must not block — anything running in the
// background — can tell "needs a browser" apart from a real failure and say so instead.
var ErrLoginRequired = errors.New("SSO login required")

// SilentToken returns a usable access token without ever starting the device-auth flow,
// returning ErrLoginRequired when that is the only remaining option.
//
//  1. a cached token that is unexpired AND still accepted by the SSO API
//  2. a silent refresh_token exchange — no browser
//
// Step 1 revalidates rather than trusting the clock alone: a token can be revoked
// server-side (logout elsewhere, session policy change) while its expiry still looks fine.
func SilentToken(ctx context.Context, sess SSOSessionConfig) (string, error) {
	rec := cachedRecord(sess.StartURL)

	if rec != nil && rec.AccessToken != "" && live(rec.ExpiresAt) {
		if err := validate(ctx, sess, rec.AccessToken); err == nil {
			return rec.AccessToken, nil
		}
	}

	if rec.canRefresh() {
		if token, err := refresh(ctx, sess, rec); err == nil {
			return token, nil
		}
		// Refresh failed — revoked, rotated out from under us, or registration dead.
		// Nothing left to salvage silently.
	}

	return "", ErrLoginRequired
}

// LiveToken returns a usable access token, escalating as far as it must — including the full
// device-auth login, which prints a user code and opens a browser.
//
// Only call this where blocking on the user is acceptable. A background refresh must use
// SilentToken: device auth polls for as long as the code is valid, and from a background task
// inside the TUI that means the prompt is painted behind the alt screen while the tool appears
// to hang.
func LiveToken(ctx context.Context, sess SSOSessionConfig) (string, error) {
	if token, err := SilentToken(ctx, sess); err == nil {
		return token, nil
	}
	return Login(ctx, sess)
}

// validate makes the cheapest authenticated call available to prove a token still works.
func validate(ctx context.Context, sess SSOSessionConfig, token string) error {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(sess.Region))
	if err != nil {
		return err
	}
	_, err = sso.NewFromConfig(cfg).ListAccounts(ctx, &sso.ListAccountsInput{
		AccessToken: aws.String(token),
		MaxResults:  aws.Int32(1),
	})
	return err
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
	s := &Session{
		AccessKeyID:     aws.ToString(out.RoleCredentials.AccessKeyId),
		SecretAccessKey: aws.ToString(out.RoleCredentials.SecretAccessKey),
		SessionToken:    aws.ToString(out.RoleCredentials.SessionToken),
		Region:          sess.Region,
	}
	// Expiration is epoch milliseconds, and 0 means the API did not say. Left zero in that
	// case rather than becoming 1970, which would render as "expired".
	if out.RoleCredentials.Expiration != 0 {
		s.Expires = time.UnixMilli(out.RoleCredentials.Expiration)
	}
	return s, nil
}

// defaultProfileRegion is used when a named profile sets no region of its own. It matches
// what the picker assumed before profiles resolved their own region.
const defaultProfileRegion = "us-east-1"

// ProfileSession resolves credentials for a named [profile] block by running the SDK's own
// chain for that profile, so static keys, SSO-backed, and assume-role profiles all work.
//
// The profile path used to fabricate a Session with empty credential fields and rely on
// setting AWS_PROFILE in this process. That could not work: every consumer feeds those
// fields to credentials.NewStaticCredentialsProvider, which rejects empty values outright
// ("static credentials are empty"), so listing instances or opening a session under a named
// profile failed at the first API call. Resolving real credentials here fixes every caller
// at once and keeps profiles and SSO sessions interchangeable from that point on.
func ProfileSession(ctx context.Context, name string) (*Session, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithSharedConfigProfile(name))
	if err != nil {
		return nil, fmt.Errorf("loading profile %s: %w", name, err)
	}

	creds, err := cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolving credentials for profile %s: %w", name, err)
	}

	region := cfg.Region
	if region == "" {
		region = defaultProfileRegion
	}

	s := &Session{
		AccessKeyID:     creds.AccessKeyID,
		SecretAccessKey: creds.SecretAccessKey,
		SessionToken:    creds.SessionToken,
		Region:          region,
		ProfileName:     name,
		Label:           "profile:" + name,
	}
	// Long-lived IAM keys never expire; only report a deadline the provider actually set.
	if creds.CanExpire {
		s.Expires = creds.Expires
	}
	return s, nil
}
