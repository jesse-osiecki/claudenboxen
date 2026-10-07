// Package run wires together image, oci, and runtime for the gVisor
// backend: build or reuse the base image, assemble the mounts, and run
// `claude` attached to the host terminal.
package run

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	specs "github.com/opencontainers/runtime-spec/specs-go"

	"github.com/jesse-osiecki/claudenboxen/internal/image"
	"github.com/jesse-osiecki/claudenboxen/internal/oci"
	clruntime "github.com/jesse-osiecki/claudenboxen/internal/runtime"
)

// Options collects the parsed CLI flags for a single invocation.
type Options struct {
	Path       string // absolute path to mount and cd into
	ClaudeArgs []string
	Mounts     []string // raw --mount SRC[:DST[:ro|rw]] specs
	Devices    []string // raw --device SRC[:DST] specs
	Env        []string // raw --env KEY=VALUE specs
	NoPersist  bool
	Network    string // sandbox | host | none
	Rebuild    bool
	RunscPath  string
}

// Run builds or reuses the base image, assembles the sandbox, and blocks
// until the `claude` session exits, returning its exit code.
func Run(ctx context.Context, opts Options) (exitCode int, err error) {
	runscPath, err := clruntime.ResolveBinary(opts.RunscPath)
	if err != nil {
		return 1, err
	}
	stateDir, err := image.RunStateDir()
	if err != nil {
		return 1, err
	}
	rootless := os.Geteuid() != 0

	rootfs, err := image.EnsureImage(ctx, image.BuildEnv{
		RunscBin: runscPath,
		StateDir: stateDir,
		Rootless: rootless,
	}, opts.Rebuild, os.Stderr)
	if err != nil {
		return 1, fmt.Errorf("prepare base image: %w", err)
	}

	runID, err := randomID()
	if err != nil {
		return 1, err
	}
	bundlesDir, err := image.BundlesDir()
	if err != nil {
		return 1, err
	}
	bundleDir := filepath.Join(bundlesDir, runID)
	defer os.RemoveAll(bundleDir)
	// 0700: shadow copies of credential files (see shadow.go) live here
	// with relaxed read bits.
	if err := os.MkdirAll(bundleDir, 0o700); err != nil {
		return 1, err
	}

	mounts, devices, deviceRules, writeback, err := buildMounts(opts, rootfs, bundleDir)
	if err != nil {
		return 1, err
	}
	// Defers run LIFO: this fires before the bundleDir cleanup above.
	defer func() {
		if err := writeback(); err != nil {
			fmt.Fprintln(os.Stderr, "claudenboxen: warning:", err)
		}
	}()

	spec := oci.BuildSpec(oci.Config{
		RootfsPath: rootfs,
		Hostname:   "claudenboxen",
		Terminal:   true,
		Args:       append([]string{"claude"}, opts.ClaudeArgs...),
		Env:        buildEnv(opts.Env),
		Cwd:        opts.Path,
		// uid 0 inside the sandbox, not the host uid: rootless gVisor only
		// allows creating files in a bind mount when the sandboxed uid is
		// 0. The sentry still performs host I/O as the real user, so files
		// land on disk owned by them. See README, "Why fake-root".
		UID:         0,
		GID:         0,
		HostNetwork: opts.Network == "host",
		ExtraMounts: mounts,
		Devices:     devices,
		DeviceRules: deviceRules,
	})

	if _, err := oci.WriteBundle(bundleDir, spec); err != nil {
		return 1, err
	}

	consoleSocket, err := clruntime.ListenConsoleSocket(filepath.Join(bundleDir, "console.sock"))
	if err != nil {
		return 1, err
	}
	defer consoleSocket.Close()

	runner := &clruntime.Runner{
		BinPath:  runscPath,
		StateDir: stateDir,
		Network:  opts.Network,
		Overlay:  "root:memory",
		Rootless: rootless,
	}
	containerID := "claudenboxen-" + runID

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := runner.RunCommand(runCtx, bundleDir, containerID, consoleSocket.Path)
	if err := cmd.Start(); err != nil {
		return 1, fmt.Errorf("start runsc: %w", err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		if _, ok := <-sigCh; ok {
			_ = runner.Kill(containerID, "KILL")
		}
	}()

	sandboxConsole, err := consoleSocket.Accept()
	if err != nil {
		cancel()
		cmd.Wait() //nolint:errcheck
		return 1, fmt.Errorf("attach sandbox console: %w", err)
	}
	defer sandboxConsole.Close()

	attachErrCh := make(chan error, 1)
	go func() { attachErrCh <- clruntime.Attach(sandboxConsole) }()
	waitErrCh := make(chan error, 1)
	go func() { waitErrCh <- cmd.Wait() }()

	var attachErr, waitErr error
	select {
	case attachErr = <-attachErrCh:
		if attachErr != nil {
			// No terminal to attach: the sandboxed process can't be
			// driven or observed, so don't leave it running.
			_ = runner.Kill(containerID, "KILL")
		}
		waitErr = <-waitErrCh
	case waitErr = <-waitErrCh:
		// Drain the attach goroutine so trailing output is flushed.
		attachErr = <-attachErrCh
	}

	if attachErr != nil {
		return 1, fmt.Errorf("attach terminal: %w", attachErr)
	}
	if waitErr != nil {
		if cmd.ProcessState != nil {
			return cmd.ProcessState.ExitCode(), nil
		}
		return 1, fmt.Errorf("runsc run: %w", waitErr)
	}
	return cmd.ProcessState.ExitCode(), nil
}

// buildMounts assembles the identity, cwd, persistence, and user-supplied
// --mount/--device entries, in that order. The returned writeback must run
// after the session ends and before workDir is removed.
func buildMounts(opts Options, rootfs, workDir string) (mounts []specs.Mount, devices []specs.LinuxDevice, deviceRules []specs.LinuxDeviceCgroup, writeback func() error, err error) {
	noop := func() error { return nil }

	identityMounts, err := oci.IdentityMounts(rootfs, workDir)
	if err != nil {
		return nil, nil, nil, noop, err
	}
	mounts = append(mounts, identityMounts...)

	cwdMount, err := oci.ParseMountSpec(opts.Path)
	if err != nil {
		return nil, nil, nil, noop, err
	}
	mounts = append(mounts, cwdMount)

	writeback = noop
	if !opts.NoPersist {
		persistMounts, wb, err := persistenceMounts(workDir)
		if err != nil {
			return nil, nil, nil, noop, err
		}
		mounts = append(mounts, persistMounts...)
		writeback = wb
	}

	for _, m := range opts.Mounts {
		spec, err := oci.ParseMountSpec(m)
		if err != nil {
			return nil, nil, nil, writeback, err
		}
		mounts = append(mounts, spec)
	}

	for _, d := range opts.Devices {
		spec, err := oci.ParseDeviceSpec(d)
		if err != nil {
			return nil, nil, nil, writeback, err
		}
		mounts = append(mounts, spec.Mount)
		devices = append(devices, spec.Device)
		deviceRules = append(deviceRules, spec.Cgroup)
	}

	mounts = append(mounts,
		specs.Mount{Destination: "/etc/resolv.conf", Type: "bind", Source: oci.ResolvConfSource(), Options: []string{"rbind", "ro"}},
		specs.Mount{Destination: "/etc/hosts", Type: "bind", Source: "/etc/hosts", Options: []string{"rbind", "ro"}},
		specs.Mount{Destination: "/etc/ssl/certs", Type: "bind", Source: "/etc/ssl/certs", Options: []string{"rbind", "ro"}},
	)

	return mounts, devices, deviceRules, writeback, nil
}

// persistenceMounts mounts ~/.claude and ~/.claude.json read-write at the
// same paths inside the sandbox, creating them first on a first-ever run.
// Owner-only files (Claude Code keeps credentials at 0600) are shadowed
// rather than bind-mounted directly — see shadow.go.
func persistenceMounts(workDir string) (mounts []specs.Mount, writeback func() error, err error) {
	noop := func() error { return nil }

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, noop, fmt.Errorf("resolve home directory: %w", err)
	}

	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		return nil, noop, fmt.Errorf("create %s: %w", claudeDir, err)
	}
	dirMount, err := oci.ParseMountSpec(claudeDir)
	if err != nil {
		return nil, noop, err
	}
	mounts = append(mounts, dirMount)

	shadowMounts, dirWriteback, err := shadowRestrictedFiles(claudeDir, claudeDir, filepath.Join(workDir, "claude-shadow"))
	if err != nil {
		return nil, noop, err
	}
	mounts = append(mounts, shadowMounts...)

	claudeJSON := filepath.Join(home, ".claude.json")
	if _, err := os.Stat(claudeJSON); os.IsNotExist(err) {
		if err := os.WriteFile(claudeJSON, []byte("{}"), 0o600); err != nil {
			return nil, noop, fmt.Errorf("create %s: %w", claudeJSON, err)
		}
	}
	jsonShadowMount, jsonWriteback, err := shadowRestrictedFile(claudeJSON, claudeJSON, filepath.Join(workDir, "claude-shadow"))
	if err != nil {
		return nil, noop, err
	}
	if jsonShadowMount != nil {
		mounts = append(mounts, *jsonShadowMount)
	} else {
		fileMount, err := oci.ParseMountSpec(claudeJSON)
		if err != nil {
			return nil, noop, err
		}
		mounts = append(mounts, fileMount)
	}

	writeback = func() error {
		if err := dirWriteback(); err != nil {
			return err
		}
		return jsonWriteback()
	}
	return mounts, writeback, nil
}

// buildEnv starts from the host environment (so API keys and shell setup
// carry over), resets PATH to sandbox paths, and applies --env overrides
// last so they win.
func buildEnv(overrides []string) []string {
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

	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

func randomID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
