package plugin

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// privateCache points UserCacheDir at a temp directory so extract() can be exercised without
// touching the real one. extract() is called directly rather than Path(), whose sync.Once would
// cache the first result for the whole test binary.
func privateCache(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	switch runtime.GOOS {
	case "darwin":
		t.Setenv("HOME", dir)
		return filepath.Join(dir, "Library", "Caches", "warren")
	case "windows":
		t.Setenv("LocalAppData", dir)
	default:
		t.Setenv("XDG_CACHE_HOME", dir)
	}
	return filepath.Join(dir, "warren")
}

func TestExtractWritesOutsideTheSharedTempDir(t *testing.T) {
	if len(pluginBinary) == 0 {
		t.Skip("no plugin embedded for this platform")
	}
	want := privateCache(t)

	got, err := extract()
	if err != nil {
		t.Fatalf("extract: %v", err)
	}

	// The old location was os.TempDir() with the fixed name "warren-plugin", which any local
	// user could pre-create. Two properties replace that: the file lives in the user's own
	// cache directory (created 0700, checked below), and its name carries a digest of the
	// bytes, so it is not a name an attacker can predict and squat ahead of first run.
	//
	// Note this cannot assert "not under os.TempDir()" — the fake cache directory above is
	// itself a t.TempDir(), which lives there.
	if filepath.Dir(got) != want {
		t.Errorf("plugin extracted to %q, want it under %q", got, want)
	}
	sum := sha256.Sum256(pluginBinary)
	if !strings.Contains(filepath.Base(got), fmt.Sprintf("%x", sum[:8])) {
		t.Errorf("filename %q does not carry the content digest, so it is predictable", filepath.Base(got))
	}

	info, err := os.Stat(filepath.Dir(got))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Errorf("cache directory mode is %v, want 0700 so no other user can write into it", info.Mode().Perm())
	}
}

// The heart of the fix. The old check compared only os.Stat size before reusing what it found, so
// a same-sized file planted at the predictable path was executed with live AWS credentials in its
// environment.
func TestExtractRefusesToTrustAnImpostorOfTheRightSize(t *testing.T) {
	if len(pluginBinary) == 0 {
		t.Skip("no plugin embedded for this platform")
	}
	privateCache(t)

	dest, err := extract()
	if err != nil {
		t.Fatalf("extract: %v", err)
	}

	impostor := bytes.Repeat([]byte{'X'}, len(pluginBinary)) // byte-for-byte the same length
	if err := os.WriteFile(dest, impostor, 0o700); err != nil {
		t.Fatal(err)
	}

	again, err := extract()
	if err != nil {
		t.Fatalf("extract after tampering: %v", err)
	}
	on, err := os.ReadFile(again)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(on, impostor) {
		t.Fatal("reused a same-sized impostor: warren would execute it with live credentials")
	}
	if !bytes.Equal(on, pluginBinary) {
		t.Error("file at the extraction path is not the embedded plugin")
	}
}

func TestExtractIsIdempotentAndExecutable(t *testing.T) {
	if len(pluginBinary) == 0 {
		t.Skip("no plugin embedded for this platform")
	}
	privateCache(t)

	first, err := extract()
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	second, err := extract()
	if err != nil {
		t.Fatalf("second extract: %v", err)
	}
	if first != second {
		t.Errorf("extract returned %q then %q; the path should be stable", first, second)
	}

	info, err := os.Stat(first)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Errorf("plugin mode is %v, want 0700 (owner-only, executable)", info.Mode().Perm())
	}

	// No leftover temp files: the atomic write renames into place and cleans up on failure.
	entries, err := os.ReadDir(filepath.Dir(first))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "plugin-") {
			t.Errorf("left a temp file behind: %s", e.Name())
		}
	}
}
