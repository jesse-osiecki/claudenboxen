package vmrun

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/jesse-osiecki/claudenboxen/internal/image"
	clruntime "github.com/jesse-osiecki/claudenboxen/internal/runtime"
	"github.com/jesse-osiecki/claudenboxen/internal/vmimage"
	"github.com/jesse-osiecki/claudenboxen/internal/vmspec"
)

// Options collects the parsed CLI flags for a single --isolation vm run.
type Options struct {
	Path       string // absolute path to mount and cd into
	ClaudeArgs []string
	Mounts     []string // raw --mount SRC[:DST[:ro|rw]] specs
	USB        []string // raw --usb VID:PID specs
	Env        []string // raw --env KEY=VALUE specs
	NoPersist  bool
	Network    string // sandbox | none
	Rebuild    bool
	MemoryMB   int
	CPUs       int
}

// Run ensures the VM base image is built, assembles the per-run overlay
// disk and virtiofs shares, boots QEMU, and blocks until the guest's
// claude process exits, returning its exit code.
func Run(ctx context.Context, opts Options) (exitCode int, err error) {
	// A VM can't join the host's network namespace; the privilege-free
	// alternative (SLIRP) is already the default.
	if opts.Network == "host" {
		return 1, fmt.Errorf("--network host isn't supported with --isolation vm: a VM can't share the host's network namespace; use the default (sandbox, NAT'd) or none")
	}

	goarch := image.HostArch()

	// runsc is only used to build the VM image (see internal/vmimage), not
	// to run it.
	runscPath, err := clruntime.ResolveBinary("")
	if err != nil {
		return 1, fmt.Errorf("%w (runsc is also required for --isolation vm, to build its VM image — see README)", err)
	}
	qemuPath, err := ResolveQEMUBinary("", goarch)
	if err != nil {
		return 1, err
	}
	virtiofsdPath, err := ResolveVirtiofsdBinary("")
	if err != nil {
		return 1, err
	}

	var usbDevices []USBDevice
	for _, spec := range opts.USB {
		d, err := ParseUSBSpec(spec)
		if err != nil {
			return 1, err
		}
		if err := CheckUSBAccess(d); err != nil {
			return 1, err
		}
		usbDevices = append(usbDevices, d)
	}

	stateDir, err := image.RunStateDir()
	if err != nil {
		return 1, err
	}
	rootless := os.Geteuid() != 0

	img, err := vmimage.EnsureImage(ctx, vmimage.BuildEnv{
		RunscBin: runscPath,
		StateDir: stateDir,
		Rootless: rootless,
	}, opts.Rebuild, os.Stderr)
	if err != nil {
		return 1, fmt.Errorf("prepare VM image: %w", err)
	}

	runID, err := randomID()
	if err != nil {
		return 1, err
	}
	bundlesDir, err := vmimage.BundlesDir()
	if err != nil {
		return 1, err
	}
	bundleDir := filepath.Join(bundlesDir, runID)
	defer os.RemoveAll(bundleDir)
	// 0700: the control share's env file carries the host environment,
	// API keys included.
	if err := os.MkdirAll(bundleDir, 0o700); err != nil {
		return 1, err
	}

	host, err := clruntime.Host()
	if err != nil {
		return 1, err
	}
	rows, cols := 24, 80
	if size, err := host.Size(); err == nil && size.Height > 0 && size.Width > 0 {
		rows, cols = int(size.Height), int(size.Width)
	}

	mounts, controlDir, writeback, err := vmspec.Build(bundleDir, vmspec.Options{
		Path:       opts.Path,
		ClaudeArgs: opts.ClaudeArgs,
		Mounts:     opts.Mounts,
		Env:        opts.Env,
		NoPersist:  opts.NoPersist,
		Rows:       rows,
		Cols:       cols,
	})
	if err != nil {
		return 1, err
	}
	// Defers run LIFO: this fires before the bundleDir cleanup above.
	defer func() {
		if err := writeback(); err != nil {
			fmt.Fprintln(os.Stderr, "claudenboxen: warning:", err)
		}
	}()

	overlayPath := filepath.Join(bundleDir, "overlay.qcow2")
	if err := createOverlay(img.Disk, overlayPath); err != nil {
		return 1, fmt.Errorf("create VM overlay disk: %w", err)
	}

	allMounts := append([]vmspec.Mount{{Tag: vmspec.ControlTag, HostDir: controlDir}}, mounts...)

	var virtiofsdProcs []*exec.Cmd
	defer func() {
		for _, p := range virtiofsdProcs {
			_ = p.Process.Kill()
			_ = p.Wait()
		}
	}()

	var qemuMounts []MountSocket
	for _, m := range allMounts {
		sockPath := filepath.Join(bundleDir, "virtiofs-"+m.Tag+".sock")
		proc, err := startVirtiofsd(virtiofsdPath, m.HostDir, sockPath, m.ReadOnly, m.Tag == vmspec.ControlTag)
		if err != nil {
			return 1, fmt.Errorf("share %s: %w", m.HostDir, err)
		}
		virtiofsdProcs = append(virtiofsdProcs, proc)
		qemuMounts = append(qemuMounts, MountSocket{Tag: m.Tag, SocketPath: sockPath})
	}

	args, err := BuildArgs(Config{
		GOARCH:      goarch,
		Kernel:      img.Kernel,
		Initrd:      img.Initrd,
		OverlayDisk: overlayPath,
		MemoryMB:    opts.MemoryMB,
		CPUs:        opts.CPUs,
		Network:     opts.Network,
		Mounts:      qemuMounts,
		USB:         usbDevices,
	})
	if err != nil {
		return 1, err
	}

	cmd := exec.CommandContext(ctx, qemuPath, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		if _, ok := <-sigCh; ok && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()

	if err := cmd.Start(); err != nil {
		return 1, fmt.Errorf("start qemu: %w", err)
	}

	// Raw mode only after QEMU has started, so its own startup errors
	// (bad flags, KVM unavailable) print normally. Once raw, ^C and
	// friends are bytes forwarded to the guest console rather than host
	// signals.
	if err := host.SetRaw(); err != nil {
		cmd.Process.Kill() //nolint:errcheck
		cmd.Wait()         //nolint:errcheck
		return 1, fmt.Errorf("set host terminal raw: %w", err)
	}
	defer host.Reset()

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, unix.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			if size, err := host.Size(); err == nil && size.Height > 0 && size.Width > 0 {
				_ = vmspec.WriteWinsize(controlDir, int(size.Height), int(size.Width))
			}
		}
	}()

	cmdErr := cmd.Wait()

	if code, ok := readExitCode(controlDir); ok {
		return code, nil
	}
	if cmdErr != nil {
		return 1, fmt.Errorf("qemu: %w", cmdErr)
	}
	return 1, fmt.Errorf("VM session ended without reporting an exit code (the guest may have failed to boot)")
}

// createOverlay creates a per-run qcow2 overlay backed by the cached base
// disk, so the base image is never written to.
func createOverlay(basePath, overlayPath string) error {
	cmd := exec.Command("qemu-img", "create", "-f", "qcow2", "-F", "raw", "-b", basePath, overlayPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("%s: %w", msg, err)
		}
		return err
	}
	return nil
}

// readExitCode reads the exit code init.sh writes to the control share
// before powering off; QEMU's own exit status says nothing about the guest
// process.
func readExitCode(controlDir string) (int, bool) {
	data, err := os.ReadFile(filepath.Join(controlDir, "exitcode"))
	if err != nil {
		return 0, false
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false
	}
	return code, true
}

func randomID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
