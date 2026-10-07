#!/bin/sh
# claudenboxen VM guest init (PID 1).
#
# Stays PID 1 for the whole session instead of exec'ing claude: a real
# kernel discards any signal PID 1 has no handler for, and claude doesn't
# install handlers for all of them. claude runs in its own session with the
# console as its controlling terminal (setsid --ctty), so ^C and friends
# reach it through the line discipline; PID 1's own session has no reliable
# controlling terminal this early in boot.
#
# No service manager is started; shutdown is a direct poweroff.
set -u

# util-linux's setsid/setpriv live in /usr/bin and must win over the
# BusyBox applets in /bin, which lack the options used below.
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

CFG=/run/cbx-config

# The initramfs mounts the root filesystem read-only.
mount -o remount,rw /

mountpoint -q /proc || mount -t proc proc /proc
mountpoint -q /sys || mount -t sysfs sysfs /sys
mountpoint -q /dev || mount -t devtmpfs devtmpfs /dev
mkdir -p /dev/pts /dev/shm /run
mountpoint -q /dev/pts || mount -t devpts devpts /dev/pts
mountpoint -q /dev/shm || mount -t tmpfs tmpfs /dev/shm
mountpoint -q /run || mount -t tmpfs tmpfs /run
mountpoint -q /tmp || mount -t tmpfs -o mode=1777 tmpfs /tmp

echo claudenboxen >/proc/sys/kernel/hostname
printf '127.0.0.1 localhost\n127.0.1.1 claudenboxen\n' >/etc/hosts

modprobe virtio_net 2>/dev/null
modprobe virtiofs 2>/dev/null
modprobe virtio_rng 2>/dev/null

# udev applies the distro's device ownership/mode rules plus the permissive
# ones installed by build.sh, and loads drivers for passed-through USB
# devices, so they're usable by the non-root session user.
/sbin/udevd --daemon >/dev/null 2>&1
udevadm trigger --action=add >/dev/null 2>&1

# Static SLIRP addressing; QEMU usermode networking always uses this scheme.
ip link set lo up
ip addr add 10.0.2.15/24 dev eth0
ip link set eth0 up
ip route add default via 10.0.2.2
i=0
while [ "$i" -lt 20 ]; do
	ip link show eth0 | grep -q LOWER_UP && break
	i=$((i + 1))
	sleep 0.1
done
echo "nameserver 10.0.2.3" >/etc/resolv.conf

mkdir -p "$CFG"
mount -t virtiofs cbx-config "$CFG"

# identity: "UID GID USERNAME HOME" (HOME last, so it may contain spaces).
read -r CBX_UID CBX_GID CBX_USER CBX_HOME <"$CFG/identity"
read -r CBX_CWD <"$CFG/cwd"

# setuid bits don't survive the unprivileged image build; sudo refuses to
# run without one.
chown 0:0 /usr/bin/sudo
chmod 4755 /usr/bin/sudo

# A real non-root account matching the host user. Host-side file I/O is
# done by virtiofsd as the real host user regardless of guest uid, so
# nothing here needs root. Device groups cover passed-through hardware;
# passwordless sudo keeps in-session privilege escalation available.
groups=""
for g in dialout audio video input usb plugdev render; do
	getent group "$g" >/dev/null 2>&1 && groups="$groups${groups:+,}$g"
done
getent group "$CBX_GID" >/dev/null 2>&1 || groupadd -g "$CBX_GID" "$CBX_USER"
useradd -M -u "$CBX_UID" -g "$CBX_GID" ${groups:+-G "$groups"} \
	-d "$CBX_HOME" -s /bin/bash "$CBX_USER"
mkdir -p /etc/sudoers.d
echo "$CBX_USER ALL=(ALL) NOPASSWD:ALL" >"/etc/sudoers.d/claudenboxen-$CBX_USER"
chmod 0440 "/etc/sudoers.d/claudenboxen-$CBX_USER"

# mounts: "TAG GUEST_PATH" per line, in order.
while read -r tag path; do
	[ -n "$tag" ] || continue
	mkdir -p "$path"
	mount -t virtiofs "$tag" "$path"
done <"$CFG/mounts"

# env: shell-quoted KEY=VALUE lines; this is claude's environment.
set -a
# shellcheck disable=SC1091
. "$CFG/env"
set +a

# winsize: "ROWS COLS". The host rewrites it on SIGWINCH; a serial console
# has no other way to learn about resizes. Setting the size on the console
# makes the kernel deliver SIGWINCH to claude's process group.
apply_winsize() {
	read -r rows cols <"$CFG/winsize" 2>/dev/null || return 0
	[ "${rows:-0}" -gt 0 ] 2>/dev/null && [ "${cols:-0}" -gt 0 ] 2>/dev/null || return 0
	stty rows "$rows" cols "$cols" </dev/console 2>/dev/null
}
apply_winsize
(
	last=$(cat "$CFG/winsize" 2>/dev/null)
	while sleep 0.5; do
		cur=$(cat "$CFG/winsize" 2>/dev/null)
		[ "$cur" = "$last" ] && continue
		last=$cur
		apply_winsize
	done
) &

# args: one argv element per line.
set --
while IFS= read -r arg; do
	set -- "$@" "$arg"
done <"$CFG/args"

udevadm settle --timeout=5 >/dev/null 2>&1
cd "$CBX_CWD" 2>/dev/null || cd /

setsid --ctty --wait setpriv --reuid="$CBX_UID" --regid="$CBX_GID" --init-groups -- "$@"
code=$?

echo "$code" >"$CFG/exitcode"
sync
# Keep the kernel's own power-down message off the session's terminal.
echo 0 >/proc/sys/kernel/printk
poweroff -f
