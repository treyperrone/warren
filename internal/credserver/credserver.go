// Package credserver serves the selected role's credentials to child processes over loopback.
//
// Why this exists: injecting AWS_ACCESS_KEY_ID into a shell hands it a frozen copy, and a parent
// cannot change a child's environment afterwards — so a shell opened from warren stopped working
// at the one-hour mark and there was nothing warren could do about it.
//
// Every AWS SDK, and the aws CLI, can instead fetch credentials from an HTTP endpoint named by
// AWS_CONTAINER_CREDENTIALS_FULL_URI. That inverts the problem: the child asks on each call, so it
// always gets whatever warren holds right now. Verified against aws-cli v2, which reports the
// source as "container-role".
package credserver

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	awsint "github.com/treyperrone/warren/internal/aws"
)

// credPath is where credentials are served. The bearer token is the actual control; the path is
// only here so the endpoint is not the listener's root.
const credPath = "/credentials"

// Server is a loopback HTTP endpoint serving whatever credentials it was last given.
type Server struct {
	mu   sync.RWMutex
	sess *awsint.Session

	token string
	ln    net.Listener
	srv   *http.Server
}

// Start binds a loopback listener on an arbitrary port and begins serving.
//
// Loopback only, never a routable address: the endpoint hands out working credentials to anything
// that can reach it and present the token. A random bearer token is required on every request,
// because on a shared machine any local process could otherwise read them.
func Start() (*Server, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("generating endpoint token: %w", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listening on loopback: %w", err)
	}

	s := &Server{
		token: base64.RawURLEncoding.EncodeToString(raw),
		ln:    ln,
	}

	mux := http.NewServeMux()
	mux.HandleFunc(credPath, s.handle)
	s.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

// credentialResponse is the shape the SDKs' container-credential provider expects.
type credentialResponse struct {
	AccessKeyID     string `json:"AccessKeyId"`
	SecretAccessKey string `json:"SecretAccessKey"`
	Token           string `json:"Token"`
	// Expiration is omitted when unknown — a profile backed by long-lived keys has none, and
	// sending a zero time would read as "expired in 1970".
	Expiration string `json:"Expiration,omitempty"`
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Constant-time, so a wrong token cannot be discovered a byte at a time.
	given := r.Header.Get("Authorization")
	if subtle.ConstantTimeCompare([]byte(given), []byte(s.token)) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	s.mu.RLock()
	sess := s.sess
	s.mu.RUnlock()

	if sess == nil || sess.AccessKeyID == "" {
		// Nothing selected yet, or a profile session with no materialised keys. 503 rather than
		// an empty body so the SDK reports a retryable failure instead of malformed credentials.
		http.Error(w, "no credentials available", http.StatusServiceUnavailable)
		return
	}

	out := credentialResponse{
		AccessKeyID:     sess.AccessKeyID,
		SecretAccessKey: sess.SecretAccessKey,
		Token:           sess.SessionToken,
	}
	if !sess.Expires.IsZero() {
		out.Expiration = sess.Expires.UTC().Format(time.RFC3339)
	}

	body, err := json.Marshal(out)
	if err != nil {
		http.Error(w, "encoding credentials", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// Never cached: the point of the endpoint is that each call sees current credentials.
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

// Set replaces the credentials served from now on. Safe to call from any goroutine.
func (s *Server) Set(sess *awsint.Session) {
	s.mu.Lock()
	s.sess = sess
	s.mu.Unlock()
}

// Session returns the credentials currently being served, or nil.
func (s *Server) Session() *awsint.Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sess
}

// URI is the endpoint address, for display.
func (s *Server) URI() string {
	return "http://" + s.ln.Addr().String() + credPath
}

// Env is the pair of variables a child needs to use the endpoint.
//
// Deliberately does not include AWS_ACCESS_KEY_ID and friends: the SDK's environment provider is
// consulted before the container provider, so a static copy alongside this would win and freeze
// the child's credentials — the exact problem the endpoint exists to solve. Callers pair this with
// Session.RegionEnv, not Session.Env.
func (s *Server) Env() []string {
	return []string{
		"AWS_CONTAINER_CREDENTIALS_FULL_URI=" + s.URI(),
		"AWS_CONTAINER_AUTHORIZATION_TOKEN=" + s.token,
	}
}

// Close stops serving.
func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return s.srv.Shutdown(ctx)
}

// KeepFresh renews credentials on its own timer and serves each result.
//
// This runs outside any UI event loop on purpose. While a child process holds the terminal, the
// bubbletea program is blocked and its own renewal tick cannot fire — which is precisely the
// window where a long-lived shell would otherwise cross its expiry. Returns when ctx is done.
func (s *Server) KeepFresh(ctx context.Context, every, lead time.Duration, renew func(context.Context) (*awsint.Session, error)) {
	t := time.NewTicker(every)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cur := s.Session()
			// Nothing to renew, or credentials that do not expire.
			if cur == nil || cur.Expires.IsZero() {
				continue
			}
			if time.Until(cur.Expires) > lead {
				continue
			}
			// A failure is not fatal: the old credentials may still have minutes on them, and
			// the next tick tries again.
			if fresh, err := renew(ctx); err == nil && fresh != nil {
				s.Set(fresh)
			}
		}
	}
}
