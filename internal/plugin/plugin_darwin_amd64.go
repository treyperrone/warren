//go:build darwin && amd64

package plugin

import _ "embed"

//go:embed assets/session-manager-plugin-darwin-amd64
var pluginBinary []byte
