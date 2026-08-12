// Package buildinfo reports the version of the running binary.
package buildinfo

import "runtime/debug"

// version is stamped at release time by goreleaser via -ldflags -X. It is
// deliberately empty by default rather than something like "dev": the common
// install path is `go install github.com/treyperrone/warren@v0.1.3`, which
// applies no ldflags at all, and a non-empty default would shadow the real
// module version that the Go toolchain embeds for exactly that case.
var version string

// Version returns the binary's version, preferring the release stamp and
// falling back to the module version or VCS revision recorded in the embedded
// build info. It always returns a non-empty string.
func Version() string {
	if version != "" {
		return version
	}

	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}

	// Set for `go install module@version`. Local `go build` reports "(devel)"
	// unless the checkout sits exactly on a tag, so fall through to the commit.
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v
	}

	var revision string
	var modified bool
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if revision == "" {
		return "devel"
	}
	short := revision
	if len(short) > 12 {
		short = short[:12]
	}
	if modified {
		short += "-dirty"
	}
	return "devel-" + short
}
