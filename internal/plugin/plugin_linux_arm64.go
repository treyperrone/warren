//go:build linux && arm64

package plugin

import _ "embed"

//go:embed assets/session-manager-plugin-linux-arm64
var pluginBinary []byte
