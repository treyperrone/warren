// Package plugin supplies the AWS session-manager-plugin binary for the platform this build
// targets, so there is nothing to install alongside warren.
//
// The plugin is embedded per-platform, by build-constrained files rather than by one
// `embed assets/*`. A Go binary is compiled for a single GOOS/GOARCH regardless, so embedding
// every platform's plugin never bought portability — it just made each build carry roughly 32MB
// of code it could never execute. Each plugin_<goos>_<goarch>.go file embeds exactly one asset
// and sets pluginBinary; platforms with no asset get an empty one and a clear error.
package plugin

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

var (
	once    sync.Once
	binPath string
	binErr  error
)

// Path extracts the session-manager-plugin for the current platform into the user's cache
// directory on first call and returns the path. Subsequent calls return the cached path.
func Path() (string, error) {
	once.Do(func() {
		binPath, binErr = extract()
	})
	return binPath, binErr
}

func extract() (string, error) {
	// Empty means this platform has no plugin embedded. Everything that does not open an SSM
	// session still works, so this has to read as one unavailable feature rather than a broken
	// install. Previously linux/arm64 silently received the amd64 plugin and failed on exec.
	if len(pluginBinary) == 0 {
		return "", fmt.Errorf(
			"no session-manager-plugin is embedded for %s/%s, so SSM sessions are unavailable in this build",
			runtime.GOOS, runtime.GOARCH)
	}

	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}

	// Not os.TempDir(): /tmp is world-writable and the old filename was fixed, so any local
	// user could pre-create a binary of the right size at that path. The previous check
	// compared only the size before reusing what it found, so warren would have executed
	// that file — with live AWS credentials in its environment. The cache directory is the
	// user's own and created 0700, and the name carries a digest of the bytes so a stale or
	// different build cannot be mistaken for this one.
	dir, err := cacheDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(pluginBinary)
	dest := filepath.Join(dir, fmt.Sprintf("session-manager-plugin-%x%s", sum[:8], ext))

	// Reuse only bytes that are exactly ours. Size is not identity.
	if existing, err := os.ReadFile(dest); err == nil && bytes.Equal(existing, pluginBinary) {
		return dest, nil
	}

	// Write to a unique name in the same directory and rename into place, so a second warren
	// starting concurrently can never exec a half-written plugin.
	tmp, err := os.CreateTemp(dir, "plugin-*")
	if err != nil {
		return "", fmt.Errorf("creating plugin temp file: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op once the rename succeeds
	if err := tmp.Chmod(0o700); err != nil {
		tmp.Close()
		return "", fmt.Errorf("setting plugin permissions: %w", err)
	}
	if _, err := tmp.Write(pluginBinary); err != nil {
		tmp.Close()
		return "", fmt.Errorf("writing plugin: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("writing plugin: %w", err)
	}

	if err := os.Rename(tmp.Name(), dest); err != nil {
		// Windows refuses to replace a file that is currently executing. Another warren
		// having already put the right bytes there is success, not failure.
		if existing, rerr := os.ReadFile(dest); rerr == nil && bytes.Equal(existing, pluginBinary) {
			return dest, nil
		}
		return "", fmt.Errorf("installing plugin to %s: %w", dest, err)
	}
	return dest, nil
}

// cacheDir returns a private per-user directory for the extracted plugin, creating it 0700.
func cacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locating a cache directory for the plugin: %w", err)
	}
	dir := filepath.Join(base, "warren")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}
	return dir, nil
}
