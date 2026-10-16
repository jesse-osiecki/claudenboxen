package oci

import (
	"os"
	"path/filepath"
)

// CABundleSource returns the host's CA bundle as a resolved file path, or
// "" if none is found. Distros keep the bundle behind symlinks into
// directories the sandbox doesn't mount (Arch: /etc/ca-certificates/
// extracted, Fedora: /etc/pki/ca-trust/extracted), so bind-mounting
// /etc/ssl/certs as a whole leaves a dangling link in the guest and TLS
// fails for curl, git, and anything else without its own trust store. The
// resolved file is bind-mounted over the guest's own bundle instead, which
// also carries any CAs added on the host.
//
// Candidates, in order: the Debian/Arch/Alpine path, the Fedora/RHEL path,
// and OpenSSL's default.
func CABundleSource() string {
	for _, p := range []string{
		"/etc/ssl/certs/ca-certificates.crt",
		"/etc/pki/tls/certs/ca-bundle.crt",
		"/etc/ssl/cert.pem",
	} {
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil {
			continue
		}
		if fi, err := os.Stat(resolved); err == nil && fi.Mode().IsRegular() {
			return resolved
		}
	}
	return ""
}
