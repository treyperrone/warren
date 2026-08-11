package plugin

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

// platforms the release matrix builds and for which an asset is vendored.
func hasAsset() bool {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64", "windows/amd64":
		return true
	}
	return false
}

// Guards a renamed, moved, or missing asset. With per-platform embedding a wrong filename is a
// compile error, but an asset that is present and empty would not be — and would surface only as
// a failed session at runtime.
func TestPluginIsEmbeddedForThisPlatform(t *testing.T) {
	if !hasAsset() {
		if len(pluginBinary) != 0 {
			t.Errorf("%s/%s has no vendored asset but embedded %d bytes",
				runtime.GOOS, runtime.GOARCH, len(pluginBinary))
		}
		t.Skipf("no plugin vendored for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	if len(pluginBinary) == 0 {
		t.Fatalf("no plugin embedded for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	// The real plugin is ~10MB. A truncated or placeholder asset would still be non-empty.
	if len(pluginBinary) < 1<<20 {
		t.Errorf("embedded plugin is only %d bytes; expected megabytes", len(pluginBinary))
	}
}

// The whole point of embedding is that a session needs nothing installed, so extraction has to
// produce a runnable file of exactly the right size.
func TestPathExtractsAnExecutable(t *testing.T) {
	if !hasAsset() {
		t.Skipf("no plugin vendored for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	path, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.Size() != int64(len(pluginBinary)) {
		t.Errorf("extracted %d bytes, embedded %d", info.Size(), len(pluginBinary))
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o100 == 0 {
		t.Errorf("extracted plugin is not executable (mode %v)", info.Mode().Perm())
	}

	// Cached on the second call rather than rewritten.
	again, err := Path()
	if err != nil || again != path {
		t.Errorf("second Path() = %q, %v; want the cached %q", again, err, path)
	}
}

// A platform with no plugin must fail with something that names the platform and says what is
// unavailable, not with a mystery exec error.
func TestExtractErrorNamesThePlatform(t *testing.T) {
	if hasAsset() {
		t.Skip("this platform has a plugin; the empty path is covered on platforms without one")
	}
	_, err := extract()
	if err == nil {
		t.Fatal("expected an error on a platform with no plugin")
	}
	for _, want := range []string{runtime.GOOS, runtime.GOARCH, "SSM sessions"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}
