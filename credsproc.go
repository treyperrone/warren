package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	awsint "github.com/treyperrone/warren/internal/aws"
)

// credsInvocation is `warren creds` as parsed: which session to authenticate against and
// which account/role to vend credentials for.
type credsInvocation struct {
	session string
	account string
	role    string
}

func parseCredsArgs(args []string) (credsInvocation, error) {
	var inv credsInvocation
	set := func(dst *string, flag, val string) error {
		if val == "" {
			return fmt.Errorf("%s needs a value", flag)
		}
		if *dst != "" {
			return fmt.Errorf("%s given twice", flag)
		}
		*dst = val
		return nil
	}
	for i := 0; i < len(args); i++ {
		var err error
		next := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch args[i] {
		case "--session":
			err = set(&inv.session, "--session", next())
		case "--account":
			err = set(&inv.account, "--account", next())
		case "--role":
			err = set(&inv.role, "--role", next())
		default:
			err = fmt.Errorf("unknown argument %q", args[i])
		}
		if err != nil {
			return inv, err
		}
	}
	if inv.session == "" || inv.account == "" || inv.role == "" {
		return inv, errors.New("creds needs --session, --account and --role")
	}
	return inv, nil
}

// processCreds is the credential_process output contract, field for field:
// https://docs.aws.amazon.com/cli/latest/userguide/cli-configure-sourcing-external.html
type processCreds struct {
	Version         int    `json:"Version"`
	AccessKeyID     string `json:"AccessKeyId"`
	SecretAccessKey string `json:"SecretAccessKey"`
	SessionToken    string `json:"SessionToken"`
	Expiration      string `json:"Expiration,omitempty"`
}

// runCreds is `warren creds`: the credential_process provider. An SDK invokes it whenever a
// profile's credentials are needed or expired, so this — not any daemon or file rewriting —
// is what keeps a one-hour org policy alive for tools warren did not launch: every call
// vends fresh role credentials off the silently-renewed SSO token.
//
// Strictly non-interactive: the caller is botocore or the Go SDK holding a pipe, and device
// auth from here would hang every aws command on the machine behind an invisible prompt.
// When the SSO session itself has ended, the only honest move is a clear instruction on
// stderr — which the aws CLI surfaces verbatim — and a non-zero exit.
// credsTimeout bounds every AWS call warren creds makes. Its caller is an SDK holding a pipe
// open, waiting on stdout — "strictly non-interactive, never a hang" only holds if a stalled
// network call can't block that pipe forever the way a browser prompt already can't.
const credsTimeout = 15 * time.Second

func runCreds(ctx context.Context, args []string) int {
	inv, err := parseCredsArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}

	ctx, cancel := context.WithTimeout(ctx, credsTimeout)
	defer cancel()

	sessions, _, err := awsint.ParseConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren creds: %v\n", err)
		return 1
	}
	var sess *awsint.SSOSessionConfig
	for i := range sessions {
		if sessions[i].Name == inv.session {
			sess = &sessions[i]
			break
		}
	}
	if sess == nil {
		fmt.Fprintf(os.Stderr, "warren creds: no sso-session named %q in ~/.aws/config\n", inv.session)
		return 1
	}

	token, err := awsint.SilentToken(ctx, *sess)
	if err != nil {
		fmt.Fprintln(os.Stderr, credsErrorMessage(sess.Name, err))
		return 1
	}

	role, err := awsint.GetRoleCredentials(ctx, *sess, token, inv.account, inv.role)
	if err != nil {
		fmt.Fprintln(os.Stderr, credsErrorMessage(sess.Name, err))
		return 1
	}
	return emitCreds(role)
}

// credsErrorMessage classifies an AWS error from the creds path into the line printed on
// stderr, so a script sees why it failed instead of a raw SDK error it has no way to act on.
// Both call sites above funnel through this: NeedsReauth already matches ErrLoginRequired (the
// sentinel SilentToken always falls back to — deliberately, since it collapses every failure,
// network blips included, rather than risk mistaking one for the org's session ceiling, see
// SilentToken's own comment), and a deadline is only ever reachable from GetRoleCredentials,
// which unlike SilentToken lets a context error surface as itself.
func credsErrorMessage(sessName string, err error) string {
	switch {
	case awsint.NeedsReauth(err):
		return fmt.Sprintf("warren creds: the %s session needs a sign-in — run: warren login %s", sessName, sessName)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Sprintf("warren creds: timed out reaching AWS after %s — check network/VPN connectivity", credsTimeout)
	default:
		return fmt.Sprintf("warren creds: %v", err)
	}
}

// emitCreds writes the JSON document to stdout — the ONLY thing that may reach stdout in
// this mode, because the SDK parses the stream, not the lines.
func emitCreds(s *awsint.Session) int {
	out := processCreds{
		Version:         1,
		AccessKeyID:     s.AccessKeyID,
		SecretAccessKey: s.SecretAccessKey,
		SessionToken:    s.SessionToken,
	}
	// Expiration is what tells the SDK when to call again; the contract reads a missing field
	// as LONG-TERM credentials, cached for the process's life. GetRoleCredentials always sets
	// Expires — synthesizing a conservative deadline itself when AWS's response omits one — so
	// there is nothing to guard against here beyond the type's own zero value.
	if !s.Expires.IsZero() {
		out.Expiration = s.Expires.UTC().Format(time.RFC3339)
	}
	data, err := json.Marshal(out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren creds: %v\n", err)
		return 1
	}
	fmt.Println(string(data))
	return 0
}
