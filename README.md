# claudenboxen

Run [Claude Code](https://code.claude.com) inside a sandbox, so it can work
on a project with fewer guardrails without putting the rest of the machine
at risk. Two backends:

- **gVisor** (default) — a [gVisor](https://gvisor.dev) (`runsc`) sandbox.
  Needs nothing beyond `runsc` installed. Full filesystem and process
  isolation; `--dangerously-skip-permissions` does *not* work here (see
  [Known limitations](#known-limitations)).
- **VM** (`--isolation vm`) — a QEMU/KVM virtual machine with its own
  kernel. Needs QEMU, KVM, and virtiofsd. Adds real memory/CPU limits, USB
  device passthrough, network isolation by default, and a working
  `--dangerously-skip-permissions`.

In both, the target directory is mounted read-write at the same path inside
the sandbox (`claudenboxen ~/code/foo` puts you at `~/code/foo` there too),
and `~/.claude/` + `~/.claude.json` are mounted so login and session state
persist across runs.

## Install

### Arch Linux

[`packaging/arch/PKGBUILD`](packaging/arch/PKGBUILD) pulls in the
dependencies for both backends and installs the udev rule USB passthrough
needs, plus shell completions:

```sh
make arch-install          # or: cd packaging/arch && makepkg -si
```

### From source

```sh
go install github.com/jesse-osiecki/claudenboxen@latest
```

Or from a checkout: `make` builds `./claudenboxen` with the version from
`git describe`, `make local` installs it to `/usr/local/bin` ahead of the
packaged one, `make install` puts it in `$GOPATH/bin`, and `make test`
runs vet and the tests. See the header of the [`Makefile`](Makefile) for
the packaging targets.

Then install the prerequisites below for whichever backend you want.

### Prerequisites

Both backends:

- Linux, amd64 or arm64.
- [gVisor](https://gvisor.dev/docs/user_guide/install/) (`runsc`) on `PATH`.
  Arch: `paru -S gvisor-bin` (AUR). VM mode uses it only to build its disk
  image.

`--isolation vm` additionally:

- QEMU with KVM (`qemu-system-x86_64` or `qemu-system-aarch64`) and
  `qemu-img`. Arch: `pacman -S qemu-system-x86 qemu-img`. Debian/Ubuntu:
  `apt install qemu-system-x86 qemu-utils`.
- `/dev/kvm` readable and writable by your user (usually via the `kvm`
  group).
- `virtiofsd`. Arch: `pacman -S virtiofsd`. It's often installed at
  `/usr/lib/virtiofsd` or `/usr/libexec/virtiofsd` rather than on `PATH`;
  both locations are checked.
- e2fsprogs 1.43+ (`mke2fs -d`).
- For `--usb`: read/write access to the device's `/dev/bus/usb/...` node.
  The Arch package installs a udev rule granting this to the locally
  logged-in user; otherwise see [USB passthrough](#usb-passthrough).

`claudenboxen doctor` (or `doctor --isolation vm`) checks all of this,
including a real gVisor launch.

Don't `setcap` the `runsc` binary unless you've read
[Network modes](#network-modes): with `kernel.yama.ptrace_scope=1` (the
default on many distros) it breaks gVisor entirely.
`sudo setcap -r $(which runsc)` undoes it.

## Usage

```sh
claudenboxen                          # sandbox the current directory
claudenboxen ~/code/myproject         # sandbox a specific directory
claudenboxen . -- --model opus        # anything after -- goes to `claude`
claudenboxen --isolation vm .         # same, in a VM
claudenboxen --isolation vm --usb 0483:df11 .   # with a USB device
```

The first run of each backend builds and caches a base image: Alpine Linux
with Claude Code installed from Anthropic's signed apk repository (`latest`
channel), plus git, ripgrep, Node.js, and a few other tools; the VM image
also carries Alpine's `linux-virt` kernel. That downloads around 100 MB
and takes a minute or two. Later runs start in seconds.

Inside, it's a normal interactive `claude` session with your usual auth and
settings. The first time in a given directory you'll see Claude Code's own
"Is this a project you trust?" prompt, same as locally.

### Flags

| Flag | Meaning |
|---|---|
| `--isolation gvisor\|vm` | Backend. Default `gvisor`. |
| `--mount SRC[:DST[:ro\|rw]]` | Extra file/dir to mount, repeatable. `DST` defaults to `SRC`. |
| `--device SRC[:DST]` | gVisor only. Host device node to pass through, repeatable. Best-effort — see [Known limitations](#known-limitations). |
| `--usb VID:PID` | VM only. USB device to pass through, repeatable; IDs as `lsusb` prints them. |
| `--env KEY=VALUE` | Extra/override env var, repeatable. |
| `--no-persist` | Skip the `~/.claude` + `~/.claude.json` mount. |
| `--network host\|sandbox\|none` | gVisor default `host` (see [Network modes](#network-modes)). VM default is NAT; `host` isn't available there. |
| `--rebuild` | Force a base image rebuild. |
| `--runsc-path PATH` | Override the `runsc` location. |
| `--vm-memory MiB` | VM only. Guest memory, default `4096`. |
| `--vm-cpus N` | VM only. Guest vCPUs, default `4`. |

```sh
claudenboxen clean    # remove cached base images other than the current ones
claudenboxen doctor   # prerequisite report
```

Shell completion is built in: `source <(claudenboxen completion bash)` or
`zsh`; see `claudenboxen completion --help` for fish and PowerShell.

### Base image pinning

The Alpine release and rootfs checksums are pinned in
`internal/image/alpine_pin.go`, so builds are reproducible and verified.
To move to the current stable release:

```sh
make generate                      # rewrites alpine_pin.go from Alpine's release metadata
go run ./tools/update-alpine -branch v3.23   # or pin a specific branch
```

Any change to the pin, the package list, or the build scripts changes the
recipe hash, and the next run rebuilds the image automatically. Claude Code
itself isn't pinned: the image installs whatever the `latest` apk channel
serves at build time, and Claude Code's own updater keeps it current within
the session.

## Network modes

This section applies to `--isolation gvisor`.

`--network` defaults to **`host`**: the sandboxed process shares the host's
network stack (no network-namespace isolation), while filesystem and
process isolation stay intact. The more isolated option doesn't work
unprivileged on many systems:

- **`sandbox`** gives the sandbox its own network namespace (NAT'd egress,
  no inbound). Creating it needs `CAP_NET_ADMIN`/`CAP_NET_RAW`. The obvious
  way to grant that — `setcap` on `runsc` — **breaks the sandbox outright**
  on hosts with Yama's ptrace restriction enabled
  (`kernel.yama.ptrace_scope >= 1`, a common default): file capabilities
  make gVisor's internal self-reexec look like a privilege-elevating exec,
  and the kernel then refuses to let the parent ptrace it. `doctor` detects
  this failure and says so. Your options:
  - `sudo claudenboxen ...` (cache files end up root-owned; `sudo chown -R
    $SUDO_UID:$SUDO_GID ~/.cache/claudenboxen` to go back), or
  - `sudo setcap cap_net_admin,cap_net_raw+eip $(which runsc)` on a system
    where `kernel.yama.ptrace_scope` is `0`.
- **`none`** — no network at all. Useful for testing, not for `claude`.

## VM isolation mode

`--isolation vm` boots `claude` inside a QEMU/KVM guest with its own
kernel. Compared to the gVisor backend:

- **Network isolation by default.** QEMU's usermode networking (SLIRP) is
  NAT'd, inbound-closed, and needs no host privilege. `--network host`
  isn't available: a VM can't share the host's network namespace, and the
  alternative (a TAP device and bridge) needs root. `--network none` works.
- **Real resource limits** via `--vm-memory` and `--vm-cpus`.
- **USB device passthrough** via `--usb VID:PID` — see below.
- **`--dangerously-skip-permissions` works.** `claude` runs as a real
  non-root user synthesized from your host identity at boot (with
  passwordless `sudo` in the guest), so Claude Code's root check doesn't
  trigger.
- **Window resizes propagate.** The guest console is a serial UART with no
  resize signal, so the host publishes the terminal size through the
  control share and the guest applies it.

The VM image is built through the same unprivileged gVisor sandbox the
gVisor image uses (`apk add` as root into a plain directory, then `mke2fs
-d` to turn that directory into an ext4 image), which is why `runsc` is
still required. It's cached separately from the gVisor image, since it
includes a kernel and initramfs.

### USB passthrough

```sh
claudenboxen --isolation vm --usb 0483:df11 .
```

The device is detached from the host and attached to the guest's xHCI
controller for the session; matching is by vendor/product ID, so a device
that re-enumerates (a board being flashed, say) reattaches automatically.
Inside the guest, udev runs with the distro's rules plus permissive ones
for raw USB, serial (`ttyUSB*`/`ttyACM*`), and HID nodes, and the session
user is in `dialout`, `usb`, `video`, `audio`, and `input`, so the device
is usable without `sudo`.

On the host, QEMU needs read/write access to `/dev/bus/usb/BUS/DEV`, which
device nodes don't grant by default. The Arch package installs
[`70-claudenboxen.rules`](packaging/udev/70-claudenboxen.rules), which
grants the locally logged-in user access to all non-hub USB devices.
Without it, `claudenboxen` checks access before booting and prints the
per-device udev rule to add. There's no PCI/VFIO passthrough.

### Limitations specific to VM mode

- No `--network host`; no PCI/VFIO passthrough (see above).
- `doctor --isolation vm` checks prerequisites but doesn't boot a VM; the
  first real run is the boot test.

## Known limitations

These apply to the default gVisor backend.

- **`--dangerously-skip-permissions` doesn't work.** The sandboxed process
  runs as uid 0 (see [Why fake-root](#why-fake-root)), and Claude Code
  refuses that flag as root unless it recognizes the sandbox. Neither
  gVisor's `/proc/gvisor/kernel_is_gvisor` marker nor the `container=` env
  convention satisfies that check. Works under `--isolation vm`.
- **No egress allowlisting, in any network mode.** `host` has no network
  isolation at all; `sandbox` is plain NAT. Anthropic's
  [`sandbox-runtime`](https://github.com/anthropic-experimental/sandbox-runtime)
  and the reference [dev container](https://code.claude.com/docs/en/devcontainer)
  use a domain-allowlisted proxy instead; that's the natural next step and
  isn't built.
- **No resource limits.** `runsc --rootless` ignores OCI memory/pids limits
  without cgroup delegation, which needs root or gVisor's experimental
  `--systemd-cgroup`. Real limits are available under `--isolation vm`.
- **Device passthrough is best-effort.** gVisor emulates common devices
  (ttys, `/dev/null`, `/dev/urandom`, FUSE) rather than exposing hardware,
  so `--device` may not work for exotic ones. Real USB passthrough is
  available under `--isolation vm`.
- **Nested sandboxing.** If Claude Code's own bubblewrap Bash sandbox is
  enabled in your persisted settings, it will try to activate inside the
  gVisor sandbox. The image ships `bubblewrap` and `socat` so it can; if it
  fails to mount `/proc`, set
  [`enableWeakerNestedSandbox`](https://code.claude.com/docs/en/sandboxing#troubleshooting).
- **The image is Alpine (musl).** Project toolchains that assume glibc
  (prebuilt binaries, some npm native modules) may need `apk add gcompat`
  or a musl build. Claude Code itself ships a native musl binary.
- **Mounting `~/.claude` means mounting credentials.** That's the point of
  the default, but don't also `--mount` `~/.ssh` or cloud credentials
  unless you mean to expose them to the session.

## How it works

The gVisor backend drives `runsc` directly as an OCI runtime — no
Docker/Podman. `internal/oci` builds the bundle, `internal/runtime` invokes
`runsc`, `internal/image` builds and caches the rootfs, `internal/run` ties
them together.

The VM backend is a parallel stack: `internal/vmimage` builds and caches
the disk image, kernel, and initramfs; `internal/vmspec` writes the per-run
control files (mounts, env, argv, identity, console size) that the guest's
PID 1 (`build/vm-recipe/init.sh`) reads from a virtiofs share at boot;
`internal/vmrun` runs virtiofsd and QEMU.

### Why fake-root

Under the gVisor backend the sandboxed process reports uid 0 rather than
your uid. Rootless gVisor only permits creating files inside a bind-mounted
directory when the sandboxed uid is 0 — a matching non-zero uid gets
`EACCES`, even with `CAP_DAC_OVERRIDE`. This is safe because the sentry
never has real root: all host file I/O is performed by the unprivileged
user who launched `runsc`, so files created in a session land on disk owned
by you at your normal umask. `internal/oci/identity.go` synthesizes an
`/etc/passwd` entry for your real user so `getpwuid`-style lookups resolve
correctly regardless.

Two more rootless-gVisor quirks are handled in `internal/run/shadow.go`: a
bind-mounted file with no "other" read bit (mode 0600 — what Claude Code
uses for `~/.claude.json` and its OAuth token) returns `EACCES` even to its
owner, so such files are shadow-copied in with relaxed permissions and
copied back out after the session; and directory listings of bind mounts
report `root:root` ownership regardless of the real owner.
