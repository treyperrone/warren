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
func runCreds(ctx context.Context, args []string) int {
	inv, err := parseCredsArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}

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
	if errors.Is(err, awsint.ErrLoginRequired) {
		fmt.Fprintf(os.Stderr, "warren creds: the %s session needs a sign-in — run: warren login %s\n", sess.Name, sess.Name)
		return 1
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren creds: %v\n", err)
		return 1
	}

	role, err := awsint.GetRoleCredentials(ctx, *sess, token, inv.account, inv.role)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren creds: %v\n", err)
		return 1
	}
	return emitCreds(role)
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
	// Expiration is what tells the SDK when to call again; the contract reads a missing
	// field as LONG-TERM credentials, cached for the process's life. These are one-hour STS
	// credentials whatever the API said, so when it did not say (Expiration==0), a
	// conservative synthetic deadline keeps callers re-asking instead of riding dead keys.
	exp := s.Expires
	if exp.IsZero() && s.SessionToken != "" {
		exp = time.Now().Add(15 * time.Minute)
	}
	if !exp.IsZero() {
		out.Expiration = exp.UTC().Format(time.RFC3339)
	}
	data, err := json.Marshal(out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren creds: %v\n", err)
		return 1
	}
	fmt.Println(string(data))
	return 0
}
