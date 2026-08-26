package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	awsint "github.com/treyperrone/warren/internal/aws"
	"github.com/treyperrone/warren/internal/browser"
)

func hintKeys(hs []keyHint) string {
	var b strings.Builder
	for _, h := range hs {
		b.WriteString(h.key + "=" + h.label + ";")
	}
	return b.String()
}

// The x key is advertised exactly where it works: on a favorite row, and nowhere else. A footer
// that promised "x remove favorite" over an SSO session row would be teaching a key that
// silently does nothing there.
func TestFooterAdvertisesRemoveOnlyOnFavoriteRows(t *testing.T) {
	m := modelWithSSOSession(t)
	if err := browser.AddFavorite(browser.Favorite{Nickname: "prod-admin", StartURL: m.ssoSessions[0].StartURL, AccountID: "1", Role: "Admin"}); err != nil {
		t.Fatal(err)
	}
	m.buildMethodList()
	m.screen = screenMethod

	if sel, _ := m.list.SelectedItem().(item); !strings.HasPrefix(sel.value, "fav:") {
		t.Fatalf("favorite is not the first row: %+v", sel)
	}
	if got := hintKeys(m.footerHints()); !strings.Contains(got, "x=remove favorite") {
		t.Errorf("favorite row footer lacks x: %s", got)
	}

	m.list.Select(1) // the session row under it
	if got := hintKeys(m.footerHints()); strings.Contains(got, "x=") {
		t.Errorf("session row footer advertises x: %s", got)
	}
	// The first screen has no "back".
	if got := hintKeys(m.footerHints()); strings.Contains(got, "esc=") {
		t.Errorf("method screen advertises esc: %s", got)
	}
}

// Enter's verb follows the cursor through an S3 listing, and esc says where it actually goes.
func TestFooterFollowsTheS3Cursor(t *testing.T) {
	m := modelWithSSOSession(t)
	m.awsSess = &awsint.Session{Label: "x", AccessKeyID: "AKIA"}
	m.s3Bucket, m.s3Prefix = "b", "tools/"
	m.s3Entries = []awsint.S3Entry{{Key: "tools/linux/", IsPrefix: true}, {Key: "tools/mimi.zip", Size: 1}}
	m.buildS3ObjectList()
	m.screen = screenS3Objects

	for i, want := range []string{"enter=upload", "enter=open", "enter=download"} {
		m.list.Select(i)
		if got := hintKeys(m.footerHints()); !strings.HasPrefix(got, want+";") {
			t.Errorf("row %d: footer %q, want it to lead with %s", i, got, want)
		}
	}
	if got := hintKeys(m.footerHints()); !strings.Contains(got, "esc=up a level") {
		t.Errorf("nested level footer: %s", got)
	}
	m.s3Prefix = ""
	if got := hintKeys(m.footerHints()); !strings.Contains(got, "esc=back") {
		t.Errorf("bucket root footer: %s", got)
	}
}

// While a search is typed the list owns the keyboard, and once one is applied esc clears it
// before it means back — the footer has to say what Update will actually do.
func TestFooterTracksSearchState(t *testing.T) {
	m := accountModel(t)

	if got := hintKeys(m.footerHints()); !strings.Contains(got, "esc=back") {
		t.Fatalf("idle footer: %s", got)
	}
	applySearch(t, m, "globo")
	if got := hintKeys(m.footerHints()); !strings.Contains(got, "esc=clear search") || strings.Contains(got, "esc=back") {
		t.Errorf("filtered footer: %s", got)
	}
}

// Main screen keys are the ones its rows label, and esc is offered only when there is a hub to
// return to.
func TestFooterMainScreen(t *testing.T) {
	m := modelWithSSOSession(t)
	m.screen = screenMain
	m.buildMainList()
	got := hintKeys(m.footerHints())
	for _, want := range []string{"n=new connection", "p=switch account", "q=quit"} {
		if !strings.Contains(got, want) {
			t.Errorf("main footer lacks %s: %s", want, got)
		}
	}
	if strings.Contains(got, "esc=") {
		t.Errorf("main footer offers esc with no credentials: %s", got)
	}
	m.awsSess = &awsint.Session{Label: "x", AccessKeyID: "AKIA"}
	if got := hintKeys(m.footerHints()); !strings.Contains(got, "esc=back") {
		t.Errorf("main footer with creds: %s", got)
	}
}

// A footer must never wrap: a second row steals a list row and misaligns the screen. It sheds
// hints from the end instead, keeping the primary action.
func TestFooterNeverWraps(t *testing.T) {
	hints := []keyHint{{"enter", "select"}, {"x", "remove favorite"}, {"/", "search"}, {"esc", "back"}, {"q", "quit"}}
	for _, w := range []int{120, 60, 40, 24, 14} {
		out := renderFooter(hints, w)
		if got := lipgloss.Width(out); got > w {
			t.Errorf("width %d: footer is %d wide: %q", w, got, out)
		}
		if !strings.Contains(out, "select") {
			t.Errorf("width %d: primary action dropped: %q", w, out)
		}
	}
	if out := renderFooter(hints, 120); !strings.Contains(out, "quit") {
		t.Errorf("wide footer dropped hints: %q", out)
	}
}

// The list is sized one row shorter so the footer has a row of its own below it.
func TestFooterHasItsOwnRow(t *testing.T) {
	m := accountModel(t)
	m.width, m.height = 100, 40
	m.resizeList()
	view := m.View()
	rows := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if got := len(rows); got > m.height {
		t.Fatalf("view is %d rows for a %d-row terminal", got, m.height)
	}
	if last := rows[len(rows)-1]; !strings.Contains(last, "select") || !strings.Contains(last, "search") {
		t.Errorf("last row is not the footer: %q", last)
	}
}
