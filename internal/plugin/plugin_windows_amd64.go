//go:build windows && amd64

package plugin

import _ "embed"

//go:embed assets/session-manager-plugin-windows-amd64.exe
var pluginBinary []byte
