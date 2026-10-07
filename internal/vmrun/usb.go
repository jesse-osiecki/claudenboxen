package vmrun

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// USBDevice is one --usb VID:PID entry, lowercase hex without a 0x prefix.
type USBDevice struct {
	VendorID  string
	ProductID string
}

func (d USBDevice) String() string { return d.VendorID + ":" + d.ProductID }

var usbIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{4}$`)

// ParseUSBSpec parses "VID:PID" as lsusb prints it, e.g. "046d:c52b".
func ParseUSBSpec(s string) (USBDevice, error) {
	vendor, product, ok := strings.Cut(s, ":")
	if !ok || !usbIDPattern.MatchString(vendor) || !usbIDPattern.MatchString(product) {
		return USBDevice{}, fmt.Errorf("invalid --usb spec %q: expected VID:PID as 4 hex digits each, e.g. 046d:c52b (see `lsusb`)", s)
	}
	return USBDevice{VendorID: strings.ToLower(vendor), ProductID: strings.ToLower(product)}, nil
}

// CheckUSBAccess verifies that d is currently attached and that its
// /dev/bus/usb node is readable and writable by this process. QEMU's own
// failure in either case is a bare libusb error after the guest has
// already started booting; this turns it into an up-front error with the
// udev rule needed to fix it.
func CheckUSBAccess(d USBDevice) error {
	nodes, err := usbDeviceNodes(d)
	if err != nil {
		return err
	}
	if len(nodes) == 0 {
		return fmt.Errorf("USB device %s is not attached (check `lsusb`)", d)
	}
	for _, node := range nodes {
		if err := unix.Access(node, unix.R_OK|unix.W_OK); err != nil {
			return fmt.Errorf("USB device %s (%s) is not accessible by your user: %w\n\n"+
				"Grant access with a udev rule, then replug the device (or `sudo udevadm trigger`):\n\n"+
				"  echo 'SUBSYSTEM==\"usb\", ATTR{idVendor}==\"%s\", ATTR{idProduct}==\"%s\", TAG+=\"uaccess\"' \\\n"+
				"    | sudo tee -a /etc/udev/rules.d/70-claudenboxen-usb.rules\n"+
				"  sudo udevadm control --reload\n\n"+
				"TAG+=\"uaccess\" grants the user logged in at the local seat; for a headless host use\n"+
				"MODE=\"0660\", GROUP=\"<group>\" instead and add yourself to that group.",
				d, node, err, d.VendorID, d.ProductID)
		}
	}
	return nil
}

// usbDeviceNodes maps a VID:PID to its /dev/bus/usb/BBB/DDD node(s) via sysfs.
func usbDeviceNodes(d USBDevice) ([]string, error) {
	entries, err := filepath.Glob("/sys/bus/usb/devices/*")
	if err != nil {
		return nil, err
	}
	var nodes []string
	for _, dir := range entries {
		if sysfsAttr(dir, "idVendor") != d.VendorID || sysfsAttr(dir, "idProduct") != d.ProductID {
			continue
		}
		bus, err1 := strconv.Atoi(sysfsAttr(dir, "busnum"))
		dev, err2 := strconv.Atoi(sysfsAttr(dir, "devnum"))
		if err1 != nil || err2 != nil {
			continue
		}
		nodes = append(nodes, fmt.Sprintf("/dev/bus/usb/%03d/%03d", bus, dev))
	}
	return nodes, nil
}

func sysfsAttr(dir, name string) string {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(string(data)))
}
