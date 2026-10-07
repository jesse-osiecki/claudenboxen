package vmimage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	specs "github.com/opencontainers/runtime-spec/specs-go"

	alpinerecipe "github.com/jesse-osiecki/claudenboxen/build/alpine-recipe"
	vmrecipe "github.com/jesse-osiecki/claudenboxen/build/vm-recipe"
	"github.com/jesse-osiecki/claudenboxen/internal/image"
	"github.com/jesse-osiecki/claudenboxen/internal/oci"
	"github.com/jesse-osiecki/claudenboxen/internal/runtime"
)

// diskSizeBytes is the ext4 base image's size: room for the userland, a
// kernel and modules, claude, and whatever a session installs into its
// overlay. The file is sparse, so unused space costs nothing on disk.
const diskSizeBytes = 8 << 30

const ext4BlockSize = 4096

// BuildEnv is what the image build needs to invoke runsc. The VM image is
// provisioned through the same gVisor sandbox as the gVisor image; running
// the VM never touches runsc.
type BuildEnv struct {
	RunscBin string
	StateDir string
	Rootless bool
}

// EnsureImage returns a ready-to-use VM image, building one if none is
// cached, the cached one's recipe hash doesn't match, or rebuild is set.
func EnsureImage(ctx context.Context, env BuildEnv, rebuild bool, out io.Writer) (Image, error) {
	hash, err := RecipeHash()
	if err != nil {
		return Image{}, err
	}

	if !rebuild {
		if img, current, err := CurrentImage(); err == nil {
			if meta, mErr := ReadMeta(current); mErr == nil && meta.RecipeHash == hash {
				return img, nil
			}
			fmt.Fprintf(out, "cached VM image is stale relative to the current recipe, rebuilding...\n")
		} else if err != ErrNoImage {
			return Image{}, err
		}
	}

	return buildImage(ctx, env, hash, out)
}

func buildImage(ctx context.Context, env BuildEnv, hash string, out io.Writer) (Image, error) {
	arch := image.HostArch()

	cacheDir, err := rootCacheDir()
	if err != nil {
		return Image{}, err
	}
	tmpParent := filepath.Join(cacheDir, "tmp")
	if err := os.MkdirAll(tmpParent, 0o755); err != nil {
		return Image{}, fmt.Errorf("create %s: %w", tmpParent, err)
	}
	workDir, err := os.MkdirTemp(tmpParent, "build-"+hash+"-")
	if err != nil {
		return Image{}, err
	}
	defer os.RemoveAll(workDir)

	rootfsDir := filepath.Join(workDir, "rootfs")
	if err := image.PrepareRootfs(DefaultRecipe, arch, workDir, rootfsDir, out); err != nil {
		return Image{}, err
	}

	// Stage 1: the shared package provisioning, with this package's
	// expanded package list.
	buildScriptPath := filepath.Join(rootfsDir, "build.sh")
	if err := os.WriteFile(buildScriptPath, []byte(alpinerecipe.BuildScript), 0o755); err != nil {
		return Image{}, fmt.Errorf("write build.sh: %w", err)
	}
	fmt.Fprintf(out, "installing packages, kernel, and claude...\n")
	if err := runProvisionStep(ctx, env, rootfsDir, []string{"/bin/sh", "/build.sh"}, image.BuildScriptEnv(DefaultRecipe), image.DNSMounts(), out); err != nil {
		return Image{}, err
	}
	os.Remove(buildScriptPath)

	// Stage 2: VM-specific finishing (initramfs, init script, udev rules).
	vmBuildScriptPath := filepath.Join(rootfsDir, "build-vm.sh")
	if err := os.WriteFile(vmBuildScriptPath, []byte(vmrecipe.BuildScript), 0o755); err != nil {
		return Image{}, fmt.Errorf("write build-vm.sh: %w", err)
	}
	initScriptPath := filepath.Join(rootfsDir, "init.sh")
	if err := os.WriteFile(initScriptPath, []byte(vmrecipe.InitScript), 0o755); err != nil {
		return Image{}, fmt.Errorf("write init.sh: %w", err)
	}
	fmt.Fprintf(out, "preparing VM boot (initramfs, init script)...\n")
	if err := runProvisionStep(ctx, env, rootfsDir, []string{"/bin/sh", "/build-vm.sh"}, image.BuildScriptEnv(DefaultRecipe)[:2], nil, out); err != nil {
		return Image{}, err
	}
	os.Remove(vmBuildScriptPath)
	os.Remove(initScriptPath)

	kernelSrc := filepath.Join(rootfsDir, "boot", "vmlinuz-virt")
	initrdSrc := filepath.Join(rootfsDir, "boot", "initramfs-virt")
	for _, p := range []string{kernelSrc, initrdSrc} {
		if _, err := os.Stat(p); err != nil {
			return Image{}, fmt.Errorf("built rootfs is missing %s: %w", filepath.Base(p), err)
		}
	}

	finalDir, err := imageDir(hash)
	if err != nil {
		return Image{}, err
	}
	os.RemoveAll(finalDir) // a previous failed build may have left a partial one
	if err := os.MkdirAll(finalDir, 0o755); err != nil {
		return Image{}, err
	}
	final, err := imageAt(hash)
	if err != nil {
		return Image{}, err
	}

	if err := copyFile(kernelSrc, final.Kernel); err != nil {
		return Image{}, fmt.Errorf("copy kernel: %w", err)
	}
	if err := copyFile(initrdSrc, final.Initrd); err != nil {
		return Image{}, fmt.Errorf("copy initramfs: %w", err)
	}

	fmt.Fprintf(out, "building disk image...\n")
	if err := buildDiskImage(ctx, env, rootfsDir, final.Disk, diskSizeBytes, out); err != nil {
		return Image{}, fmt.Errorf("build disk image: %w", err)
	}

	if err := writeMeta(hash, Meta{
		RecipeHash: hash,
		Arch:       arch,
		BuiltAt:    time.Now(),
	}); err != nil {
		return Image{}, err
	}
	if err := setCurrent(hash); err != nil {
		return Image{}, err
	}

	fmt.Fprintf(out, "VM image built: %s\n", hash)
	return final, nil
}

