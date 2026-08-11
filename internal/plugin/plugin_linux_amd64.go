//go:build linux && amd64

package plugin

import _ "embed"

//go:embed assets/session-manager-plugin-linux-amd64
var pluginBinary []byte
