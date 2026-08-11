//go:build darwin && arm64

package plugin

import _ "embed"

//go:embed assets/session-manager-plugin-darwin-arm64
var pluginBinary []byte
