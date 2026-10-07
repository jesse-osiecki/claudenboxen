// Package prereq validates the host is ready to run claudenboxen.
package prereq

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/jesse-osiecki/claudenboxen/internal/image"
	"github.com/jesse-osiecki/claudenboxen/internal/oci"
	clruntime "github.com/jesse-osiecki/claudenboxen/internal/runtime"
	"github.com/jesse-osiecki/claudenboxen/internal/vmrun"
)

// Check is one named pass/fail result in a Report.
type Check struct {
	Name    string
	OK      bool
	Detail  string
	FixHint string
}

// Report is the full `claudenboxen doctor` output.
type Report struct {
	Checks []Check
}

// OK reports whether every check passed.
func (r Report) OK() bool {
	for _, c := range r.Checks {
		if !c.OK {
			return false
		}
	}
	return true
}

// QuickCheck is the fast validation run before every normal invocation:
// platform, runsc presence, cache-dir writability. Returns the runsc path.
func QuickCheck(runscOverride string) (runscPath string, err error) {
	if goruntime.GOOS != "linux" {
		return "", fmt.Errorf("claudenboxen only runs on Linux (found %s)", goruntime.GOOS)
	}
	if goruntime.GOARCH != "amd64" && goruntime.GOARCH != "arm64" {
		return "", fmt.Errorf("unsupported architecture %s (claudenboxen supports amd64 and arm64)", goruntime.GOARCH)
	}
	path, err := clruntime.ResolveBinary(runscOverride)
	if err != nil {
		return "", err
	}
	cacheDir, err := image.CacheDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", fmt.Errorf("cache directory %s is not writable: %w", cacheDir, err)
	}
	return path, nil
}

// Doctor runs the full prerequisite report, including a real sandbox
// launch against wantNetwork. With isolation "vm" it adds the VM backend's
// checks; runsc is checked either way since the VM image is built with it.
func Doctor(runscOverride, wantNetwork, isolation string) Report {
	var r Report

	r.Checks = append(r.Checks, Check{
		Name:    "platform",
		OK:      goruntime.GOOS == "linux" && (goruntime.GOARCH == "amd64" || goruntime.GOARCH == "arm64"),
		Detail:  fmt.Sprintf("%s/%s", goruntime.GOOS, goruntime.GOARCH),
		FixHint: "claudenboxen only supports Linux on amd64 or arm64",
	})

	runscPath, err := clruntime.ResolveBinary(runscOverride)
	runscCheck := Check{Name: "runsc binary"}
	if err != nil {
		runscCheck.OK = false
		runscCheck.Detail = err.Error()
		runscCheck.FixHint = "install gVisor: https://gvisor.dev/docs/user_guide/install/"
		r.Checks = append(r.Checks, runscCheck)
		return r // nothing else can run without the binary
	}
	runscCheck.OK = true
	runscCheck.Detail = runscPath
	r.Checks = append(r.Checks, runscCheck)

	version, vErr := exec.Command(runscPath, "-version").CombinedOutput()
	r.Checks = append(r.Checks, Check{
		Name:    "runsc version",
		OK:      vErr == nil,
		Detail:  strings.TrimSpace(string(version)),
		FixHint: "runsc -version failed to run; reinstall gVisor",
	})

	cacheDir, cErr := image.CacheDir()
	cacheOK := cErr == nil
	if cacheOK {
		cacheOK = os.MkdirAll(cacheDir, 0o755) == nil
	}
	r.Checks = append(r.Checks, Check{
		Name:    "cache directory",
		OK:      cacheOK,
		Detail:  cacheDir,
		FixHint: "ensure " + cacheDir + " is writable",
	})

	stateDir, sErr := image.RunStateDir()
	if sErr == nil {
		sErr = os.MkdirAll(stateDir, 0o755)
	}
	r.Checks = append(r.Checks, Check{
		Name:    "runsc state directory",
		OK:      sErr == nil,
		Detail:  stateDir,
		FixHint: "ensure " + stateDir + " is writable",
	})

	smokeErr := runSmokeTest(runscPath, stateDir, "none", false)
	r.Checks = append(r.Checks, Check{
		Name:    "sandbox smoke test (network=none)",
		OK:      smokeErr == nil,
		Detail:  errString(smokeErr),
		FixHint: "the gVisor sandbox itself couldn't start; check kernel compatibility and AppArmor/seccomp restrictions",
	})

	if wantNetwork != "none" {
		netErr := runSmokeTest(runscPath, stateDir, wantNetwork, wantNetwork == "host")
		r.Checks = append(r.Checks, Check{
			Name:    fmt.Sprintf("sandbox smoke test (network=%s)", wantNetwork),
			OK:      netErr == nil,
			Detail:  errString(netErr),
			FixHint: networkFixHint(wantNetwork, netErr),
		})
	}

	if isolation == "vm" {
		r.Checks = append(r.Checks, vmChecks(goruntime.GOARCH)...)
	}

	return r
}

