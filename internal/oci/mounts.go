package oci

import (
	"fmt"
	"os"
	"strings"

	specs "github.com/opencontainers/runtime-spec/specs-go"
	"golang.org/x/sys/unix"
)

// ParseMountSpec parses "SRC[:DST[:ro|rw]]" (the --mount flag and the
// default cwd/persistence mounts share this format). DST defaults to SRC
// (mirrored path). rw is the default access mode.
func ParseMountSpec(s string) (specs.Mount, error) {
	src, dst, mode, err := splitSpec(s, "rw")
	if err != nil {
		return specs.Mount{}, err
	}
	if mode != "ro" && mode != "rw" {
		return specs.Mount{}, fmt.Errorf("mount %q: mode must be ro or rw, got %q", s, mode)
	}

	if _, err := os.Stat(src); err != nil {
		return specs.Mount{}, fmt.Errorf("mount %q: %w", s, err)
	}

	options := []string{"rbind", mode}

	return specs.Mount{
		Destination: dst,
		Type:        "bind",
		Source:      src,
		Options:     options,
	}, nil
}

// DeviceSpec is a host device node passed through with --device: a bind
// mount for the node itself, plus the OCI device entry and cgroup allow
// rule needed for the sentry to grant it access.
type DeviceSpec struct {
	Mount  specs.Mount
	Device specs.LinuxDevice
	Cgroup specs.LinuxDeviceCgroup
}

// ParseDeviceSpec parses "SRC[:DST]" for the --device flag. SRC must be an
// existing character or block device node on the host.
func ParseDeviceSpec(s string) (DeviceSpec, error) {
	src, dst, _, err := splitSpec(s, "")
	if err != nil {
		return DeviceSpec{}, err
	}

	var st unix.Stat_t
	if err := unix.Stat(src, &st); err != nil {
		return DeviceSpec{}, fmt.Errorf("device %q: %w", s, err)
	}

	var devType string
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFCHR:
		devType = "c"
	case unix.S_IFBLK:
		devType = "b"
	default:
		return DeviceSpec{}, fmt.Errorf("device %q: not a character or block device", s)
	}

	major := int64(unix.Major(uint64(st.Rdev)))
	minor := int64(unix.Minor(uint64(st.Rdev)))
	mode := os.FileMode(st.Mode & 0o777)

	return DeviceSpec{
		Mount: specs.Mount{
			Destination: dst,
			Type:        "bind",
			Source:      src,
			Options:     []string{"rbind", "rw"},
		},
		Device: specs.LinuxDevice{
			Path:     dst,
			Type:     devType,
			Major:    major,
			Minor:    minor,
			FileMode: &mode,
		},
		Cgroup: specs.LinuxDeviceCgroup{
			Allow:  true,
			Type:   devType,
			Major:  &major,
			Minor:  &minor,
			Access: "rwm",
		},
	}, nil
}

// splitSpec splits "SRC[:DST[:THIRD]]". DST defaults to SRC and THIRD to
// defaultThird when omitted.
func splitSpec(s, defaultThird string) (src, dst, third string, err error) {
	parts := strings.Split(s, ":")
	switch len(parts) {
	case 1:
		return parts[0], parts[0], defaultThird, nil
	case 2:
		return parts[0], parts[1], defaultThird, nil
	case 3:
		return parts[0], parts[1], parts[2], nil
	default:
		return "", "", "", fmt.Errorf("invalid spec %q: expected SRC[:DST[:MODE]]", s)
	}
}
