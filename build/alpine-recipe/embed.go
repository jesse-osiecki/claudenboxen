// Package recipe embeds the shell script that provisions the base image.
package recipe

import _ "embed"

//go:embed build.sh
var BuildScript string
