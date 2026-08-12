package tunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/treyperrone/warren/internal/homedir"
	"github.com/treyperrone/warren/internal/plugin"
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
	SSHUser      string
	cmd          *exec.Cmd
}

func (t *Tunnel) Label() string {
	switch t.Kind {
	case KindShell:
		return fmt.Sprintf("[%-5s] %-30s %s", t.Kind, t.InstanceName, t.AuthLabel)
	case KindSSH:
		user := t.SSHUser
		if user == "" {
			user = "?"
		}
		return fmt.Sprintf("[%-5s] %-30s localhost:%-6d  ssh -p %d %s@localhost", t.Kind, t.InstanceName, t.LocalPort, t.LocalPort, user)
	default:
		return fmt.Sprintf("[%-5s] %-30s localhost:%-6d %s", t.Kind, t.InstanceName, t.LocalPort, t.AuthLabel)
	}
}

// Hint is the second line of a manager row: what to do with this tunnel now it is up.
//
// The row used to read "PID 12345", which answers a question nobody has. A forwarded port is only
// useful once you know what to point at it, and for RDP in particular "localhost:13389" in the
// title is not obviously the thing to paste into a client. The pid stays for killing something by
// hand if it ever comes to that.
func (t *Tunnel) Hint() string {
	switch t.Kind {
	case KindRDP:
		return fmt.Sprintf("point your RDP client at localhost:%d  •  PID %d", t.LocalPort, t.PID)
	case KindSSH:
		user := t.SSHUser
		if user == "" {
			user = "<user>"
		}
		return fmt.Sprintf("ssh -p %d %s@localhost  •  PID %d", t.LocalPort, user, t.PID)
	default:
		return fmt.Sprintf("PID %d", t.PID)
	}
}

func (t *Tunnel) Alive() bool {
	if t.PID == 0 {
		return false
	}
	return aliveByPID(t.PID)
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
	SSHUser      string `json:"ssh_user,omitempty"`
}

func NewManager() *Manager {
	f := filepath.Join(homedir.Dir(), ".warren_sessions.json")
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
			SSHUser:      e.SSHUser,
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
				SSHUser:      t.SSHUser,
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

// sessionCreds is the minimal interface we need from aws.Session.
type sessionCreds interface {
	Env() []string
	Creds() (accessKey, secretKey, sessionToken, region string)
}

// pluginResponse is the JSON the session-manager-plugin expects as its first argument.
type pluginResponse struct {
	SessionID  string `json:"SessionId"`
	TokenValue string `json:"TokenValue"`
	StreamURL  string `json:"StreamUrl"`
}

// startSSMSession calls the SSM StartSession API and returns the plugin response
// JSON and the endpoint URL needed to invoke the plugin.
func startSSMSession(ctx context.Context, instanceID, documentName string, params map[string][]string, sess sessionCreds) (string, string, error) {
	ak, sk, st, region := sess.Creds()
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(ak, sk, st)),
	)
	if err != nil {
		return "", "", err
	}
	client := ssm.NewFromConfig(cfg)

	input := &ssm.StartSessionInput{
		Target: aws.String(instanceID),
	}
	if documentName != "" {
		input.DocumentName = aws.String(documentName)
	}
	if len(params) > 0 {
		input.Parameters = params
	}

	out, err := client.StartSession(ctx, input)
	if err != nil {
		return "", "", fmt.Errorf("StartSession: %w", err)
	}

	resp := pluginResponse{
		SessionID:  aws.ToString(out.SessionId),
		TokenValue: aws.ToString(out.TokenValue),
		StreamURL:  aws.ToString(out.StreamUrl),
	}
	respJSON, err := json.Marshal(resp)
	if err != nil {
		return "", "", err
	}

	endpoint := fmt.Sprintf("https://ssm.%s.amazonaws.com", region)
	return string(respJSON), endpoint, nil
}

// ShellCmd calls SSM StartSession then returns an exec.Cmd ready to run the
// plugin interactively. The caller should use tea.ExecProcess.
func ShellCmd(ctx context.Context, instanceID string, sess sessionCreds) (*exec.Cmd, error) {
	pluginBin, err := plugin.Path()
	if err != nil {
		return nil, fmt.Errorf("plugin: %w", err)
	}

	respJSON, endpoint, err := startSSMSession(ctx, instanceID, "", nil, sess)
	if err != nil {
		return nil, err
	}

	_, _, _, region := sess.Creds()
	cmd := exec.Command(pluginBin,
		respJSON,
		region,
		"StartSession",
		"",
		fmt.Sprintf(`{"Target":"%s"}`, instanceID),
		endpoint,
	)
	cmd.Env = append(os.Environ(), sess.Env()...)
	return cmd, nil
}

// StartPortForward calls SSM StartSession then launches the plugin in the
// background for port forwarding.
func StartPortForward(ctx context.Context, instanceID string, remotePort, localPort int, sess sessionCreds) (*Tunnel, error) {
	pluginBin, err := plugin.Path()
	if err != nil {
		return nil, fmt.Errorf("plugin: %w", err)
	}

	params := map[string][]string{
		"portNumber":      {fmt.Sprintf("%d", remotePort)},
		"localPortNumber": {fmt.Sprintf("%d", localPort)},
	}
	respJSON, endpoint, err := startSSMSession(ctx, instanceID, "AWS-StartPortForwardingSession", params, sess)
	if err != nil {
		return nil, err
	}

	_, _, _, region := sess.Creds()
	reqJSON := fmt.Sprintf(
		`{"Target":"%s","DocumentName":"AWS-StartPortForwardingSession","Parameters":{"portNumber":["%d"],"localPortNumber":["%d"]}}`,
		instanceID, remotePort, localPort,
	)

	cmd := exec.Command(pluginBin,
		respJSON,
		region,
		"StartSession",
		"",
		reqJSON,
		endpoint,
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
