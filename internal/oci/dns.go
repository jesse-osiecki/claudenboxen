package oci

import "os"

// ResolvConfSource picks which host file to bind-mount as the sandbox's
// /etc/resolv.conf. On systemd-resolved hosts, /etc/resolv.conf is usually a
// symlink to a stub listening on 127.0.0.53, which is unreachable from
// inside the sandbox's own network namespace — the real upstream servers
// live in /run/systemd/resolve/resolv.conf instead. Prefer that file when
// it exists; otherwise fall back to /etc/resolv.conf directly.
func ResolvConfSource() string {
	const systemdResolved = "/run/systemd/resolve/resolv.conf"
	if _, err := os.Stat(systemdResolved); err == nil {
		return systemdResolved
	}
	return "/etc/resolv.conf"
}