// runProvisionStep runs args as uid 0 inside a throwaway, non-overlaid
// gVisor sandbox rooted at rootfsDir, with host networking (see
// internal/image.runBuildScript).
func runProvisionStep(ctx context.Context, env BuildEnv, rootfsDir string, args, procEnv []string, extraMounts []specs.Mount, out io.Writer) error {
	spec := oci.BuildSpec(oci.Config{
		RootfsPath:  rootfsDir,
		Hostname:    "claudenboxen-vm-build",
		Terminal:    false,
		Args:        args,
		Env:         procEnv,
		Cwd:         "/",
		UID:         0,
		GID:         0,
		HostNetwork: true,
		ExtraMounts: extraMounts,
	})

	bundleDir := filepath.Join(rootfsDir, "..", "build-bundle")
	if _, err := oci.WriteBundle(bundleDir, spec); err != nil {
		return err
	}

	runner := &runtime.Runner{
		BinPath:  env.RunscBin,
		StateDir: env.StateDir,
		Network:  "host",
		Overlay:  "none",
		Rootless: env.Rootless,
	}
	// A fresh container ID per step: runsc only cleans up a foreground
	// run's state on normal exit, so an interrupted build would otherwise
	// leave a stale container blocking the next one.
	containerID, err := randomContainerID()
	if err != nil {
		return err
	}
	cmd := runner.RunCommand(ctx, bundleDir, containerID, "")
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", strings.Join(args, " "), err)
	}
	return nil
}

func randomContainerID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "claudenboxen-vm-build-" + hex.EncodeToString(buf), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// buildDiskImage builds an ext4 image from rootfsDir with `mke2fs -d`, so
// no loop device or root is needed.
//
// mke2fs runs inside the gVisor sandbox rather than on the host: rootless
// gVisor can't chown host files, so on disk every file in rootfsDir is
// owned by the real user, and the sandbox is the only place where stat()
// reports them as uid 0. The image therefore gets root ownership for
// everything, which is what the base image wants. setuid bits still don't
// survive this path; init.sh restores sudo's at boot.
//
// rootfsDir is bind-mounted a second time at /rootfs-src so the sandbox's
// own live /proc, /sys, /dev, and /tmp mounts aren't imaged.
func buildDiskImage(ctx context.Context, env BuildEnv, rootfsDir, destPath string, sizeBytes int64, out io.Writer) error {
	blocks := sizeBytes / ext4BlockSize

	outputDir := filepath.Join(rootfsDir, "..", "disk-output")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}

	args := []string{
		"mke2fs",
		"-F",
		"-q",
		"-t", "ext4",
		"-b", strconv.Itoa(ext4BlockSize),
		"-d", "/rootfs-src",
		"/output/disk.img",
		strconv.FormatInt(blocks, 10),
	}
	extraMounts := []specs.Mount{
		{Destination: "/rootfs-src", Type: "bind", Source: rootfsDir, Options: []string{"rbind", "ro"}},
		{Destination: "/output", Type: "bind", Source: outputDir, Options: []string{"rbind", "rw"}},
	}
	if err := runProvisionStep(ctx, env, rootfsDir, args, image.BuildScriptEnv(DefaultRecipe)[:2], extraMounts, out); err != nil {
		return err
	}

	return os.Rename(filepath.Join(outputDir, "disk.img"), destPath)
}
