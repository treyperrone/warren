package tunnel

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// OpenRDPClient launches the platform's RDP client pointed at the forwarded port, so an RDP
// tunnel connects itself instead of ending at "now paste localhost:13389 into something".
// The returned note says what happened either way — including the no-client case, where the
// old manual instruction is still the right answer. Launch failures degrade to that note
// rather than an error: the tunnel is up and usable, which is the part that matters.
// launch starts a client and reaps it in the background: warren keeps running to manage
// the tunnel, and a Start without Wait leaves the exited client a zombie for that whole
// lifetime on unix.
func launch(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// fullscreen selects the client's presentation; see browser.RDPScreen for the setting.
// instanceName is the EC2 Name tag, used to label the connection so a client's window and
// bookmark read "web-01" instead of "localhost-13389"; "" falls back to the address.
func OpenRDPClient(port int, user, instanceName string, fullscreen bool) string {
	manual := fmt.Sprintf("point your RDP client at localhost:%d", port)
	switch runtime.GOOS {
	case "windows":
		// mstsc ships with every Windows edition; /v is the documented connect-directly flag.
		args := []string{fmt.Sprintf("/v:localhost:%d", port)}
		if fullscreen {
			args = append(args, "/f")
		}
		if err := launch(exec.Command("mstsc.exe", args...)); err != nil {
			return manual + fmt.Sprintf(" (mstsc failed to start: %v)", err)
		}
		return fmt.Sprintf("opened mstsc → localhost:%d", port)

	case "darwin":
		// Windows App (né Microsoft Remote Desktop) opens .rdp documents; a temp file plus
		// open(1) survives the app's renames and URL-scheme changes across versions, which
		// an rdp:// URL has not. The file carries no secret — an address and a username —
		// and lands in a 0700 warren dir all the same. Windows App has no display-name key
		// in the .rdp format and labels the connection by the file's name, so the file is
		// named for the instance.
		path, err := writeRDPFile(port, user, instanceName, fullscreen)
		if err != nil {
			return manual
		}
		if err := launch(exec.Command("open", path)); err != nil {
			return manual
		}
		return fmt.Sprintf("opened Windows App → localhost:%d", port)

	default:
		// Best-effort roster, same philosophy as browser detection: probe PATH, first hit
		// wins, absence is ordinary.
		if bin, err := exec.LookPath("xfreerdp"); err == nil {
			args := []string{fmt.Sprintf("/v:localhost:%d", port)}
			if user != "" {
				args = append(args, "/u:"+user)
			}
			if title := rdpConnLabel(instanceName, port); title != "" {
				args = append(args, "/t:"+title)
			}
			if fullscreen {
				args = append(args, "/f")
			}
			if err := launch(exec.Command(bin, args...)); err == nil {
				return fmt.Sprintf("opened xfreerdp → localhost:%d", port)
			}
		}
		if bin, err := exec.LookPath("remmina"); err == nil {
			if err := launch(exec.Command(bin, "-c", fmt.Sprintf("rdp://localhost:%d", port))); err == nil {
				return fmt.Sprintf("opened remmina → localhost:%d", port)
			}
		}
		return manual
	}
}

// rdpFileContents is the .rdp document Windows App (and mstsc) reads to connect. Split out so
// the exact format — colon-typed keys, CRLF not required — is pinned by a test rather than
// discovered on someone's Mac.
//
// Windowed by default: with no "screen mode id" Windows App takes the whole display, which
// on a laptop means the local desktop vanishes behind the remote one every time a tunnel
// opens. A window at a laptop-friendly size, with smart sizing so dragging the window
// rescales the remote desktop instead of scrolling it, is what a tunnel to one box wants.
// Full screen is the same document with mode 2, for the people who do want that.
func rdpFileContents(port int, user string, fullscreen bool) string {
	doc := fmt.Sprintf("full address:s:localhost:%d\n", port) + "use multimon:i:0\n"
	if fullscreen {
		doc += "screen mode id:i:2\n"
	} else {
		doc += "screen mode id:i:1\n" + // 1 = windowed, 2 = full screen
			"desktopwidth:i:1600\n" +
			"desktopheight:i:1000\n" +
			"smart sizing:i:1\n"
	}
	if user != "" {
		doc += "username:s:" + user + "\n"
	}
	return doc
}

func writeRDPFile(port int, user, instanceName string, fullscreen bool) (string, error) {
	dir := filepath.Join(os.TempDir(), "warren-rdp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	// The base name is what Windows App shows as the connection label. It carries both the
	// instance name (so you know which host) and the local port (so you know which tunnel) —
	// "web-01 (localhost-13389)". No colon: macOS translates ":" to "/" in display names.
	// The port also keeps simultaneous tunnels to two hosts sharing a Name tag off one file.
	name := fmt.Sprintf("localhost-%d", port)
	if slug := rdpFileSlug(instanceName); slug != "" {
		name = fmt.Sprintf("%s (localhost-%d)", slug, port)
	}
	path := filepath.Join(dir, name+".rdp")
	if err := os.WriteFile(path, []byte(rdpFileContents(port, user, fullscreen)), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// rdpFileSlug reduces an instance name to something safe for a filename: a leading label
// Windows App will display verbatim. Anything outside a conservative set becomes a dash,
// runs collapse, and an empty result (a name that was all punctuation) yields "" so the
// caller falls back to the address.
func rdpFileSlug(s string) string {
	var b []rune
	lastDash := true // also trims leading dashes
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_':
			b = append(b, r)
			lastDash = false
		default:
			if !lastDash {
				b = append(b, '-')
				lastDash = true
			}
		}
	}
	return strings.Trim(string(b), "-")
}

// rdpConnLabel is the window/bookmark title for clients that take one as a flag (xfreerdp
// /t) — name and port together, the address alone when there is no usable name. A CLI arg,
// not a filename, so the colon is fine here.
func rdpConnLabel(instanceName string, port int) string {
	if slug := rdpFileSlug(instanceName); slug != "" {
		return fmt.Sprintf("%s (localhost:%d)", slug, port)
	}
	return fmt.Sprintf("localhost:%d", port)
}
