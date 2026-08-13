package aws

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Creds returns raw credential fields for direct SDK use.
func (s *Session) Creds() (accessKey, secretKey, sessionToken, region string) {
	return s.AccessKeyID, s.SecretAccessKey, s.SessionToken, s.Region
}

// Env returns the session as AWS environment variable strings for os/exec.
//
// With no resolved access key it names the profile instead of emitting empty key variables.
// An empty-but-set variable is not universally read as absent: botocore, which the aws CLI
// is built on, reports partial credentials rather than falling through to the next provider,
// so emitting `AWS_ACCESS_KEY_ID=` would break the very command this is meant to enable.
func (s *Session) Env() []string {
	return append(s.CredentialEnv(), s.RegionEnv()...)
}

// CredentialEnv is only the part that answers "who am I". It is separate from RegionEnv because a
// caller serving credentials from an endpoint must replace this and keep the region: the SDK's
// environment provider is consulted before the endpoint, so leaving these in would pin a child to
// a frozen copy and make the endpoint pointless.
func (s *Session) CredentialEnv() []string {
	if s.AccessKeyID != "" {
		return []string{
			"AWS_ACCESS_KEY_ID=" + s.AccessKeyID,
			"AWS_SECRET_ACCESS_KEY=" + s.SecretAccessKey,
			"AWS_SESSION_TOKEN=" + s.SessionToken,
		}
	}
	if s.ProfileName != "" {
		return []string{"AWS_PROFILE=" + s.ProfileName}
	}
	return nil
}

// RegionEnv names the region, if one is known.
//
// Same reasoning as CredentialEnv's empty-value guard: an empty region is worse than an unset one,
// since it suppresses the region the profile or the SDK would otherwise supply.
func (s *Session) RegionEnv() []string {
	if s.Region == "" {
		return nil
	}
	return []string{
		"AWS_DEFAULT_REGION=" + s.Region,
		"AWS_REGION=" + s.Region,
	}
}

// EnvKeys lists every variable Env may set. Callers building a child environment strip these
// from the parent's copy first, so a stale AWS_PROFILE or key pair already in the user's
// shell cannot compete with the session actually selected.
func EnvKeys() []string {
	return []string{
		"AWS_ACCESS_KEY_ID",
		"AWS_SECRET_ACCESS_KEY",
		"AWS_SESSION_TOKEN",
		"AWS_PROFILE",
		"AWS_DEFAULT_REGION",
		"AWS_REGION",
		// Not set by Env, but stripped for the same reason: a stale endpoint inherited from an
		// outer warren would otherwise compete with whatever this one supplies.
		"AWS_CONTAINER_CREDENTIALS_FULL_URI",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
		"AWS_CONTAINER_AUTHORIZATION_TOKEN",
	}
}

// NeutralizedProfilePaths are the AWS_CONFIG_FILE and AWS_SHARED_CREDENTIALS_FILE values a child
// process is given to stop it falling back to an ambient [default] profile — a bare profile in
// ~/.aws/credentials needs no AWS_PROFILE to be reached, and it was found to answer ahead of both
// injected credentials and a credential endpoint. Neither path is ever created; a missing config
// or credentials file is treated as "no profiles defined," not an error.
//
// Exported, and read back by ConfigPath below, for a reason specific to this tool: warren itself
// is a consumer of AWS_CONFIG_FILE, to find the very sso-sessions its own picker lists. `warren
// exec`/`shell` spawn a child that carries this sentinel so *its* AWS calls cannot see the ambient
// profile — but running `warren` again from inside that child (its normal, expected shell) would
// otherwise inherit the sentinel and be unable to find the user's real ~/.aws/config, reproducing
// on warren itself the exact failure this was built to prevent for everything else. ConfigPath
// recognising its own sentinel and discarding it is what keeps those two purposes from colliding.
func NeutralizedProfilePaths() (configFile, credentialsFile string) {
	dir := filepath.Join(os.TempDir(), "warren-no-ambient-aws-profile")
	return filepath.Join(dir, "config"), filepath.Join(dir, "credentials")
}

// ExpiresIn renders the time left on the credentials, or "" when the expiry is unknown —
// which is the honest answer for a profile backed by long-lived keys, and better than
// inventing a number. now is a parameter so this is testable without freezing the clock.
func (s *Session) ExpiresIn(now time.Time) string {
	if s.Expires.IsZero() {
		return ""
	}
	left := s.Expires.Sub(now)
	if left <= 0 {
		return "expired"
	}
	// Rounded to the minute: these credentials last an hour, so seconds are noise.
	left = left.Round(time.Minute)
	if h := int(left.Hours()); h > 0 {
		return fmt.Sprintf("%dh%02dm", h, int(left.Minutes())%60)
	}
	return fmt.Sprintf("%dm", int(left.Minutes()))
}

// BuildLabel sets the display label from resolved account/role info.
func (s *Session) BuildLabel(acctName, role string) {
	s.AccountName = acctName
	s.RoleName = role
	s.Label = fmt.Sprintf("%s (%s)/%s", acctName, s.AccountID, role)
}
