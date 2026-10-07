// Package cmd implements the CLI (cobra). It parses flags and delegates to
// internal/run, internal/vmrun, and internal/prereq.
package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/jesse-osiecki/claudenboxen/internal/prereq"
	"github.com/jesse-osiecki/claudenboxen/internal/run"
	"github.com/jesse-osiecki/claudenboxen/internal/vmrun"
)

// version is set via -ldflags "-X .../cmd.version=..." at release build time.
var version = "dev"

var (
	flagMounts    []string
	flagDevices   []string
	flagEnv       []string
	flagNoPersist bool
	flagNetwork   string
	flagRebuild   bool
	flagRunscPath string

	flagIsolation string
	flagUSB       []string
	flagVMMemory  int
	flagVMCPUs    int
)

var rootCmd = &cobra.Command{
	Use:     "claudenboxen [path] [-- claude-args...]",
	Short:   "Run Claude Code inside a gVisor sandbox or a QEMU/KVM virtual machine",
	Version: version,
	Long: `claudenboxen runs Claude Code inside a sandbox: it can read and write
your project and reach the network, but not the rest of your machine.

The target directory (default ".") is mounted read-write at the same path
inside the sandbox. ~/.claude and ~/.claude.json are mounted too, so login
and session state persist across runs. Anything after -- is passed to the
claude CLI itself:

  claudenboxen ~/code/myproject -- --model opus

Isolation backends (--isolation):

  gvisor (default)  gVisor (runsc) sandbox. Needs only runsc installed.
                    --dangerously-skip-permissions does not work here
                    (see README).
  vm                QEMU/KVM virtual machine. Needs qemu, /dev/kvm access,
                    and virtiofsd. Real memory/CPU limits, USB passthrough
                    (--usb), NAT'd network isolation by default, and a
                    working --dangerously-skip-permissions.`,
	Args:         cobra.ArbitraryArgs,
	SilenceUsage: true,
	RunE:         runRoot,
}

func runRoot(cmd *cobra.Command, args []string) error {
	path := "."
	var claudeArgs []string

	if dash := cmd.ArgsLenAtDash(); dash >= 0 {
		if dash > 1 {
			return fmt.Errorf("only one path argument is allowed before --, got %d", dash)
		}
		if dash == 1 {
			path = args[0]
		}
		claudeArgs = args[dash:]
	} else if len(args) > 0 {
		if len(args) > 1 {
			return fmt.Errorf("unexpected extra arguments %v (use -- to pass args to claude)", args[1:])
		}
		path = args[0]
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve %q: %w", path, err)
	}
	if info, err := os.Stat(absPath); err != nil {
		return fmt.Errorf("%s: %w", absPath, err)
	} else if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", absPath)
	}

	if _, err := prereq.QuickCheck(flagRunscPath); err != nil {
		return fmt.Errorf("%w\n\nRun `claudenboxen doctor` for a full prerequisite report.", err)
	}

	// The flag default "host" is gVisor-specific; VM mode has no host
	// networking, so an unset --network means NAT there. An explicit
	// --network host still reaches vmrun.Run's error.
	if flagIsolation == "vm" && !cmd.Flags().Changed("network") {
		flagNetwork = "sandbox"
	}

	var exitCode int
	switch flagIsolation {
	case "gvisor":
		if len(flagUSB) > 0 {
			return fmt.Errorf("--usb requires --isolation vm (gVisor emulates devices rather than passing USB hardware through)")
		}
		exitCode, err = run.Run(cmd.Context(), run.Options{
			Path:       absPath,
			ClaudeArgs: claudeArgs,
			Mounts:     flagMounts,
			Devices:    flagDevices,
			Env:        flagEnv,
			NoPersist:  flagNoPersist,
			Network:    flagNetwork,
			Rebuild:    flagRebuild,
			RunscPath:  flagRunscPath,
		})
	case "vm":
		if len(flagDevices) > 0 {
			return fmt.Errorf("--device requires --isolation gvisor; with --isolation vm use --usb VID:PID instead")
		}
		exitCode, err = vmrun.Run(cmd.Context(), vmrun.Options{
			Path:       absPath,
			ClaudeArgs: claudeArgs,
			Mounts:     flagMounts,
			USB:        flagUSB,
			Env:        flagEnv,
			NoPersist:  flagNoPersist,
			Network:    flagNetwork,
			Rebuild:    flagRebuild,
			MemoryMB:   flagVMMemory,
			CPUs:       flagVMCPUs,
		})
	default:
		return fmt.Errorf("--isolation must be gvisor or vm, got %q", flagIsolation)
	}
	if err != nil {
		return err
	}
	if exitCode != 0 {
		os.Exit(exitCode)
	}
	return nil
}

func init() {
	flags := rootCmd.Flags()
	flags.StringArrayVar(&flagMounts, "mount", nil, "extra file/dir to mount: SRC[:DST[:ro|rw]] (repeatable)")
	flags.StringArrayVar(&flagDevices, "device", nil, "host device node to pass through (--isolation gvisor only): SRC[:DST] (repeatable)")
	flags.StringArrayVar(&flagEnv, "env", nil, "extra/override env var: KEY=VALUE (repeatable)")
	flags.BoolVar(&flagNoPersist, "no-persist", false, "skip the default ~/.claude + ~/.claude.json mount")
	flags.BoolVar(&flagRebuild, "rebuild", false, "force a base image rebuild")
	flags.StringArrayVar(&flagUSB, "usb", nil, "USB device to pass through (--isolation vm only): VID:PID as shown by lsusb (repeatable)")
	flags.IntVar(&flagVMMemory, "vm-memory", 4096, "--isolation vm guest memory, in MiB")
	flags.IntVar(&flagVMCPUs, "vm-cpus", 4, "--isolation vm guest vCPU count")

	// Persistent so `doctor` reports against the same settings.
	//
	// --network defaults to host for gVisor because an isolated netns needs
	// CAP_NET_ADMIN/CAP_NET_RAW, and granting those to runsc via setcap
	// breaks it on hosts with Yama ptrace restrictions (see README).
	persistent := rootCmd.PersistentFlags()
	persistent.StringVar(&flagNetwork, "network", "host", "network mode: host|sandbox|none (gvisor default: host; vm: host unsupported, default is NAT)")
	persistent.StringVar(&flagRunscPath, "runsc-path", "", "override the runsc binary location")
	persistent.StringVar(&flagIsolation, "isolation", "gvisor", "isolation backend: gvisor|vm")

	rootCmd.AddCommand(doctorCmd, cleanCmd)
}

// Execute is the entrypoint main.go calls.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "claudenboxen:", err)
		os.Exit(1)
	}
}
