package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	awsint "github.com/treyperrone/warren/internal/aws"
	"github.com/treyperrone/warren/internal/homedir"
)

// The S3 browser: buckets → delimiter-level object listing → download on Enter, upload via
// a screen that accepts a dragged file. It rides the same list widget as every other
// screen, so "/" search and the movement keys come free.

const actionS3 = "s3"

type msgS3Buckets struct {
	buckets []string
	err     error
}

// msgS3Objects carries one listed level plus the coordinates it belongs to, so a slow
// listing that lands after the user moved on cannot mislabel the screen.
type msgS3Objects struct {
	bucket  string
	region  string
	prefix  string
	entries []awsint.S3Entry
	err     error
}

// msgS3Done reports a finished transfer; refresh asks for the current level to be relisted
// (an upload changed it, a download did not).
type msgS3Done struct {
	note    string
	refresh bool
	err     error
}

// liveSessionSource hands transfers a getter for the freshest credentials the TUI holds.
// The event loop stores into the atomic box everywhere m.awsSess changes; the transfer
// goroutine only ever loads — no lock ordering, no reading Model fields off-thread.
func (m *Model) liveSessionSource() awsint.SessionSource {
	m.liveSess.Store(m.awsSess)
	box := &m.liveSess
	return func() *awsint.Session {
		s, _ := box.Load().(*awsint.Session)
		return s
	}
}

func (m *Model) fetchS3Buckets() tea.Cmd {
	get := m.liveSessionSource()
	ctx := m.ctx
	return func() tea.Msg {
		buckets, err := awsint.ListBuckets(ctx, get)
		return msgS3Buckets{buckets: buckets, err: err}
	}
}

// fetchS3Level lists one delimiter level, resolving the bucket's region on first entry —
// buckets are global, transfers are not, and cached thereafter.
func (m *Model) fetchS3Level(bucket, region, prefix string) tea.Cmd {
	get := m.liveSessionSource()
	ctx := m.ctx
	return func() tea.Msg {
		if region == "" {
			r, err := awsint.BucketRegion(ctx, get, bucket)
			if err != nil {
				return msgS3Objects{err: err}
			}
			region = r
		}
		entries, err := awsint.ListS3Objects(ctx, get, bucket, region, prefix)
		return msgS3Objects{bucket: bucket, region: region, prefix: prefix, entries: entries, err: err}
	}
}

func (m *Model) buildS3BucketList() {
	var items []list.Item
	for _, b := range m.s3Buckets {
		items = append(items, item{title: b, desc: "enter to browse", value: b})
	}
	m.list.Title = "S3 buckets  •  " + m.credSummary()
	m.list.SetStatusBarItemName("bucket", "buckets")
	m.setListItems(items)
	m.list.Select(0)
}

func (m *Model) buildS3ObjectList() {
	items := []list.Item{item{
		title: "⇪ Upload to this location",
		desc:  "drag a file into this window on the next screen, or paste a path",
		value: "upload",
	}}
	for _, e := range m.s3Entries {
		if e.IsPrefix {
			items = append(items, item{title: "📁 " + e.Name(), desc: "enter to open", value: "p:" + e.Key})
			continue
		}
		items = append(items, item{
			title: e.Name(),
			desc:  fmt.Sprintf("%s  •  %s  •  enter downloads", humanSize(e.Size), e.Modified.Local().Format("2006-01-02 15:04")),
			value: "o:" + e.Key,
		})
	}
	loc := "s3://" + m.s3Bucket + "/" + m.s3Prefix
	m.list.Title = loc + "  •  Esc=up"
	m.list.SetStatusBarItemName("entry", "entries")
	m.setListItems(items)
	m.list.Select(0)
}

func (m *Model) selectS3Bucket(name string) tea.Cmd {
	m.s3Bucket, m.s3Region, m.s3Prefix = name, "", ""
	m.loading = true
	return m.fetchS3Level(name, "", "")
}

func (m *Model) selectS3Entry(val string) tea.Cmd {
	switch {
	case val == "upload":
		m.s3Input = textinput.New()
		m.s3Input.Placeholder = "drag a file into this window, or paste a path"
		m.s3Input.Focus()
		m.s3Input.Width = 70
		m.s3UploadErr = ""
		m.screen = screenS3Upload
		return textinput.Blink

	case strings.HasPrefix(val, "p:"):
		// Passed, not committed: m.s3Prefix changes only when the listing for the new
		// level actually arrives (msgS3Objects), so a failed descend cannot leave uploads
		// targeting a level the screen never showed.
		m.loading = true
		return m.fetchS3Level(m.s3Bucket, m.s3Region, strings.TrimPrefix(val, "p:"))

	case strings.HasPrefix(val, "o:"):
		key := strings.TrimPrefix(val, "o:")
		get, bucket, region := m.liveSessionSource(), m.s3Bucket, m.s3Region
		ctx := m.ctx
		m.loading = true
		return func() tea.Msg {
			dest, err := awsint.DownloadObject(ctx, get, bucket, region, key, downloadDir())
			if err != nil {
				return msgS3Done{err: err}
			}
			return msgS3Done{note: "downloaded to " + dest}
		}
	}
	return nil
}

