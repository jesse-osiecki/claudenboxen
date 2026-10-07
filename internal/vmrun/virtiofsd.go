package vmrun

import (
	"fmt"
	"os"
	"os/exec"
	"time"
)

// ResolveVirtiofsdBinary finds virtiofsd: an explicit override,
// $CLAUDENBOXEN_VIRTIOFSD, the usual libexec locations (it's often not on
// $PATH), then $PATH itself.
func ResolveVirtiofsdBinary(override string) (string, error) {
	if override != "" {
		if _, err := os.Stat(override); err != nil {
			return "", fmt.Errorf("virtiofsd not found at %q: %w", override, err)
		}
		return override, nil
	}
	if env := os.Getenv("CLAUDENBOXEN_VIRTIOFSD"); env != "" {
		return ResolveVirtiofsdBinary(env)
	}
	for _, candidate := range []string{"/usr/lib/virtiofsd", "/usr/libexec/virtiofsd"} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	if path, err := exec.LookPath("virtiofsd"); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("virtiofsd not found (checked /usr/lib/virtiofsd, /usr/libexec/virtiofsd, $PATH): install it, usually packaged alongside qemu")
}

// startVirtiofsd shares hostDir at socketPath and returns once the socket
// is listening, which QEMU requires before it can connect. uncached
// disables guest-side caching so host writes are visible immediately (used
// for the control share, which carries live state like the window size).
//
// --sandbox none: virtiofsd's own namespace/chroot sandboxing needs
// privilege this process doesn't have. It still only serves hostDir's
// subtree, as the same unprivileged user. --inode-file-handles never and
// --rlimit-nofile 0 skip two startup probes that also need privilege and
// would otherwise log a warning on every run.
func startVirtiofsd(bin, hostDir, socketPath string, readOnly, uncached bool) (*exec.Cmd, error) {
	args := []string{
		"--shared-dir", hostDir,
		"--socket-path", socketPath,
		"--sandbox", "none",
		"--inode-file-handles=never",
		"--rlimit-nofile", "0",
		"--log-level", "warn",
	}
	if readOnly {
		args = append(args, "--readonly")
	}
	if uncached {
		args = append(args, "--cache", "never")
	}

	cmd := exec.Command(bin, args...)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start virtiofsd for %s: %w", hostDir, err)
	}

	if err := waitForSocket(socketPath, 5*time.Second); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	return cmd, nil
}

func waitForSocket(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for virtiofsd to listen on %s", path)
}
