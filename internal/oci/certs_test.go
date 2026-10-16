package oci

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCABundleSource(t *testing.T) {
	p := CABundleSource()
	if p == "" {
		t.Skip("no CA bundle on this host")
	}
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("CABundleSource() = %q, not a regular file (err=%v)", p, err)
	}
	if resolved, _ := filepath.EvalSymlinks(p); resolved != p {
		t.Errorf("CABundleSource() = %q, symlinks not resolved (%q)", p, resolved)
	}
}
