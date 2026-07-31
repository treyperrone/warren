package tunnel

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/treyp/ssm-tool/internal/plugin"
)

type Kind string

const (
	KindRDP   Kind = "RDP"
	KindSSH   Kind = "SSH"
	KindShell Kind = "Shell"
)

// Tunnel represents an active SSM connection.
type Tunnel struct {
	PID          int
	Kind         Kind
	InstanceID   string
	InstanceName string
	LocalPort    int
	AuthLabel    string
	cmd          *exec.Cmd
}

func (t *Tunnel) Label() string {
	if t.Kind == KindShell {
		return fmt.Sprintf("[%-5s] %-30s %s", t.Kind, t.InstanceName, t.AuthLabel)
	}
	return fmt.Sprintf("[%-5s] %-30s localhost:%-6d %s", t.Kind, t.InstanceName, t.LocalPort, t.AuthLabel)
}

func (t *Tunnel) Alive() bool {
	if t.PID == 0 {
		return false
	}
	proc, err := os.FindProcess(t.PID)
	if err != nil {
		return false
	}
	return proc.Signal(os.Signal(nil)) == nil
}

func (t *Tunnel) Kill() error {
	if t.cmd != nil && t.cmd.Process != nil {
		return t.cmd.Process.Kill()
	}
	proc, err := os.FindProcess(t.PID)
	if err != nil {
		return err
	}
	return proc.Kill()
}

// Manager tracks all active tunnels and persists them to a state file.
type Manager struct {
	mu      sync.Mutex
	tunnels []*Tunnel
	file    string
}

type persistEntry struct {
	PID          int    `json:"pid"`
	Kind         string `json:"kind"`
	InstanceID   string `json:"instance_id"`
	InstanceName string `json:"instance_name"`
	LocalPort    int    `json:"local_port"`
	AuthLabel    string `json:"auth_label"`
}

func NewManager() *Manager {
	f := filepath.Join(os.Getenv("HOME"), ".ssm_sessions.json")
	m := &Manager{file: f}
	m.load()
	return m
}

func (m *Manager) load() {
	data, err := os.ReadFile(m.file)
	if err != nil {
		return
	}
	var entries []persistEntry
	if json.Unmarshal(data, &entries) != nil {
		return
	}
	for _, e := range entries {
		proc, err := os.FindProcess(e.PID)
		if err != nil || proc.Signal(os.Signal(nil)) != nil {
			continue
		}
		m.tunnels = append(m.tunnels, &Tunnel{
			PID:          e.PID,
			Kind:         Kind(e.Kind),
			InstanceID:   e.InstanceID,
			InstanceName: e.InstanceName,
			LocalPort:    e.LocalPort,
			AuthLabel:    e.AuthLabel,
		})
	}
}

func (m *Manager) save() {
	var entries []persistEntry
	for _, t := range m.tunnels {
		if t.Alive() {
			entries = append(entries, persistEntry{
				PID:          t.PID,
				Kind:         string(t.Kind),
				InstanceID:   t.InstanceID,
				InstanceName: t.InstanceName,
				LocalPort:    t.LocalPort,
				AuthLabel:    t.AuthLabel,
			})
		}
	}
	data, _ := json.MarshalIndent(entries, "", "  ")
	_ = os.WriteFile(m.file, data, 0600)
}

func (m *Manager) Add(t *Tunnel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tunnels = append(m.tunnels, t)
	m.save()
}

func (m *Manager) Remove(t *Tunnel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var kept []*Tunnel
	for _, existing := range m.tunnels {
		if existing != t {
			kept = append(kept, existing)
		}
	}
	m.tunnels = kept
	m.save()
}

// Active returns only live tunnels, pruning dead ones.
func (m *Manager) Active() []*Tunnel {
	m.mu.Lock()
	defer m.mu.Unlock()
	var live []*Tunnel
	for _, t := range m.tunnels {
		if t.Alive() {
			live = append(live, t)
		}
	}
	m.tunnels = live
	m.save()
	return live
}

// FreePort finds the lowest available port at or above base.
func FreePort(base int) int {
	for port := base; port < base+100; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			ln.Close()
			return port
		}
	}
	return base
}

// WaitPort polls until the port accepts connections or timeout.
func WaitPort(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("port %d never opened after %s", port, timeout)
}

// ssmRequest is the JSON payload the session-manager-plugin expects on stdin.
type ssmRequest struct {
	SessionID  string `json:"SessionId"`
	TokenValue string `json:"TokenValue"`
	StreamURL  string `json:"StreamUrl"`
}

// sessionEnv is the minimal interface we need from aws.Session.
type sessionEnv interface {
	Env() []string
}

// StartPortForward launches an SSM port-forwarding session via the embedded plugin.
func StartPortForward(instanceID string, remotePort, localPort int, sess sessionEnv) (*Tunnel, error) {
	pluginBin, err := plugin.Path()
	if err != nil {
		return nil, fmt.Errorf("plugin: %w", err)
	}

	params := fmt.Sprintf(`{"portNumber":["%d"],"localPortNumber":["%d"]}`, remotePort, localPort)

	// The plugin is invoked the same way the aws CLI invokes it:
	// session-manager-plugin <response-json> <region> StartSession <profile> <request-json> <endpoint>
	// We pass the SSM StartSession API call result as the response JSON.
	// Since we're calling the API directly via SDK, we build the args the plugin expects.
	cmd := exec.Command(pluginBin,
		"", // response JSON — populated by caller via SDK (see StartPortForwardWithToken)
		"us-east-1",
		"StartSession",
		"",
		fmt.Sprintf(`{"Target":"%s","DocumentName":"AWS-StartPortForwardingSession","Parameters":%s}`, instanceID, params),
		"https://ssm.us-east-1.amazonaws.com",
	)
	cmd.Env = append(os.Environ(), sess.Env()...)
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start plugin: %w", err)
	}
	return &Tunnel{
		PID: cmd.Process.Pid,
		cmd: cmd,
	}, nil
}

// ShellCmd returns an exec.Cmd for an interactive SSM shell session.
// The caller should use tea.ExecProcess to run it in the foreground.
func ShellCmd(instanceID string, sess sessionEnv) (*exec.Cmd, error) {
	pluginBin, err := plugin.Path()
	if err != nil {
		return nil, fmt.Errorf("plugin: %w", err)
	}

	cmd := exec.Command(pluginBin,
		"",
		"us-east-1",
		"StartSession",
		"",
		fmt.Sprintf(`{"Target":"%s"}`, instanceID),
		"https://ssm.us-east-1.amazonaws.com",
	)
	cmd.Env = append(os.Environ(), sess.Env()...)
	return cmd, nil
}
