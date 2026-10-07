// Package vmimage builds and caches the VM backend's base disk: the same
// Alpine userland and claude CLI as internal/image's gVisor rootfs, plus a
// kernel and the guest init script. It's a separate cached image so the
// gVisor image doesn't carry a kernel it never uses.
package vmimage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	vmrecipe "github.com/jesse-osiecki/claudenboxen/build/vm-recipe"
	"github.com/jesse-osiecki/claudenboxen/internal/image"
)

// DefaultRecipe is internal/image.DefaultRecipe plus what the VM needs: the
// virtio-oriented kernel (which pulls in mkinitfs), udev, sudo, usbutils,
// useradd, util-linux's setsid and setpriv (the BusyBox applets lack the
// options init.sh needs), iproute2, and e2fsprogs (mke2fs runs inside the
// build sandbox).
var DefaultRecipe = func() image.Recipe {
	r := image.DefaultRecipe
	r.Packages = append(append([]string{}, image.DefaultRecipe.Packages...),
		"linux-virt",
		"eudev",
		"sudo",
		"usbutils",
		"shadow",
		"util-linux-misc",
		"setpriv",
		"iproute2",
		"e2fsprogs",
	)
	return r
}()

// RecipeHash fingerprints everything that affects the built VM image: the
// base recipe hash (with this package's package list) plus the VM-specific
// build and init scripts.
func RecipeHash() (string, error) {
	base, err := DefaultRecipe.Hash(image.HostArch())
	if err != nil {
		return "", err
	}

	h := sha256.New()
	fmt.Fprintf(h, "base=%s\n", base)
	fmt.Fprintf(h, "vm_build_script_sha256=%x\n", sha256.Sum256([]byte(vmrecipe.BuildScript)))
	fmt.Fprintf(h, "vm_init_script_sha256=%x\n", sha256.Sum256([]byte(vmrecipe.InitScript)))
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}
