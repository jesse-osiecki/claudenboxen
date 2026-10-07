package vmrun

import (
	"strings"
	"testing"
)

func TestParseUSBSpec(t *testing.T) {
	d, err := ParseUSBSpec("046D:C52B")
	if err != nil {
		t.Fatal(err)
	}
	if d.VendorID != "046d" || d.ProductID != "c52b" {
		t.Fatalf("got %+v", d)
	}
	for _, bad := range []string{"", "046d", "046d:", "046d:c52", "046d:c52bb", "xyz0:0000", "046d-c52b"} {
		if _, err := ParseUSBSpec(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestBuildArgs(t *testing.T) {
	base := Config{
		GOARCH: "amd64", Kernel: "/k", Initrd: "/i", OverlayDisk: "/o.qcow2",
		MemoryMB: 1024, CPUs: 2, Network: "sandbox",
		Mounts: []MountSocket{{Tag: "project", SocketPath: "/s.sock"}},
	}

	args, err := BuildArgs(base)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-accel kvm", "-machine q35", "-m 1024M", "-smp 2",
		"console=ttyS0", "init=/opt/claudenboxen/init",
		"-netdev user,id=net0",
		"vhost-user-fs-pci,chardev=char-project,tag=project",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	if strings.Contains(joined, "usb-host") || strings.Contains(joined, "qemu-xhci") {
		t.Errorf("unexpected USB devices without --usb: %s", joined)
	}

	noNet := base
	noNet.Network = "none"
	args, _ = BuildArgs(noNet)
	if strings.Contains(strings.Join(args, " "), "-netdev") {
		t.Error("network=none should add no -netdev")
	}

	usb := base
	usb.USB = []USBDevice{{VendorID: "058f", ProductID: "9380"}}
	args, _ = BuildArgs(usb)
	joined = strings.Join(args, " ")
	if !strings.Contains(joined, "qemu-xhci,id=cbx-usb") || !strings.Contains(joined, "usb-host,bus=cbx-usb.0,vendorid=0x058f,productid=0x9380") {
		t.Errorf("USB passthrough args missing: %s", joined)
	}

	arm := base
	arm.GOARCH = "arm64"
	args, _ = BuildArgs(arm)
	joined = strings.Join(args, " ")
	if !strings.Contains(joined, "-machine virt") || !strings.Contains(joined, "console=ttyAMA0") {
		t.Errorf("arm64 machine/console wrong: %s", joined)
	}

	for _, bad := range []Config{
		{GOARCH: "amd64", MemoryMB: 0, CPUs: 1},
		{GOARCH: "amd64", MemoryMB: 512, CPUs: 0},
		{GOARCH: "mips", MemoryMB: 512, CPUs: 1},
	} {
		if _, err := BuildArgs(bad); err == nil {
			t.Errorf("%+v: expected error", bad)
		}
	}
}

func TestQEMUBinaryName(t *testing.T) {
	if n, _ := qemuBinaryName("amd64"); n != "qemu-system-x86_64" {
		t.Error(n)
	}
	if n, _ := qemuBinaryName("arm64"); n != "qemu-system-aarch64" {
		t.Error(n)
	}
	if _, err := qemuBinaryName("riscv64"); err == nil {
		t.Error("expected error")
	}
}
