// Package image builds and caches the sandbox's base rootfs: Alpine Linux
// plus the claude CLI (from Claude Code's own apk repository), installed by
// running a build script inside a throwaway runsc container (see build.go).
package image

//go:generate go run ../../tools/update-alpine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"runtime"

	recipescript "github.com/jesse-osiecki/claudenboxen/build/alpine-recipe"
)

// Asset is a single downloadable, checksum-verified build input.
type Asset struct {
	URL    string
	SHA256 string // lowercase hex
}

// Recipe pins every input to the base image. Changing any of them changes
// Hash() and triggers a rebuild on next run.
type Recipe struct {
	AlpineBranch  string            // e.g. "v3.24"
	AlpineVersion string            // e.g. "3.24.2"
	RootfsSHA256  map[string]string // by Alpine arch name
	Packages      []string          // apk packages from the Alpine repositories

	// Claude Code's apk repository. Channel is "latest" or "stable"; the
	// package version inside it floats deliberately.
	ClaudeChannel   string
	ClaudeKeyURL    string
	ClaudeKeySHA256 string
}

// DefaultRecipe is the recipe baked into the binary. The Alpine pin comes
// from alpine_pin.go; refresh it with `go generate ./internal/image/`.
var DefaultRecipe = Recipe{
	AlpineBranch:  alpineBranch,
	AlpineVersion: alpineVersion,
	RootfsSHA256:  alpineRootfsSHA256,
	Packages: []string{
		// Claude Code's runtime dependencies on musl.
		"bash",
		"curl",
		"libgcc",
		"libstdc++",
		"ripgrep",
		"ca-certificates",
		"git",
		"jq",
		"less",
		"openssh-client",
		"procps-ng",
		// For project tooling and npx-launched MCP servers.
		"nodejs",
		"npm",
		// For Claude Code's own bubblewrap Bash sandbox, if enabled in the
		// user's settings (see README, nested sandboxing).
		"bubblewrap",
		"socat",
	},
	ClaudeChannel:   "latest",
	ClaudeKeyURL:    "https://downloads.claude.ai/keys/claude-code.rsa.pub",
	ClaudeKeySHA256: "395759c1f7449ef4cdef305a42e820f3c766d6090d142634ebdb049f113168b6",
}

// ClaudeRepo is the apk repository URL for the recipe's channel.
func (r Recipe) ClaudeRepo() string {
	return "https://downloads.claude.ai/claude-code/apk/" + r.ClaudeChannel
}

// alpineArch maps GOARCH to Alpine's architecture name.
func alpineArch(goarch string) (string, error) {
	switch goarch {
	case "amd64":
		return "x86_64", nil
	case "arm64":
		return "aarch64", nil
	default:
		return "", fmt.Errorf("unsupported architecture %q (claudenboxen supports amd64 and arm64)", goarch)
	}
}

// RootfsAsset returns the pinned Alpine minirootfs tarball for goarch.
func (r Recipe) RootfsAsset(goarch string) (Asset, error) {
	arch, err := alpineArch(goarch)
	if err != nil {
		return Asset{}, err
	}
	sum, ok := r.RootfsSHA256[arch]
	if !ok {
		return Asset{}, fmt.Errorf("no rootfs checksum pinned for %s", arch)
	}
	return Asset{
		URL: fmt.Sprintf("https://dl-cdn.alpinelinux.org/alpine/%s/releases/%s/alpine-minirootfs-%s-%s.tar.gz",
			r.AlpineBranch, arch, r.AlpineVersion, arch),
		SHA256: sum,
	}, nil
}

// HostArch is runtime.GOARCH.
func HostArch() string { return runtime.GOARCH }

// Hash fingerprints every input that affects the built image: the recipe
// fields, the rootfs checksum for goarch, and build.sh's content, so a
// change to any of them triggers a rebuild.
func (r Recipe) Hash(goarch string) (string, error) {
	rootfs, err := r.RootfsAsset(goarch)
	if err != nil {
		return "", err
	}

	h := sha256.New()
	fmt.Fprintf(h, "arch=%s\n", goarch)
	fmt.Fprintf(h, "alpine_branch=%s\n", r.AlpineBranch)
	fmt.Fprintf(h, "alpine_version=%s\n", r.AlpineVersion)
	fmt.Fprintf(h, "rootfs_sha256=%s\n", rootfs.SHA256)
	for _, pkg := range r.Packages {
		fmt.Fprintf(h, "pkg=%s\n", pkg)
	}
	fmt.Fprintf(h, "claude_channel=%s\n", r.ClaudeChannel)
	fmt.Fprintf(h, "claude_key_url=%s\n", r.ClaudeKeyURL)
	fmt.Fprintf(h, "claude_key_sha256=%s\n", r.ClaudeKeySHA256)
	fmt.Fprintf(h, "build_script_sha256=%x\n", sha256.Sum256([]byte(recipescript.BuildScript)))
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}
