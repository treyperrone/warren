package aws

import (
	"strings"
	"testing"
	"time"
)

func envMap(t *testing.T, pairs []string) map[string]string {
	t.Helper()
	m := make(map[string]string, len(pairs))
	for _, kv := range pairs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			t.Fatalf("env entry %q has no '='", kv)
		}
		m[k] = v
	}
	return m
}

func TestEnvWithResolvedCredentials(t *testing.T) {
	s := &Session{
		AccessKeyID:     "AKIAEXAMPLE",
		SecretAccessKey: "secret",
		SessionToken:    "token",
		Region:          "eu-west-2",
	}
	got := envMap(t, s.Env())

	for k, want := range map[string]string{
		"AWS_ACCESS_KEY_ID":     "AKIAEXAMPLE",
		"AWS_SECRET_ACCESS_KEY": "secret",
		"AWS_SESSION_TOKEN":     "token",
		"AWS_DEFAULT_REGION":    "eu-west-2",
		"AWS_REGION":            "eu-west-2",
	} {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
	// A key-bearing session must not also name a profile, or which one wins depends on the
	// consuming SDK's provider order rather than on what was selected.
	if _, ok := got["AWS_PROFILE"]; ok {
		t.Error("AWS_PROFILE set alongside explicit credentials")
	}
}

// The bug this guards: a profile-backed session with no resolved key used to emit
// AWS_ACCESS_KEY_ID= (empty). botocore, which the aws CLI is built on, reads that as partial
// credentials and errors instead of falling through to the profile.
func TestEnvWithProfileEmitsNoEmptyCredentials(t *testing.T) {
	s := &Session{ProfileName: "crlab", Region: "us-east-1"}
	got := envMap(t, s.Env())

	if got["AWS_PROFILE"] != "crlab" {
		t.Errorf("AWS_PROFILE = %q, want crlab", got["AWS_PROFILE"])
	}
	for _, k := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN"} {
		if v, ok := got[k]; ok {
			t.Errorf("%s is set to %q; it must be absent, not empty", k, v)
		}
	}
}

// An empty region must be absent rather than set-and-empty, which would suppress the region
// the profile or the SDK would otherwise supply.
func TestEnvOmitsUnknownRegion(t *testing.T) {
	s := &Session{AccessKeyID: "AKIA", SecretAccessKey: "s", SessionToken: "t"}
	got := envMap(t, s.Env())

	for _, k := range []string{"AWS_REGION", "AWS_DEFAULT_REGION"} {
		if v, ok := got[k]; ok {
			t.Errorf("%s is set to %q; it must be absent", k, v)
		}
	}
}

// EnvKeys drives the strip list callers use to build a child environment. If Env learns a new
// variable and EnvKeys does not, a stale value from the user's shell survives into the child
// and silently competes with the selected session.
func TestEnvKeysCoversEverythingEnvSets(t *testing.T) {
	known := make(map[string]bool, len(EnvKeys()))
	for _, k := range EnvKeys() {
		known[k] = true
	}

	// Both shapes, since they set different variables.
	for _, s := range []*Session{
		{AccessKeyID: "AKIA", SecretAccessKey: "s", SessionToken: "t", Region: "us-east-1"},
		{ProfileName: "p", Region: "us-east-1"},
	} {
		for k := range envMap(t, s.Env()) {
			if !known[k] {
				t.Errorf("Env sets %s but EnvKeys does not list it", k)
			}
		}
	}
}

func TestExpiresIn(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		expires time.Time
		want    string
	}{
		// Long-lived IAM keys have no expiry; inventing one would be a lie.
		{"unknown", time.Time{}, ""},
		{"already past", now.Add(-time.Minute), "expired"},
		{"exactly now", now, "expired"},
		{"under an hour", now.Add(45 * time.Minute), "45m"},
		{"over an hour", now.Add(90 * time.Minute), "1h30m"},
		// Seconds are noise on hour-long credentials, so they round away.
		{"rounds to the minute", now.Add(45*time.Minute + 20*time.Second), "45m"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Session{Expires: tt.expires}
			if got := s.ExpiresIn(now); got != tt.want {
				t.Errorf("ExpiresIn() = %q, want %q", got, tt.want)
			}
		})
	}
}
