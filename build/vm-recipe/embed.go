// Package vmrecipe embeds the VM image's finishing build script and the
// guest init script (the VM's PID 1).
package vmrecipe

import _ "embed"

//go:embed build.sh
var BuildScript string

//go:embed init.sh
var InitScript string
