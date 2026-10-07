package vmspec

import (
	"fmt"
	"os"
	"strings"
)

// parseMountSpec parses "SRC[:DST[:ro|rw]]" — the same shape
// internal/oci.ParseMountSpec accepts for the gVisor backend's --mount
// flag, reimplemented locally rather than shared because the two callers
// want different result types (an OCI specs.Mount there, a plain
// host/guest/readonly tuple here for virtiofsd) and the parsing itself is
// small enough that sharing isn't worth the cross-package coupling.
func parseMountSpec(s string) (src, dst string, readOnly bool, err error) {
	parts := strings.Split(s, ":")
	mode := "rw"
	switch len(parts) {
	case 1:
		src, dst = parts[0], parts[0]
	case 2:
		src, dst = parts[0], parts[1]
	case 3:
		src, dst, mode = parts[0], parts[1], parts[2]
	default:
		return "", "", false, fmt.Errorf("invalid mount spec %q: expected SRC[:DST[:ro|rw]]", s)
	}
	if mode != "ro" && mode != "rw" {
		return "", "", false, fmt.Errorf("mount %q: mode must be ro or rw, got %q", s, mode)
	}
	if _, err := os.Stat(src); err != nil {
		return "", "", false, fmt.Errorf("mount %q: %w", s, err)
	}
	return src, dst, mode == "ro", nil
}
