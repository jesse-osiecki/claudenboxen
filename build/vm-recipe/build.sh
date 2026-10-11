#!/bin/sh
# Runs inside the gVisor build sandbox as root, after alpine-recipe/build.sh
# has provisioned the rootfs (see internal/vmimage/build.go). Adds what the
# VM needs to boot under QEMU: an initramfs that can mount a virtio root,
# the guest init script, and device permission rules for passthrough.
set -eu

# linux-virt builds virtio_blk and ext4 as modules, so the initramfs has
# to carry them.
kver=$(basename /lib/modules/*)
echo 'features="base virtio ext4"' >/etc/mkinitfs/mkinitfs.conf
mkinitfs -b / -o /boot/initramfs-virt "$kver"

mkdir -p /opt/claudenboxen
mv /init.sh /opt/claudenboxen/init
chmod 755 /opt/claudenboxen/init

# lsusb and udev's own device naming resolve vendor/product names through
# the compiled hwdb.
udevadm hwdb --update

# The guest is disposable and single-user: anything passed through is
# meant for the session user, so raw USB, serial, and HID nodes are opened
# up beyond the distro defaults (which only cover group-owned classes).
mkdir -p /etc/udev/rules.d
cat >/etc/udev/rules.d/90-claudenboxen.rules <<'EOF'
SUBSYSTEM=="usb", ENV{DEVTYPE}=="usb_device", MODE="0666"
SUBSYSTEM=="tty", KERNEL=="ttyUSB*|ttyACM*", MODE="0666"
SUBSYSTEM=="hidraw", MODE="0666"
EOF

echo "build-vm.sh: done"
