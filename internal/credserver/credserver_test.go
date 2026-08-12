package credserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	awsint "github.com/treyperrone/warren/internal/aws"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	s, err := Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func session(expires time.Time) *awsint.Session {
	return &awsint.Session{
		AccessKeyID:     "AKIAENDPOINT",
		SecretAccessKey: "secret",
		SessionToken:    "token",
		Region:          "us-east-1",
		AccountID:       "195170887130",
		AccountName:     "crlab-globogym",
		RoleName:        "AdminRole",
		Label:           "crlab-globogym (195170887130)/AdminRole",
		Expires:         expires,
	}
}

// get issues a request with the token the server expects unless one is given.
func get(t *testing.T, s *Server, token string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, s.URI(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func (s *Server) testToken() string {
	for _, kv := range s.Env() {
		if k, v, ok := strings.Cut(kv, "="); ok && k == "AWS_CONTAINER_AUTHORIZATION_TOKEN" {
			return v
		}
	}
	return ""
}

func TestServesCredentialsInTheProviderShape(t *testing.T) {
	s := testServer(t)
	expiry := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	s.Set(session(expiry))

	code, body := get(t, s, s.testToken())
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", code, body)
	}

	// Field names are the contract with every AWS SDK; a rename breaks all of them silently.
	var got struct {
		AccessKeyID     string `json:"AccessKeyId"`
		SecretAccessKey string `json:"SecretAccessKey"`
		Token           string `json:"Token"`
		Expiration      string `json:"Expiration"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", body, err)
	}
	if got.AccessKeyID != "AKIAENDPOINT" || got.SecretAccessKey != "secret" || got.Token != "token" {
		t.Errorf("credentials = %+v", got)
	}
	if got.Expiration != expiry.Format(time.RFC3339) {
		t.Errorf("Expiration = %q, want %q", got.Expiration, expiry.Format(time.RFC3339))
	}
}

// The endpoint hands out working credentials, so on a shared machine anything local could read
// them without this.
func TestRefusesWithoutTheToken(t *testing.T) {
	s := testServer(t)
	s.Set(session(time.Now().Add(time.Hour)))

	for _, tt := range []struct{ name, token string }{
		{"absent", ""},
		{"wrong", "not-the-token"},
		{"prefix of the real one", s.testToken()[:8]},
	} {
		t.Run(tt.name, func(t *testing.T) {
			code, _ := get(t, s, tt.token)
			if code != http.StatusForbidden {
				t.Errorf("status = %d, want 403", code)
			}
		})
	}
}

// Loopback only: a routable address would offer credentials to the network.
func TestListensOnLoopbackOnly(t *testing.T) {
	s := testServer(t)
	if !strings.HasPrefix(s.URI(), "http://127.0.0.1:") {
		t.Errorf("URI = %q, want a 127.0.0.1 address", s.URI())
	}
}

// 503 rather than empty credentials, so the SDK reports a retryable failure instead of treating
// blank strings as an identity.
func TestReportsUnavailableBeforeAnySessionIsSet(t *testing.T) {
	s := testServer(t)
	code, _ := get(t, s, s.testToken())
	if code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", code)
	}
}

func TestRejectsNonGET(t *testing.T) {
	s := testServer(t)
	s.Set(session(time.Now().Add(time.Hour)))

	req, _ := http.NewRequest(http.MethodPost, s.URI(), nil)
	req.Header.Set("Authorization", s.testToken())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

// The whole point: a later request sees the newer credentials, which a copy in the child's
// environment could never do.
func TestServesWhateverWasSetMostRecently(t *testing.T) {
	s := testServer(t)
	s.Set(session(time.Now().Add(time.Hour)))

	next := session(time.Now().Add(time.Hour))
	next.AccessKeyID = "AKIARENEWED"
	s.Set(next)

	_, body := get(t, s, s.testToken())
	if !strings.Contains(body, "AKIARENEWED") {
		t.Errorf("body %q does not carry the renewed key", body)
	}
}

// Credentials with no expiry — a profile backed by long-lived keys — must omit the field rather
// than send a zero time, which reads as "expired in 1970".
func TestOmitsExpirationWhenUnknown(t *testing.T) {
	s := testServer(t)
	s.Set(session(time.Time{}))

	_, body := get(t, s, s.testToken())
	if strings.Contains(body, "Expiration") {
		t.Errorf("body %q includes an Expiration for credentials that do not expire", body)
	}
}

// Env must not carry a static copy alongside the endpoint: the SDK's environment provider is
// consulted first, so a copy would win and pin the child to frozen credentials — the exact problem
// the endpoint exists to solve.
func TestEnvCarriesNoStaticCredentials(t *testing.T) {
	s := testServer(t)
	joined := strings.Join(s.Env(), "\n")

	if !strings.Contains(joined, "AWS_CONTAINER_CREDENTIALS_FULL_URI=") ||
		!strings.Contains(joined, "AWS_CONTAINER_AUTHORIZATION_TOKEN=") {
		t.Fatalf("Env() = %v, want the endpoint pair", s.Env())
	}
	for _, banned := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_PROFILE"} {
		if strings.Contains(joined, banned) {
			t.Errorf("Env() includes %s, which would take precedence over the endpoint", banned)
		}
	}
}

func TestKeepFreshRenewsOnlyWhenExpiryIsNear(t *testing.T) {
	tests := []struct {
		name      string
		expires   time.Time
		wantRenew bool
	}{
		{"far from expiry", time.Now().Add(time.Hour), false},
		{"inside the lead window", time.Now().Add(time.Second), true},
		{"already expired", time.Now().Add(-time.Minute), true},
		// Long-lived keys have nothing to renew; renewing on a timer would call AWS forever.
		{"no expiry at all", time.Time{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testServer(t)
			s.Set(session(tt.expires))

			renewed := make(chan struct{}, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			go s.KeepFresh(ctx, 10*time.Millisecond, time.Minute,
				func(context.Context) (*awsint.Session, error) {
					fresh := session(time.Now().Add(time.Hour))
					fresh.AccessKeyID = "AKIAFROMKEEPFRESH"
					select {
					case renewed <- struct{}{}:
					default:
					}
					return fresh, nil
				})

			select {
			case <-renewed:
				if !tt.wantRenew {
					t.Error("renewed credentials that were not near expiry")
				}
			case <-time.After(200 * time.Millisecond):
				if tt.wantRenew {
					t.Error("did not renew credentials that were near expiry")
				}
			}
		})
	}
}

// A failed renewal must not discard credentials that may still have minutes left on them.
func TestKeepFreshKeepsOldCredentialsOnFailure(t *testing.T) {
	s := testServer(t)
	s.Set(session(time.Now().Add(time.Second)))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.KeepFresh(ctx, 10*time.Millisecond, time.Minute,
		func(context.Context) (*awsint.Session, error) {
			return nil, context.DeadlineExceeded
		})

	time.Sleep(100 * time.Millisecond)
	if got := s.Session(); got == nil || got.AccessKeyID != "AKIAENDPOINT" {
		t.Errorf("session = %+v, want the original credentials retained", got)
	}
}

// The contract that matters: the real aws CLI must accept this endpoint. Everything else here
// tests our own assumptions about the shape; this tests the consumer's.
func TestRealAWSCLIUsesTheEndpoint(t *testing.T) {
	if _, err := exec.LookPath("aws"); err != nil {
		t.Skip("aws CLI not installed")
	}

	s := testServer(t)
	s.Set(session(time.Now().Add(time.Hour)))

	// A scratch home so there is no real ~/.aws to fall back on: the point is that the credentials
	// come from the endpoint and nowhere else.
	//
	// Both HOME and USERPROFILE, because the environment is built from scratch rather than
	// inherited. The AWS CLI locates the home directory from USERPROFILE on Windows, and with
	// neither set it refuses to start at all — "Could not determine home directory" — which looked
	// like the endpoint failing when it was the test handing over an unusable environment.
	home := t.TempDir()
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"AWS_EC2_METADATA_DISABLED=1",
		"AWS_REGION=us-east-1",
		"HOME=" + home,
		"USERPROFILE=" + home,
	}
	if runtime.GOOS == "windows" {
		// Python, which the CLI is built on, needs these to import its own standard library.
		for _, k := range []string{"SystemRoot", "SystemDrive", "TEMP", "TMP"} {
			if v := os.Getenv(k); v != "" {
				env = append(env, k+"="+v)
			}
		}
	}

	cmd := exec.Command("aws", "configure", "list")
	cmd.Env = append(env, s.Env()...)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("aws configure list: %v\n%s", err, out)
	}

	got := string(out)
	// "container-role" is how the CLI names credentials fetched from this kind of endpoint.
	if !strings.Contains(got, "container-role") {
		t.Errorf("aws did not source credentials from the endpoint:\n%s", got)
	}
	// The last four characters of our access key, which is all the CLI prints.
	if !strings.Contains(got, "OINT") {
		t.Errorf("aws did not report our access key:\n%s", got)
	}
}
