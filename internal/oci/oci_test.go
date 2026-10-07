package oci

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMountSpec(t *testing.T) {
	dir := t.TempDir()

	m, err := ParseMountSpec(dir)
	if err != nil || m.Source != dir || m.Destination != dir || strings.Join(m.Options, ",") != "rbind,rw" {
		t.Fatalf("plain: %+v %v", m, err)
	}
	m, err = ParseMountSpec(dir + ":/dst:ro")
	if err != nil || m.Destination != "/dst" || strings.Join(m.Options, ",") != "rbind,ro" {
		t.Fatalf("ro: %+v %v", m, err)
	}
	for _, bad := range []string{dir + ":/x:bogus", dir + ":/x:ro:more", filepath.Join(dir, "nope")} {
		if _, err := ParseMountSpec(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestParseDeviceSpec(t *testing.T) {
	d, err := ParseDeviceSpec("/dev/null:/dev/sandbox-null")
	if err != nil {
		t.Fatal(err)
	}
	if d.Device.Type != "c" || d.Device.Path != "/dev/sandbox-null" || d.Device.Major != 1 || d.Device.Minor != 3 {
		t.Errorf("device: %+v", d.Device)
	}
	if !d.Cgroup.Allow || d.Cgroup.Access != "rwm" {
		t.Errorf("cgroup: %+v", d.Cgroup)
	}
	if _, err := ParseDeviceSpec(t.TempDir()); err == nil {
		t.Error("directory should be rejected")
	}
}

func TestMergeIntoFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "passwd")
	if err := os.WriteFile(src, []byte("root:x:0:0:root:/root:/bin/bash\nnobody:x:65534:65534::/:/bin/false\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := mergeIntoFile(src, filepath.Join(dir, "out1"), "me:x:1000:1000::/home/me:/bin/bash\n", ":", 3, "1000")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(out)
	if !strings.HasSuffix(string(data), "me:x:1000:1000::/home/me:/bin/bash\n") || strings.Count(string(data), "\n") != 3 {
		t.Errorf("appended: %q", data)
	}

	out, err = mergeIntoFile(src, filepath.Join(dir, "out2"), "dup:x:0:0::/:/bin/sh\n", ":", 3, "0")
	if err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(out)
	if strings.Contains(string(data), "dup:") {
		t.Errorf("colliding uid should not be appended: %q", data)
	}
}

func TestBuildSpecNamespaces(t *testing.T) {
	hasNet := func(hostNetwork bool) bool {
		spec := BuildSpec(Config{RootfsPath: "/", Args: []string{"true"}, HostNetwork: hostNetwork})
		for _, ns := range spec.Linux.Namespaces {
			if ns.Type == "network" {
				return true
			}
		}
		return false
	}
	if hasNet(true) {
		t.Error("host networking must not unshare the network namespace")
	}
	if !hasNet(false) {
		t.Error("isolated networking must unshare the network namespace")
	}
}
