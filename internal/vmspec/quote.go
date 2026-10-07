package vmspec

import "strings"

// shellQuote wraps s in single quotes, escaping any embedded single quote
// as '\” (close quote, escaped literal quote, reopen quote) — the
// standard POSIX-shell-safe quoting trick, used so the "env" control file
// can be `.`-sourced by build/vm-recipe/init.sh without any value's
// special characters being reinterpreted by the shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
