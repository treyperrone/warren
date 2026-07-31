package plugin

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

//go:embed assets/*
var assets embed.FS

var (
	once    sync.Once
	binPath string
	binErr  error
)

// Path extracts the session-manager-plugin for the current platform to a temp
// file on first call and returns the path. Subsequent calls return the cached path.
func Path() (string, error) {
	once.Do(func() {
		binPath, binErr = extract()
	})
	return binPath, binErr
}

func assetName() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		switch runtime.GOARCH {
		case "arm64":
			return "assets/session-manager-plugin-darwin-arm64", nil
		default:
			return "assets/session-manager-plugin-darwin-amd64", nil
		}
	case "linux":
		return "assets/session-manager-plugin-linux-amd64", nil
	case "windows":
		return "assets/session-manager-plugin-windows-amd64.exe", nil
	default:
		return "", fmt.Errorf("unsupported platform: %s/%s", runtime.GOOS, runtime.GOARCH)
	}
}

func extract() (string, error) {
	name, err := assetName()
	if err != nil {
		return "", err
	}

	data, err := assets.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("reading embedded plugin: %w", err)
	}

	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	dest := filepath.Join(os.TempDir(), "ssm-tool-plugin"+ext)

	// skip re-write if already there and same size
	if info, err := os.Stat(dest); err == nil && info.Size() == int64(len(data)) {
		return dest, nil
	}

	if err := os.WriteFile(dest, data, 0700); err != nil {
		return "", fmt.Errorf("writing plugin to %s: %w", dest, err)
	}
	return dest, nil
}
