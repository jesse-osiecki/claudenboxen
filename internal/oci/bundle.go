package oci

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	specs "github.com/opencontainers/runtime-spec/specs-go"
)

// Config describes one container invocation (either a normal run of `claude`
// or an image-build step). internal/run and internal/image translate their
// own concerns into this before calling BuildSpec.
type Config struct {
	RootfsPath string // absolute path to the rootfs directory
	Hostname   string
	Terminal   bool
	Args       []string
	Env        []string
	Cwd        string // absolute path, must exist inside the rootfs/mounts
	UID        uint32
	GID        uint32

	// HostNetwork must match whether the caller will invoke runsc with
	// --network=host: it omits the network namespace entirely rather than
	// creating an isolated, routeless one (see defaultNamespaces).
	HostNetwork bool

	// ExtraMounts are appended after the default proc/dev/sys mounts, in
	// order: cwd, persistence (~/.claude, ~/.claude.json), then anything
	// from --mount/--device.
	ExtraMounts []specs.Mount
	Devices     []specs.LinuxDevice
	DeviceRules []specs.LinuxDeviceCgroup
}

// BuildSpec assembles a full OCI spec for the given invocation. Root.Readonly
// is left false; run-mode's ephemeral-write boundary comes from runsc's own
// --overlay2 flag (internal/runtime), not from the spec itself.
func BuildSpec(cfg Config) *specs.Spec {
	spec := &specs.Spec{
		Version:  ociVersion,
		Hostname: cfg.Hostname,
		Root: &specs.Root{
			Path:     cfg.RootfsPath,
			Readonly: false,
		},
		Process: &specs.Process{
			Terminal: cfg.Terminal,
			User:     specs.User{UID: cfg.UID, GID: cfg.GID},
			Args:     cfg.Args,
			Env:      cfg.Env,
			Cwd:      cfg.Cwd,
		},
		Mounts: append(defaultMounts(), cfg.ExtraMounts...),
		Linux: &specs.Linux{
			Namespaces: defaultNamespaces(cfg.HostNetwork),
		},
	}
	if len(cfg.Devices) > 0 {
		spec.Linux.Devices = cfg.Devices
	}
	if len(cfg.DeviceRules) > 0 {
		spec.Linux.Resources = &specs.LinuxResources{Devices: cfg.DeviceRules}
	}
	return spec
}

// WriteBundle creates bundleDir (if needed) and writes spec as config.json
// inside it, returning the config.json path.
func WriteBundle(bundleDir string, spec *specs.Spec) (string, error) {
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		return "", fmt.Errorf("create bundle dir: %w", err)
	}
	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal config.json: %w", err)
	}
	path := filepath.Join(bundleDir, "config.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("write config.json: %w", err)
	}
	return path, nil
}
