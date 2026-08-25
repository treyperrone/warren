package tui

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	awsint "github.com/treyperrone/warren/internal/aws"
)

// What a terminal pastes when a file is DROPPED on the window, per emulator: Terminal.app
// backslash-escapes spaces, iTerm2/Windows Terminal quote, most add a trailing space. This
// cleaning is the whole difference between "drag and drop works" and "weird paste noise".
func TestCleanDroppedPath(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "image.iso")
	spaced := filepath.Join(dir, "my file.bin")
	for _, p := range []string{plain, spaced} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cases := map[string]string{
		"plain":             plain,
		"trailing space":    plain + " ",
		"double quoted":     `"` + plain + `"`,
		"single quoted":     "'" + plain + "'",
		"quoted with space": `"` + spaced + `"`,
	}
	// Backslash-escaping is a Unix-emulator behavior (Terminal.app); on Windows a backslash
	// is a path separator and cleanDroppedPath deliberately leaves it alone.
	if runtime.GOOS != "windows" {
		cases["escaped spaces"] = strings.ReplaceAll(spaced, " ", `\ `)
	}
	for name, raw := range cases {
		got, err := cleanDroppedPath(raw)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got != plain && got != spaced {
			t.Errorf("%s: cleaned to %q", name, got)
		}
	}

	if _, err := cleanDroppedPath(""); err == nil {
		t.Error("empty input accepted")
	}
	if _, err := cleanDroppedPath(dir); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Errorf("directory accepted or wrong error: %v", err)
	}
	if _, err := cleanDroppedPath(filepath.Join(dir, "never-existed")); err == nil {
		t.Error("missing file accepted")
	}
}

// The object listing renders folders first with the upload row pinned on top, names shown
// as their last path segment, and values carrying the full key for selection.
func TestBuildS3ObjectList(t *testing.T) {
	m := modelWithSSOSession(t)
	m.awsSess = &awsint.Session{Label: "x", AccessKeyID: "AKIA"}
	m.s3Bucket, m.s3Prefix = "range-drops", "tools/"
	m.s3Entries = []awsint.S3Entry{
		{Key: "tools/linux/", IsPrefix: true},
		{Key: "tools/mimi.zip", Size: 5 << 20},
	}
	m.buildS3ObjectList()

	items := m.list.Items()
	first := items[0].(item)
	if first.value != "upload" {
		t.Fatalf("first row = %+v, want the upload row pinned", first)
	}
	if dirRow := items[1].(item); dirRow.value != "p:tools/linux/" || !strings.Contains(dirRow.title, "linux/") {
		t.Errorf("prefix row = %+v", dirRow)
	}
	objRow := items[2].(item)
	if objRow.value != "o:tools/mimi.zip" || objRow.title != "mimi.zip" {
		t.Errorf("object row = %+v", objRow)
	}
	if !strings.Contains(objRow.desc, "5.0 MiB") {
		t.Errorf("object desc = %q, want a human size", objRow.desc)
	}
	if !strings.Contains(m.list.Title, "s3://range-drops/tools/") {
		t.Errorf("title = %q", m.list.Title)
	}
}

func TestParentPrefix(t *testing.T) {
	for in, want := range map[string]string{
		"a/b/c/": "a/b/",
		"a/":     "",
		"":       "",
	} {
		if got := parentPrefix(in); got != want {
			t.Errorf("parentPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

// Esc from a nested level walks up one prefix — but the coordinates only COMMIT when the
// parent's listing arrives. A failed ascent must leave state and screen agreeing on the
// level actually shown, or an upload after a network blip targets a level nobody saw.
func TestS3GoBackWalksUp(t *testing.T) {
	m := modelWithSSOSession(t)
	m.awsSess = &awsint.Session{Label: "x", AccessKeyID: "AKIA"}
	m.s3Bucket, m.s3Region, m.s3Prefix = "b", "us-east-1", "a/b/"
	m.screen = screenS3Objects

	cmd := m.goBack()
	if cmd == nil || !m.loading {
		t.Fatalf("goBack from a/b/ started nothing (loading=%v)", m.loading)
	}
	if m.s3Prefix != "a/b/" {
		t.Fatalf("prefix committed before the listing succeeded: %q", m.s3Prefix)
	}

	// A failed ascent changes nothing but the error.
	m.Update(msgS3Objects{err: errors.New("blip")})
	if m.s3Prefix != "a/b/" {
		t.Fatalf("failed ascent moved the prefix to %q", m.s3Prefix)
	}

	// A successful one commits the message's own coordinates.
	m.Update(msgS3Objects{bucket: "b", region: "us-east-1", prefix: "a/"})
	if m.s3Prefix != "a/" || m.screen != screenS3Objects {
		t.Fatalf("success did not commit: prefix=%q screen=%v", m.s3Prefix, m.screen)
	}

	m.loading = false
	m.s3Prefix = ""
	m.screen = screenS3Objects
	m.goBack()
	if m.screen != screenS3Buckets {
		t.Fatalf("goBack from root = %v, want the bucket list", m.screen)
	}
}

// Terminal.app escapes every shell-special character on drop, not just spaces.
func TestUnescapeShellChars(t *testing.T) {
	if got := unescapeShellChars(`/tmp/report\(final\)\ v2\&more.pdf`); got != "/tmp/report(final) v2&more.pdf" {
		t.Errorf("got %q", got)
	}
	// Backslash before an alphanumeric is content, not an escape.
	if got := unescapeShellChars(`/tmp/a\b2`); got != `/tmp/a\b2` {
		t.Errorf("ate a literal backslash: %q", got)
	}
}
