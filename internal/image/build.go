package image

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	specs "github.com/opencontainers/runtime-spec/specs-go"

	recipescript "github.com/jesse-osiecki/claudenboxen/build/alpine-recipe"
	"github.com/jesse-osiecki/claudenboxen/internal/oci"
	"github.com/jesse-osiecki/claudenboxen/internal/runtime"
)

// BuildEnv is what the image build needs to invoke runsc. Network and
// overlay settings aren't here: the build always uses its own fixed ones.
type BuildEnv struct {
	RunscBin string
	StateDir string
	Rootless bool
}

// EnsureImage returns the path to a ready-to-use rootfs, building one if
// none is cached, the cached one's recipe hash doesn't match DefaultRecipe,
// or rebuild is set.
func EnsureImage(ctx context.Context, env BuildEnv, rebuild bool, out io.Writer) (rootfsPath string, err error) {
	hash, err := DefaultRecipe.Hash(HostArch())
	if err != nil {
		return "", err
	}

	if !rebuild {
		if path, current, err := CurrentImage(); err == nil {
			if meta, mErr := ReadMeta(current); mErr == nil && meta.RecipeHash == hash {
				return path, nil
			}
			fmt.Fprintf(out, "cached image is stale relative to the current recipe, rebuilding...\n")
		} else if err != ErrNoImage {
			return "", err
		}
	}

	return buildImage(ctx, env, hash, out)
}

func buildImage(ctx context.Context, env BuildEnv, hash string, out io.Writer) (rootfsPath string, err error) {
	arch := HostArch()

	cacheDir, err := CacheDir()
	if err != nil {
		return "", err
	}
	tmpParent := filepath.Join(cacheDir, "tmp")
	if err := os.MkdirAll(tmpParent, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", tmpParent, err)
	}
	workDir, err := os.MkdirTemp(tmpParent, "build-"+hash+"-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(workDir)

	rootfsDir := filepath.Join(workDir, "rootfs")
	if err := PrepareRootfs(DefaultRecipe, arch, workDir, rootfsDir, out); err != nil {
		return "", err
	}

	buildScriptPath := filepath.Join(rootfsDir, "build.sh")
	if err := os.WriteFile(buildScriptPath, []byte(recipescript.BuildScript), 0o755); err != nil {
		return "", fmt.Errorf("write build.sh: %w", err)
	}

	fmt.Fprintf(out, "installing packages and claude...\n")
	if err := runBuildScript(ctx, env, rootfsDir, DefaultRecipe, out); err != nil {
		return "", err
	}

	os.Remove(buildScriptPath)

	finalDir, err := imageDir(hash)
	if err != nil {
		return "", err
	}
	os.RemoveAll(finalDir) // a previous failed build may have left a partial one
	if err := os.MkdirAll(finalDir, 0o755); err != nil {
		return "", err
	}
	finalRootfs := filepath.Join(finalDir, "rootfs")
	if err := os.Rename(rootfsDir, finalRootfs); err != nil {
		return "", fmt.Errorf("finalize image %s: %w", hash, err)
	}

	if err := writeMeta(hash, Meta{
		RecipeHash:    hash,
		Arch:          arch,
		BuiltAt:       time.Now(),
		AlpineVersion: DefaultRecipe.AlpineVersion,
	}); err != nil {
		return "", err
	}
	if err := setCurrent(hash); err != nil {
		return "", err
	}

	fmt.Fprintf(out, "image built: %s\n", hash)
	return finalRootfs, nil
}

// PrepareRootfs downloads and extracts the recipe's Alpine minirootfs into
// rootfsDir, ready for a build script to run against. Shared with
// internal/vmimage.
func PrepareRootfs(r Recipe, goarch, workDir, rootfsDir string, out io.Writer) error {
	asset, err := r.RootfsAsset(goarch)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "downloading Alpine %s rootfs (%s)...\n", r.AlpineVersion, asset.URL)
	tarPath := filepath.Join(workDir, "rootfs.tar.gz")
	if err := DownloadVerified(asset.URL, asset.SHA256, tarPath); err != nil {
		return err
	}

	if err := os.MkdirAll(rootfsDir, 0o755); err != nil {
		return err
	}
	fmt.Fprintf(out, "extracting rootfs...\n")
	if err := ExtractTar(tarPath, rootfsDir, 0); err != nil {
		return fmt.Errorf("extract rootfs: %w", err)
	}

	// Mount points for the build sandbox's DNS bind mounts; the minirootfs
	// doesn't ship a resolv.conf.
	for _, name := range []string{"resolv.conf", "hosts"} {
		p := filepath.Join(rootfsDir, "etc", name)
		if _, err := os.Stat(p); os.IsNotExist(err) {
			if err := os.WriteFile(p, nil, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// DNSMounts are the read-only bind mounts that give a build sandbox the
// host's DNS configuration.
func DNSMounts() []specs.Mount {
	return []specs.Mount{
		{Destination: "/etc/resolv.conf", Type: "bind", Source: oci.ResolvConfSource(), Options: []string{"rbind", "ro"}},
		{Destination: "/etc/hosts", Type: "bind", Source: "/etc/hosts", Options: []string{"rbind", "ro"}},
	}
}

// BuildScriptEnv is the environment build.sh reads its inputs from.
func BuildScriptEnv(r Recipe) []string {
	return []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root",
		"CLAUDENBOXEN_PACKAGES=" + strings.Join(r.Packages, " "),
		"CLAUDENBOXEN_CLAUDE_REPO=" + r.ClaudeRepo(),
		"CLAUDENBOXEN_CLAUDE_KEY_URL=" + r.ClaudeKeyURL,
		"CLAUDENBOXEN_CLAUDE_KEY_SHA256=" + r.ClaudeKeySHA256,
	}
}

// runBuildScript runs build.sh as uid 0 inside a throwaway, non-overlaid
// gVisor sandbox rooted at rootfsDir. Host networking is used because an
// isolated netns needs privilege (see internal/prereq) and the build only
// fetches from the Alpine mirrors and Claude Code's apk repository.
func runBuildScript(ctx context.Context, env BuildEnv, rootfsDir string, r Recipe, out io.Writer) error {
	spec := oci.BuildSpec(oci.Config{
		RootfsPath:  rootfsDir,
		Hostname:    "claudenboxen-build",
		Terminal:    false,
		Args:        []string{"/bin/sh", "/build.sh"},
		Env:         BuildScriptEnv(r),
		Cwd:         "/",
		UID:         0,
		GID:         0,
		HostNetwork: true,
		ExtraMounts: DNSMounts(),
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
	cmd := runner.RunCommand(ctx, bundleDir, "claudenboxen-build-"+filepath.Base(filepath.Dir(rootfsDir)), "")
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build.sh failed: %w", err)
	}
	return nil
}
