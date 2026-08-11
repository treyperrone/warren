// Package plugin supplies the AWS session-manager-plugin binary for the platform this build
// targets, so there is nothing to install alongside postern.
//
// The plugin is embedded per-platform, by build-constrained files rather than by one
// `embed assets/*`. A Go binary is compiled for a single GOOS/GOARCH regardless, so embedding
// every platform's plugin never bought portability — it just made each build carry roughly 32MB
// of code it could never execute. Each plugin_<goos>_<goarch>.go file embeds exactly one asset
// and sets pluginBinary; platforms with no asset get an empty one and a clear error.
package plugin

import (
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

// Path extracts the session-manager-plugin for the current platform to a temp file on first
// call and returns the path. Subsequent calls return the cached path.
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
	dest := filepath.Join(os.TempDir(), "postern-plugin"+ext)

	// skip re-write if already there and same size
	if info, err := os.Stat(dest); err == nil && info.Size() == int64(len(pluginBinary)) {
		return dest, nil
	}

	if err := os.WriteFile(dest, pluginBinary, 0700); err != nil {
		return "", fmt.Errorf("writing plugin to %s: %w", dest, err)
	}
	return dest, nil
}
