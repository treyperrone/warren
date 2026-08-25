package tunnel

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

func OpenRDPClient(port int, user string) string {
	manual := fmt.Sprintf("point your RDP client at localhost:%d", port)
	switch runtime.GOOS {
	case "windows":
		// mstsc ships with every Windows edition; /v is the documented connect-directly flag.
		if err := launch(exec.Command("mstsc.exe", fmt.Sprintf("/v:localhost:%d", port))); err != nil {
			return manual + fmt.Sprintf(" (mstsc failed to start: %v)", err)
		}
		return fmt.Sprintf("opened mstsc → localhost:%d", port)

	case "darwin":
		// Windows App (né Microsoft Remote Desktop) opens .rdp documents; a temp file plus
		// open(1) survives the app's renames and URL-scheme changes across versions, which
		// an rdp:// URL has not. The file carries no secret — an address and a username —
		// and lands in a 0700 warren dir all the same.
		path, err := writeRDPFile(port, user)
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

// rdpFileContents is the two-key .rdp document Windows App needs to connect. Split out so
// the exact format — colon-typed keys, CRLF not required — is pinned by a test rather than
// discovered on someone's Mac.
func rdpFileContents(port int, user string) string {
	doc := fmt.Sprintf("full address:s:localhost:%d\n", port)
	if user != "" {
		doc += "username:s:" + user + "\n"
	}
	return doc
}

func writeRDPFile(port int, user string) (string, error) {
	dir := filepath.Join(os.TempDir(), "warren-rdp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("localhost-%d.rdp", port))
	if err := os.WriteFile(path, []byte(rdpFileContents(port, user)), 0o600); err != nil {
		return "", err
	}
	return path, nil
}
