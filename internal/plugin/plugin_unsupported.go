//go:build !(darwin && amd64) && !(darwin && arm64) && !(linux && amd64) && !(linux && arm64) && !(windows && amd64)

package plugin

// No session-manager-plugin is vendored for this platform. Leaving it empty is deliberate:
// extract names the platform and says SSM sessions are unavailable, rather than handing over a
// binary for the wrong architecture and failing on exec — which is what the old
// `embed assets/*` did to linux/arm64.
var pluginBinary []byte