// updateS3Upload owns the keyboard while the upload screen is up: every rune belongs to the
// path box — including the pasted path a terminal synthesizes when a file is DROPPED on the
// window, which is what makes drag-and-drop real in a TUI.
func (m *Model) updateS3Upload(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.buildS3ObjectList()
		m.screen = screenS3Objects
		return m, nil
	case "enter":
		path, err := cleanDroppedPath(m.s3Input.Value())
		if err != nil {
			m.s3UploadErr = err.Error()
			return m, nil
		}
		get, bucket, region, prefix := m.liveSessionSource(), m.s3Bucket, m.s3Region, m.s3Prefix
		ctx := m.ctx
		m.loading = true
		m.screen = screenS3Objects
		return m, func() tea.Msg {
			key, err := awsint.UploadFile(ctx, get, bucket, region, prefix, path)
			if err != nil {
				return msgS3Done{err: err}
			}
			return msgS3Done{note: "uploaded s3://" + bucket + "/" + key, refresh: true}
		}
	}
	var cmd tea.Cmd
	m.s3Input, cmd = m.s3Input.Update(msg)
	return m, cmd
}

func (m *Model) s3UploadView() string {
	b := m.banner() + "\n" +
		styleTitle.MarginLeft(2).Render("Upload to s3://"+m.s3Bucket+"/"+m.s3Prefix) + "\n\n" +
		"  " + m.s3Input.View() + "\n\n"
	if m.s3UploadErr != "" {
		b += styleErr.MarginLeft(2).Render(m.s3UploadErr) + "\n\n"
	}
	return b + styleDim.MarginLeft(2).Render("drop a file onto this window (the terminal pastes its path) • enter uploads • esc cancels") + "\n"
}

// cleanDroppedPath turns what a terminal pastes on file drop into a usable path. Every
// terminal quotes differently: Terminal.app backslash-escapes spaces, iTerm2 and Windows
// Terminal wrap in quotes, most append a trailing space. The file must exist and be a file
// — catching that here beats a five-second upload attempt ending in an SDK error.
func cleanDroppedPath(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	quoted := false
	if len(p) >= 2 && (p[0] == '"' && p[len(p)-1] == '"' || p[0] == '\'' && p[len(p)-1] == '\'') {
		p = p[1 : len(p)-1]
		quoted = true
	}
	// Backslash-unescaping is a Unix-terminal convention (Terminal.app escapes spaces,
	// parens, quotes, ampersands — anything shell-special — on drop) and only applies to
	// UNQUOTED pastes: quoted ones are already literal. Never on Windows, where backslash
	// IS the path separator and "un-escaping" C:\Program Files would eat it.
	if runtime.GOOS != "windows" && !quoted {
		p = unescapeShellChars(p)
	}
	if strings.HasPrefix(p, "~/") {
		p = filepath.Join(homedir.Dir(), p[2:])
	}
	if p == "" {
		return "", fmt.Errorf("no file given — drag one into the window or paste a path")
	}
	fi, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %v", p, err)
	}
	if fi.IsDir() {
		return "", fmt.Errorf("%s is a directory — drag a single file (use `aws s3 sync` for trees)", p)
	}
	return p, nil
}

// unescapeShellChars strips the backslash a Unix terminal puts before shell-special
// characters when pasting a dropped file's path: \( \) \& \' \" \space and friends
// become their literal selves. Only backslashes before non-alphanumerics are eaten, so an
// actual backslash-letter sequence in a filename survives.
func unescapeShellChars(p string) string {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		if p[i] == '\\' && i+1 < len(p) {
			next := p[i+1]
			isAlnum := next >= 'a' && next <= 'z' || next >= 'A' && next <= 'Z' || next >= '0' && next <= '9'
			if !isAlnum {
				b.WriteByte(next)
				i++
				continue
			}
		}
		b.WriteByte(p[i])
	}
	return b.String()
}

// downloadDir is ~/Downloads where it exists — where every browser has trained people to
// look — and the working directory otherwise.
func downloadDir() string {
	d := filepath.Join(homedir.Dir(), "Downloads")
	if fi, err := os.Stat(d); err == nil && fi.IsDir() {
		return d
	}
	return "."
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// parentPrefix walks one delimiter level up: "a/b/c/" → "a/b/", "a/" → "".
func parentPrefix(p string) string {
	p = strings.TrimSuffix(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[:i+1]
	}
	return ""
}
