package plugin

import (
	_ "embed"
	"strings"
)

// version.txt is written by scripts/build-plugin.sh and holds the aws/session-manager-plugin
// release tag the embedded binaries were built from. It is a file rather than a constant so the
// script has exactly one thing to update alongside the binaries, and so the two cannot drift.
//
//go:embed version.txt
var versionTxt string

// Version is the aws/session-manager-plugin release tag the embedded plugin was built from.
//
// This is the tag, not what the plugin reports about itself. AWS does not keep its in-tree version
// constant aligned with its release tags: at tag 1.2.835.0 the source reads 1.3.0.0, so a build of
// that tag answers `--version` with 1.3.0.0 while AWS's own 1.2.835.0 build answers 1.2.835.0. The
// tag is what identifies the source, so the tag is what is recorded — see scripts/build-plugin.sh
// for why the source is not patched to agree.
func Version() string { return strings.TrimSpace(versionTxt) }
