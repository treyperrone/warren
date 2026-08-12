package tui

import (
	"strings"
	"testing"
)

// The values here all originate in AWS API responses or ~/.aws/config, so none of them can be
// assumed shell-safe. Each must survive intact as its own argv element rather than being spliced
// into a command string.
func TestTmuxArgsPassValuesAsArgvNotShellText(t *testing.T) {
	env := []string{
		"AWS_ACCESS_KEY_ID=AKIAEXAMPLE",
		"AWS_PROFILE=my prod account",                      // a space is legal in a profile name
		"WARREN_SESSION=globogym (195170887130)/AdminRole", // Label always looks like this
		"AWS_SECRET_ACCESS_KEY=a;rm -rf /;b",               // if it ever reached a shell
		"PATH=/usr/bin",                                    // not ours, must not be forwarded
	}
	argv := []string{"/cache/session-manager-plugin", `{"SessionId":"s-1","TokenValue":"it's"}`}

	got := tmuxNewSessionArgs("warren-1", "/cache/warren/tmux.conf", 100, 29, env, argv)

	// Every forwarded value must appear verbatim as one element.
	for _, want := range []string{
		"AWS_PROFILE=my prod account",
		"WARREN_SESSION=globogym (195170887130)/AdminRole",
		"AWS_SECRET_ACCESS_KEY=a;rm -rf /;b",
		`{"SessionId":"s-1","TokenValue":"it's"}`,
	} {
		if !containsExact(got, want) {
			t.Errorf("%q is not a standalone argv element; args = %q", want, got)
		}
	}

	// No argument may be a shell fragment: no "export VAR=x; " concatenation, and nothing
	// wrapped in quotes by us. An apostrophe *inside* a value is fine and is why the JSON
	// above contains one — the point is that it passes through unmangled rather than being
	// escaped or terminating a quoted string.
	for _, arg := range got {
		if strings.Contains(arg, "export ") {
			t.Errorf("argument %q builds shell text instead of argv", arg)
		}
		if len(arg) > 1 && strings.HasPrefix(arg, "'") && strings.HasSuffix(arg, "'") {
			t.Errorf("argument %q was quote-wrapped for a shell that is no longer involved", arg)
		}
	}

	// The command must sit after "--" so tmux execs it rather than running a shell.
	sep := indexOf(got, "--")
	if sep < 0 {
		t.Fatalf(`no "--" separator in %q`, got)
	}
	if len(got) != sep+1+len(argv) {
		t.Errorf("expected exactly the command after --, got %q", got[sep+1:])
	}

	// Variables warren did not set stay out of it.
	if containsExact(got, "PATH=/usr/bin") {
		t.Error("forwarded PATH, which should be inherited rather than passed with -e")
	}
}

func TestTmuxArgsCarryGeometrySocketAndConf(t *testing.T) {
	got := tmuxNewSessionArgs("warren-42", "/cache/warren/tmux.conf", 120, 39, nil, []string{"plugin"})
	for _, pair := range [][2]string{{"-L", "warren-42"}, {"-x", "120"}, {"-y", "39"}, {"-f", "/cache/warren/tmux.conf"}} {
		if i := indexOf(got, pair[0]); i < 0 || i+1 >= len(got) || got[i+1] != pair[1] {
			t.Errorf("%s %s missing from %q", pair[0], pair[1], got)
		}
	}
}

func containsExact(hay []string, needle string) bool {
	return indexOf(hay, needle) >= 0
}

func indexOf(hay []string, needle string) int {
	for i, s := range hay {
		if s == needle {
			return i
		}
	}
	return -1
}
