package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	awsint "github.com/treyperrone/warren/internal/aws"
	"github.com/treyperrone/warren/internal/awsexec"
	"github.com/treyperrone/warren/internal/browser"
	"github.com/treyperrone/warren/internal/credserver"
)

// favoriteByNickname reports whether name is a saved favorite, so parseArgs can tell
// `warren exec corp-admin -- aws s3 ls` apart from `warren exec aws s3 ls` without inventing
// flag syntax: an exact nickname match is a favorite, anything else is the command it always
// was. Nicknames are slugs (corp-lab-adminrole), so shadowing a real executable name takes
// deliberate effort.
func favoriteByNickname(name string) (browser.Favorite, bool) {
	for _, f := range browser.Favorites() {
		if f.Nickname == name {
			return f, true
		}
	}
	return browser.Favorite{}, false
}

// runFavorite is `warren exec <favorite> -- cmd` / `warren shell <favorite>`: credentials
// for a bookmarked account+role with no picker and no saved AWS profile. The token must
// already be live (warren login first) — this path exists for muscle memory and scripts,
// and a surprise device-auth inside a scripted command is a hang, not a feature.
//
// The child reads credentials from the same loopback endpoint the TUI uses, renewed on the
// same cadence, so a shell or long command opened this way survives the one-hour mark just
// like one opened through the picker.
func runFavorite(ctx context.Context, fav browser.Favorite, run invocation) int {
	sessions, _, err := awsint.ParseConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	var ssoCfg *awsint.SSOSessionConfig
	for i := range sessions {
		if sessions[i].StartURL == fav.StartURL {
			ssoCfg = &sessions[i]
			break
		}
	}
	if ssoCfg == nil {
		fmt.Fprintf(os.Stderr, "favorite %s points at %s, which no [sso-session] in ~/.aws/config uses anymore\n",
			fav.Nickname, fav.StartURL)
		return 1
	}

	fetch := func(ctx context.Context) (*awsint.Session, error) {
		token, err := awsint.SilentToken(ctx, *ssoCfg)
		if err != nil {
			return nil, err
		}
		sess, err := awsint.GetRoleCredentials(ctx, *ssoCfg, token, fav.AccountID, fav.Role)
		if err != nil {
			return nil, err
		}
		sess.AccountID = fav.AccountID
		sess.BuildLabel(fav.AccountName, fav.Role)
		return sess, nil
	}

	sess, err := fetch(ctx)
	if errors.Is(err, awsint.ErrLoginRequired) {
		fmt.Fprintf(os.Stderr, "%s needs a sign-in first — run: warren login %s\n", fav.Nickname, ssoCfg.Name)
		return 1
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	// Identity to stderr, like the picker flows: stdout belongs to the wrapped command.
	who := sess.Label
	if left := sess.ExpiresIn(time.Now()); left != "" {
		who += "  •  credentials expire in " + left
	}
	fmt.Fprintf(os.Stderr, "%s\n", who)
	if run.mode == modeShell {
		fmt.Fprintf(os.Stderr, "%s is set; exit the shell to return.\n", awsexec.SessionLabelVar)
	}

	// Serve credentials over loopback rather than handing the child a copy, exactly like the
	// picker flows and for the same reason; the cadence constants match internal/tui.
	var credEnv []string
	if srv, err := credserver.Start(); err == nil {
		srv.Set(sess)
		keepCtx, cancel := context.WithCancel(ctx)
		go srv.KeepFresh(keepCtx, 30*time.Second, 10*time.Minute, fetch)
		credEnv = srv.Env()
		defer func() {
			cancel()
			_ = srv.Close()
		}()
	} else {
		fmt.Fprintf(os.Stderr, "note: credential endpoint unavailable (%v); "+
			"credentials will not renew inside this process\n", err)
	}

	code, err := awsexec.Run(sess, run.argv, credEnv...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}
	return code
}
