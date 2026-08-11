package aws

import (
	"fmt"
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
	var env []string

	if s.AccessKeyID != "" {
		env = append(env,
			"AWS_ACCESS_KEY_ID="+s.AccessKeyID,
			"AWS_SECRET_ACCESS_KEY="+s.SecretAccessKey,
			"AWS_SESSION_TOKEN="+s.SessionToken,
		)
	} else if s.ProfileName != "" {
		env = append(env, "AWS_PROFILE="+s.ProfileName)
	}

	// Same reasoning as above: an empty region is worse than an unset one, since it
	// suppresses the region the profile or the SDK would otherwise supply.
	if s.Region != "" {
		env = append(env,
			"AWS_DEFAULT_REGION="+s.Region,
			"AWS_REGION="+s.Region,
		)
	}
	return env
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
	}
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
