package tunnel

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/treyperrone/warren/internal/testenv"
)

// sleeper starts a process that stays alive until the test ends, and returns its pid.
func sleeper(t *testing.T) int {
	t.Helper()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd.exe", "/C", "ping -n 30 127.0.0.1 > NUL")
	} else {
		cmd = exec.Command("sleep", "30")
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting a helper process: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd.Process.Pid
}

// The bug this exists for: Alive() called proc.Signal(os.Signal(nil)), which os.Process.Signal
// rejects with "unsupported signal type" — a non-nil error — so it answered false for every
// running tunnel. Manager.Active then pruned each one as soon as it was asked, so no tunnel ever
// showed on the manager screen, its local port was never displayed, and the plugin process was
// left running with nothing tracking it.
func TestAliveIsTrueForARunningProcess(t *testing.T) {
	tun := &Tunnel{PID: sleeper(t)}
	if !tun.Alive() {
		t.Error("Alive() is false for a process that is definitely running")
	}
}

func TestAliveIsFalseForAProcessThatHasExited(t *testing.T) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd.exe", "/C", "exit 0")
	} else {
		cmd = exec.Command("true")
	}
	if err := cmd.Run(); err != nil { // Run waits, so it is reaped by the time we ask
		t.Fatalf("helper process: %v", err)
	}
	if runtime.GOOS == "windows" {
		t.Skip("a closed-but-openable handle can still look alive on windows; see alive_windows.go")
	}

	tun := &Tunnel{PID: cmd.Process.Pid}
	if tun.Alive() {
		t.Error("Alive() is true for a process that has exited")
	}
}

// A zero pid is the zero value, which means "never started" rather than any real process — and pid
// 0 is a real process on Unix, so probing it would answer the wrong question.
func TestAliveIsFalseForAZeroPID(t *testing.T) {
	if (&Tunnel{}).Alive() {
		t.Error("Alive() is true for a tunnel with no pid")
	}
}

// The consequence that was actually visible: Active() prunes anything not alive, so a broken
// liveness check silently emptied the manager.
func TestActiveKeepsARunningTunnel(t *testing.T) {
	testenv.SetHome(t, t.TempDir())

	m := NewManager()
	m.Add(&Tunnel{PID: sleeper(t), Kind: KindRDP, LocalPort: 13389, InstanceName: "win-01"})

	live := m.Active()
	if len(live) != 1 {
		t.Fatalf("Active() returned %d tunnels, want 1 — the manager screen would show nothing", len(live))
	}
	if live[0].LocalPort != 13389 {
		t.Errorf("local port = %d, want 13389 — this is the port the user needs told", live[0].LocalPort)
	}
}

func TestActiveDropsATunnelThatIsGone(t *testing.T) {
	testenv.SetHome(t, t.TempDir())

	m := NewManager()
	m.Add(&Tunnel{PID: 0, Kind: KindSSH, LocalPort: 2222}) // never started

	if live := m.Active(); len(live) != 0 {
		t.Errorf("Active() kept %d dead tunnels", len(live))
	}
}

// Active() rewrites the persisted list, so a broken liveness check does not just hide tunnels from
// this process — it erases them from the file the next run reads.
func TestActivePersistsSurvivingTunnels(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)

	m := NewManager()
	m.Add(&Tunnel{PID: sleeper(t), Kind: KindRDP, LocalPort: 13389, InstanceName: "win-01"})
	m.Active()

	data, err := os.ReadFile(filepath.Join(home, ".warren_sessions.json"))
	if err != nil {
		t.Fatalf("session file not written: %v", err)
	}
	if len(data) == 0 || string(data) == "null" || string(data) == "[]" {
		t.Errorf("session file records no tunnels: %q", data)
	}
}

func TestFreePortReturnsSomethingUsable(t *testing.T) {
	// 13389 is the RDP base, which is where the reported port comes from.
	got := FreePort(13389)
	if got < 13389 || got >= 13389+100 {
		t.Errorf("FreePort(13389) = %d, outside the range it searches", got)
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", got))
	if err != nil {
		t.Errorf("port %d reported free but cannot be bound: %v", got, err)
		return
	}
	_ = ln.Close()
}

// A forwarded port is only useful once you know what to point at it. The row used to read
// "PID 12345", which answers a question nobody asked.
func TestHintSaysWhatToDoWithTheTunnel(t *testing.T) {
	rdp := (&Tunnel{Kind: KindRDP, LocalPort: 13389, PID: 42}).Hint()
	for _, want := range []string{"13389", "RDP client"} {
		if !strings.Contains(rdp, want) {
			t.Errorf("RDP hint %q does not mention %q", rdp, want)
		}
	}

	ssh := (&Tunnel{Kind: KindSSH, LocalPort: 2222, SSHUser: "ec2-user", PID: 42}).Hint()
	for _, want := range []string{"ssh -p 2222", "ec2-user@localhost"} {
		if !strings.Contains(ssh, want) {
			t.Errorf("SSH hint %q does not mention %q", ssh, want)
		}
	}

	// An unknown user must not render as an empty @localhost that looks like a broken command.
	if got := (&Tunnel{Kind: KindSSH, LocalPort: 2222}).Hint(); strings.Contains(got, " @localhost") {
		t.Errorf("SSH hint with no user reads as a broken command: %q", got)
	}

	// The port is what the user needs, so it must appear even for kinds without a tailored hint.
	if got := (&Tunnel{Kind: KindShell, PID: 42}).Hint(); !strings.Contains(got, "42") {
		t.Errorf("shell hint %q loses the pid", got)
	}
}
