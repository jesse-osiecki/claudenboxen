// Package vmrun drives the VM backend: it resolves QEMU and virtiofsd,
// starts one virtiofsd per share, builds and runs the QEMU invocation, and
// attaches the host terminal to the guest's serial console.
package vmrun

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

// ResolveQEMUBinary finds qemu-system-<arch>: an explicit override,
// $CLAUDENBOXEN_QEMU, or the first match on $PATH.
func ResolveQEMUBinary(override, goarch string) (string, error) {
	if override != "" {
		if _, err := os.Stat(override); err != nil {
			return "", fmt.Errorf("qemu not found at %q: %w", override, err)
		}
		return override, nil
	}
	if env := os.Getenv("CLAUDENBOXEN_QEMU"); env != "" {
		return ResolveQEMUBinary(env, goarch)
	}
	name, err := qemuBinaryName(goarch)
	if err != nil {
		return "", err
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s not found on PATH: install QEMU with KVM support", name)
	}
	return path, nil
}

func qemuBinaryName(goarch string) (string, error) {
	switch goarch {
	case "amd64":
		return "qemu-system-x86_64", nil
	case "arm64":
		return "qemu-system-aarch64", nil
	default:
		return "", fmt.Errorf("unsupported architecture %q (claudenboxen supports amd64 and arm64)", goarch)
	}
}

// MountSocket is one virtiofs share as QEMU sees it: a listening
// vhost-user socket and the tag the guest mounts it by.
type MountSocket struct {
	Tag        string
	SocketPath string
}

// Config is everything BuildArgs needs for one QEMU invocation.
type Config struct {
	GOARCH      string
	Kernel      string
	Initrd      string
	OverlayDisk string // per-run qcow2 backed by the cached base disk
	MemoryMB    int
	CPUs        int
	Network     string // sandbox | none
	Mounts      []MountSocket
	USB         []USBDevice
}

// BuildArgs assembles the qemu-system-<arch> argument list: direct kernel
// boot, the overlay as the virtio root disk, usermode networking unless
// disabled, one vhost-user-fs device per share (plus the shared memory
// backend vhost-user requires), optional USB passthrough, and a serial
// console on our own stdio. -display/-monitor/-parallel none is used
// instead of -nographic so QEMU doesn't layer monitor escape handling onto
// the console byte stream.
func BuildArgs(cfg Config) ([]string, error) {
	machine, console, err := machineAndConsole(cfg.GOARCH)
	if err != nil {
		return nil, err
	}
	if cfg.MemoryMB <= 0 {
		return nil, fmt.Errorf("--vm-memory must be positive, got %d", cfg.MemoryMB)
	}
	if cfg.CPUs <= 0 {
		return nil, fmt.Errorf("--vm-cpus must be positive, got %d", cfg.CPUs)
	}

	// quiet/loglevel keep kernel and initramfs chatter off the console so
	// the session starts on a clean screen; errors still print. panic=1
	// with -no-reboot makes a guest crash exit QEMU instead of hanging.
	cmdline := fmt.Sprintf("root=/dev/vda rootfstype=ext4 rw modules=virtio_pci,virtio_blk,ext4 console=%s,115200n8 quiet loglevel=3 panic=1 init=/opt/claudenboxen/init", console)

	args := []string{
		"-accel", "kvm",
		"-cpu", "host",
		"-machine", machine,
		"-m", fmt.Sprintf("%dM", cfg.MemoryMB),
		"-smp", strconv.Itoa(cfg.CPUs),
		"-object", fmt.Sprintf("memory-backend-memfd,id=mem,size=%dM,share=on", cfg.MemoryMB),
		"-numa", "node,memdev=mem",
		"-nodefaults",
		"-no-reboot",
		"-display", "none",
		"-serial", "stdio",
		"-monitor", "none",
		"-parallel", "none",
		"-kernel", cfg.Kernel,
		"-initrd", cfg.Initrd,
		"-append", cmdline,
		"-drive", fmt.Sprintf("file=%s,if=virtio,format=qcow2", cfg.OverlayDisk),
		"-device", "virtio-rng-pci",
	}

	if cfg.Network != "none" {
		args = append(args, "-netdev", "user,id=net0", "-device", "virtio-net-pci,netdev=net0")
	}

	for _, m := range cfg.Mounts {
		args = append(args,
			"-chardev", fmt.Sprintf("socket,id=char-%s,path=%s", m.Tag, m.SocketPath),
			"-device", fmt.Sprintf("vhost-user-fs-pci,chardev=char-%s,tag=%s", m.Tag, m.Tag),
		)
	}

	if len(cfg.USB) > 0 {
		args = append(args, "-device", "qemu-xhci,id=cbx-usb")
		// Matching by vendor/product rather than bus address lets a device
		// that re-enumerates mid-session (e.g. a board being flashed)
		// reattach to the guest automatically.
		for _, d := range cfg.USB {
			args = append(args, "-device", fmt.Sprintf("usb-host,bus=cbx-usb.0,vendorid=0x%s,productid=0x%s", d.VendorID, d.ProductID))
		}
	}

	return args, nil
}

// machineAndConsole returns the -machine type and serial console device
// for goarch; aarch64's virt machine exposes a PL011 UART, not a 16550.
func machineAndConsole(goarch string) (machine, console string, err error) {
	switch goarch {
	case "amd64":
		return "q35", "ttyS0", nil
	case "arm64":
		return "virt", "ttyAMA0", nil
	default:
		return "", "", fmt.Errorf("unsupported architecture %q (claudenboxen supports amd64 and arm64)", goarch)
	}
}
