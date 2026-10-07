// Package runtime is the only place that knows how to invoke gVisor
// (runsc). internal/oci builds the bundle; this package runs it.
package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// Runner drives a runsc binary against a fixed state directory.
type Runner struct {
	BinPath  string // absolute path to runsc
	StateDir string // runsc --root
	Network  string // sandbox | host | none
	Overlay  string // --overlay2 value, e.g. "root:memory" or "none"
	Rootless bool   // pass --rootless
}

// ResolveBinary finds runsc: an explicit override (--runsc-path or
// $CLAUDENBOXEN_RUNSC), or the first "runsc" on $PATH.
func ResolveBinary(override string) (string, error) {
	if override != "" {
		if _, err := os.Stat(override); err != nil {
			return "", fmt.Errorf("runsc not found at %q: %w", override, err)
		}
		return override, nil
	}
	if env := os.Getenv("CLAUDENBOXEN_RUNSC"); env != "" {
		return ResolveBinary(env)
	}
	path, err := exec.LookPath("runsc")
	if err != nil {
		return "", fmt.Errorf("runsc not found on PATH: install it (see https://gvisor.dev/docs/user_guide/install/) or pass --runsc-path")
	}
	return path, nil
}

func (r *Runner) globalArgs() []string {
	args := []string{"--root", r.StateDir}
	if r.Network != "" {
		args = append(args, "--network", r.Network)
	}
	if r.Overlay != "" {
		args = append(args, "--overlay2", r.Overlay)
	}
	if r.Rootless {
		args = append(args, "--rootless")
	}
	// Exposes /proc/gvisor/kernel_is_gvisor so software inside can detect
	// the sandbox.
	args = append(args, "--gvisor-marker-file")
	return args
}

// RunCommand builds the `runsc run` invocation for a bundle. With a
// consoleSocket, runsc sends the pty master fd over it (see console.go)
// and the caller relays stdio; without one, the returned command's stdio
// can be wired directly.
func (r *Runner) RunCommand(ctx context.Context, bundleDir, containerID, consoleSocket string) *exec.Cmd {
	args := r.globalArgs()
	args = append(args, "run", "--bundle", bundleDir)
	if consoleSocket != "" {
		args = append(args, "--console-socket", consoleSocket)
	}
	args = append(args, containerID)

	cmd := exec.CommandContext(ctx, r.BinPath, args...)
	cmd.Stderr = os.Stderr
	return cmd
}

// Kill signals a container. A foreground `runsc run` tears down its own
// state on normal exit; this is for the external-signal path.
func (r *Runner) Kill(containerID, signal string) error {
	args := append(r.globalArgs(), "kill", containerID, signal)
	return exec.Command(r.BinPath, args...).Run()
}

// Delete removes leftover container state.
func (r *Runner) Delete(containerID string) error {
	args := append(r.globalArgs(), "delete", "--force", containerID)
	return exec.Command(r.BinPath, args...).Run()
}