// vmChecks covers the VM backend's prerequisites: presence and permission
// checks only. A boot test would need the multi-GB base image built first.
func vmChecks(goarch string) []Check {
	var checks []Check

	qemuCheck := Check{Name: "qemu binary"}
	if path, err := vmrun.ResolveQEMUBinary("", goarch); err != nil {
		qemuCheck.Detail = err.Error()
		qemuCheck.FixHint = "install QEMU with KVM support (e.g. `pacman -S qemu-desktop` on Arch, `apt install qemu-system-x86` on Debian/Ubuntu)"
	} else {
		qemuCheck.OK = true
		qemuCheck.Detail = path
	}
	checks = append(checks, qemuCheck)

	qemuImgCheck := Check{Name: "qemu-img binary"}
	if path, err := exec.LookPath("qemu-img"); err != nil {
		qemuImgCheck.Detail = err.Error()
		qemuImgCheck.FixHint = "install qemu-img (a separate package on some distros, e.g. `pacman -S qemu-img` on Arch)"
	} else {
		qemuImgCheck.OK = true
		qemuImgCheck.Detail = path
	}
	checks = append(checks, qemuImgCheck)

	kvmCheck := Check{Name: "/dev/kvm access"}
	if err := unix.Access("/dev/kvm", unix.R_OK|unix.W_OK); err != nil {
		kvmCheck.Detail = err.Error()
		kvmCheck.FixHint = "ensure /dev/kvm exists and is readable/writable by your user (often via the kvm group: `sudo usermod -aG kvm $USER`, then re-login)"
	} else {
		kvmCheck.OK = true
		kvmCheck.Detail = "/dev/kvm"
	}
	checks = append(checks, kvmCheck)

	virtiofsdCheck := Check{Name: "virtiofsd binary"}
	if path, err := vmrun.ResolveVirtiofsdBinary(""); err != nil {
		virtiofsdCheck.Detail = err.Error()
		virtiofsdCheck.FixHint = "install virtiofsd (usually packaged alongside qemu)"
	} else {
		virtiofsdCheck.OK = true
		virtiofsdCheck.Detail = path
	}
	checks = append(checks, virtiofsdCheck)

	mke2fsCheck := Check{Name: "mke2fs binary"}
	if path, err := exec.LookPath("mke2fs"); err != nil {
		mke2fsCheck.Detail = err.Error()
		mke2fsCheck.FixHint = "install e2fsprogs — mke2fs -d, used to build the VM's base disk image directly from a directory tree with no loop devices or root needed, requires a reasonably recent version (1.43+)"
	} else {
		version, _ := exec.Command(path, "-V").CombinedOutput()
		mke2fsCheck.OK = true
		mke2fsCheck.Detail = strings.TrimSpace(string(version))
	}
	checks = append(checks, mke2fsCheck)

	return checks
}

// networkFixHint tailors the --network=sandbox failure message. The
// obvious fix (setcap CAP_NET_ADMIN/CAP_NET_RAW on runsc) breaks gVisor on
// hosts with kernel.yama.ptrace_scope >= 1: file capabilities make its
// self-reexec a privilege-elevating exec the parent can no longer ptrace.
func networkFixHint(network string, err error) string {
	if network != "sandbox" || err == nil {
		return ""
	}
	if strings.Contains(err.Error(), "re-executing self") {
		return "runsc has file capabilities set (setcap) but this kernel restricts ptrace " +
			"(kernel.yama.ptrace_scope >= 1), which breaks gVisor's self-reexec under setcap. " +
			"Run `sudo setcap -r $(which runsc)` to undo it, then either use `sudo claudenboxen` " +
			"for --network=sandbox, or accept the default --network=host instead. See README."
	}
	return "grant runsc netns/veth privilege: sudo setcap cap_net_admin,cap_net_raw+eip $(which runsc) " +
		"(note: this can conflict with Yama ptrace hardening — see README if the sandbox stops starting afterward)"
}

func errString(err error) string {
	if err == nil {
		return "ok"
	}
	return err.Error()
}

// runSmokeTest starts a minimal sandbox (host / read-only, /bin/true) with
// the given network mode and reports whether runsc could start it.
func runSmokeTest(runscPath, stateDir, network string, hostNetwork bool) error {
	spec := oci.BuildSpec(oci.Config{
		RootfsPath:  "/",
		Hostname:    "claudenboxen-doctor",
		Terminal:    false,
		Args:        []string{"/bin/true"},
		Env:         []string{"PATH=/usr/bin:/bin"},
		Cwd:         "/",
		UID:         uint32(os.Getuid()),
		GID:         uint32(os.Getgid()),
		HostNetwork: hostNetwork,
	})
	spec.Root.Readonly = true

	tmpBundle, err := os.MkdirTemp("", "claudenboxen-doctor-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpBundle)

	if _, err := oci.WriteBundle(tmpBundle, spec); err != nil {
		return err
	}

	runner := &clruntime.Runner{
		BinPath:  runscPath,
		StateDir: stateDir,
		Network:  network,
		Overlay:  "none",
		Rootless: os.Geteuid() != 0,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	containerID := "doctor-" + filepath.Base(tmpBundle)
	cmd := runner.RunCommand(ctx, tmpBundle, containerID, "")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err = cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(out.String())
		if msg == "" {
			return err
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}
