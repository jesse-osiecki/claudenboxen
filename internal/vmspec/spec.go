// Package vmspec builds the per-run spec for the VM backend: the virtiofs
// shares a run needs, and the control share (a small virtiofs mount of its
// own, tag ControlTag) that the guest's init script reads at boot — mounts
// to make, the guest process's environment and argv, the working
// directory, the console size, and the host identity to synthesize a
// non-root account from. It plays the role internal/oci plays for the
// gVisor backend, but produces flat files for a shell script rather than
// an OCI config.json.
package vmspec

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
)

// ControlTag is the fixed mount tag of the control share. Every other tag
// is read from the control share itself, so this one must be agreed on
// statically.
const ControlTag = "cbx-config"

// Mount is one virtiofs share: internal/vmrun runs a virtiofsd per Mount
// and attaches it to QEMU under Tag; init.sh mounts Tag at GuestPath.
type Mount struct {
	Tag       string
	HostDir   string
	GuestPath string
	ReadOnly  bool
}

// Options collects what a single VM run needs.
type Options struct {
	Path       string // absolute path to mount and cd into
	ClaudeArgs []string
	Mounts     []string // raw --mount SRC[:DST[:ro|rw]] specs
	Env        []string // raw --env KEY=VALUE specs
	NoPersist  bool
	Rows, Cols int // initial console size
}

// Build creates the control share and home staging directory under
// bundleDir, writes the control files, and returns the shares to attach
// (not including the control share itself) plus a writeback func that
// copies ~/.claude.json back to the host after the run — virtiofs shares
// directories, not single files, so that one is staged in a directory of
// its own.
func Build(bundleDir string, opts Options) (mounts []Mount, controlDir string, writeback func() error, err error) {
	noop := func() error { return nil }

	controlDir = filepath.Join(bundleDir, "cbx-config")
	if err := os.MkdirAll(controlDir, 0o700); err != nil {
		return nil, "", noop, err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, "", noop, fmt.Errorf("resolve home directory: %w", err)
	}

	mounts = append(mounts, Mount{Tag: "project", HostDir: opts.Path, GuestPath: opts.Path})

	writeback = noop
	if !opts.NoPersist {
		// "home" is listed before "claudedir": init.sh mounts in order, and
		// $HOME must exist before $HOME/.claude can be mounted inside it.
		homeStage := filepath.Join(bundleDir, "vm-home")
		if err := os.MkdirAll(homeStage, 0o700); err != nil {
			return nil, "", noop, err
		}
		claudeJSON := filepath.Join(home, ".claude.json")
		if _, err := os.Stat(claudeJSON); os.IsNotExist(err) {
			if err := os.WriteFile(claudeJSON, []byte("{}"), 0o600); err != nil {
				return nil, "", noop, fmt.Errorf("create %s: %w", claudeJSON, err)
			}
		}
		stagedJSON := filepath.Join(homeStage, ".claude.json")
		if err := copyFile(claudeJSON, stagedJSON); err != nil {
			return nil, "", noop, fmt.Errorf("stage %s: %w", claudeJSON, err)
		}
		mounts = append(mounts, Mount{Tag: "home", HostDir: homeStage, GuestPath: home})

		claudeDir := filepath.Join(home, ".claude")
		if err := os.MkdirAll(claudeDir, 0o700); err != nil {
			return nil, "", noop, fmt.Errorf("create %s: %w", claudeDir, err)
		}
		mounts = append(mounts, Mount{Tag: "claudedir", HostDir: claudeDir, GuestPath: claudeDir})

		writeback = func() error {
			return copyFile(stagedJSON, claudeJSON)
		}
	}

	for i, m := range opts.Mounts {
		src, dst, ro, err := parseMountSpec(m)
		if err != nil {
			return nil, "", writeback, err
		}
		mounts = append(mounts, Mount{Tag: fmt.Sprintf("extra%d", i), HostDir: src, GuestPath: dst, ReadOnly: ro})
	}

	if err := writeControlFiles(controlDir, opts, mounts); err != nil {
		return nil, "", writeback, err
	}

	return mounts, controlDir, writeback, nil
}

// WriteWinsize publishes the console size to the control share. The guest
// polls this file and applies changes to its console, which is how window
// resizes reach a session over a serial UART with no out-of-band signal.
// The write is atomic (rename) so the guest never reads a partial line.
func WriteWinsize(controlDir string, rows, cols int) error {
	path := filepath.Join(controlDir, "winsize")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(fmt.Sprintf("%d %d\n", rows, cols)), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func writeControlFiles(controlDir string, opts Options, mounts []Mount) error {
	u, err := user.Current()
	if err != nil {
		return fmt.Errorf("look up current user: %w", err)
	}

	// Fields with no spaces come first so HOME, read last, absorbs any.
	identity := fmt.Sprintf("%s %s %s %s\n", u.Uid, u.Gid, u.Username, u.HomeDir)
	if err := os.WriteFile(filepath.Join(controlDir, "identity"), []byte(identity), 0o600); err != nil {
		return err
	}

	if err := os.WriteFile(filepath.Join(controlDir, "cwd"), []byte(opts.Path+"\n"), 0o600); err != nil {
		return err
	}
	if err := WriteWinsize(controlDir, opts.Rows, opts.Cols); err != nil {
		return err
	}

	var mountLines strings.Builder
	for _, m := range mounts {
		fmt.Fprintf(&mountLines, "%s %s\n", m.Tag, m.GuestPath)
	}
	if err := os.WriteFile(filepath.Join(controlDir, "mounts"), []byte(mountLines.String()), 0o600); err != nil {
		return err
	}

	env := buildEnv(opts.Env)
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var envLines strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&envLines, "%s=%s\n", k, shellQuote(env[k]))
	}
	if err := os.WriteFile(filepath.Join(controlDir, "env"), []byte(envLines.String()), 0o600); err != nil {
		return err
	}

	var argLines strings.Builder
	argLines.WriteString("claude\n")
	for _, a := range opts.ClaudeArgs {
		argLines.WriteString(a)
		argLines.WriteString("\n")
	}
	return os.WriteFile(filepath.Join(controlDir, "args"), []byte(argLines.String()), 0o600)
}

// buildEnv starts from the host environment (so API keys and shell setup
// carry over), resets PATH to guest paths, and applies --env overrides
// last so they win.
func buildEnv(overrides []string) map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	env["PATH"] = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	env["USE_BUILTIN_RIPGREP"] = "0" // Claude Code's bundled ripgrep is glibc-linked; the image is musl
	for _, kv := range overrides {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	return env
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}
