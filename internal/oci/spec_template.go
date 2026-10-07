// Package oci builds OCI runtime-spec bundles (config.json) for the gVisor
// backend. internal/runtime invokes runsc against them.
package oci

import specs "github.com/opencontainers/runtime-spec/specs-go"

const ociVersion = "1.0.0"

// defaultMounts mirrors what `runsc spec` generates (gVisor implements
// proc/dev/sys itself, so no devpts/mqueue/cgroup mounts are needed),
// plus a tmpfs at /tmp. /tmp is a dedicated mount so it's always 1777 and
// ephemeral, independent of how the image's own /tmp was extracted.
func defaultMounts() []specs.Mount {
	return []specs.Mount{
		{Destination: "/proc", Type: "proc", Source: "proc"},
		{Destination: "/dev", Type: "tmpfs", Source: "tmpfs"},
		{
			Destination: "/sys",
			Type:        "sysfs",
			Source:      "sysfs",
			Options:     []string{"nosuid", "noexec", "nodev", "ro"},
		},
		{
			Destination: "/tmp",
			Type:        "tmpfs",
			Source:      "tmpfs",
			Options:     []string{"nosuid", "nodev", "mode=1777"},
		},
	}
}

// defaultNamespaces isolates pid/ipc/uts/mount, plus network unless
// hostNetwork is set. No user namespace. Host networking means not
// unsharing the network namespace at all; listing an empty network
// namespace entry would give the container a fresh, routeless netns even
// under runsc --network=host.
func defaultNamespaces(hostNetwork bool) []specs.LinuxNamespace {
	types := []specs.LinuxNamespaceType{
		specs.PIDNamespace,
		specs.IPCNamespace,
		specs.UTSNamespace,
		specs.MountNamespace,
	}
	if !hostNetwork {
		types = append(types, specs.NetworkNamespace)
	}
	ns := make([]specs.LinuxNamespace, len(types))
	for i, t := range types {
		ns[i] = specs.LinuxNamespace{Type: t}
	}
	return ns
}
