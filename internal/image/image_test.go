package image

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStripPathComponents(t *testing.T) {
	cases := []struct {
		name string
		n    int
		want string
	}{
		{"a/b/c", 0, "a/b/c"},
		{"./a/b/c", 1, "b/c"},
		{"a/b/c", 3, ""},
		{"a", 1, ""},
		{"/abs/path", 0, "abs/path"},
		{"./", 0, ""},
		{".", 0, ""},
	}
	for _, c := range cases {
		if got := stripPathComponents(c.name, c.n); got != c.want {
			t.Errorf("strip(%q,%d) = %q, want %q", c.name, c.n, got, c.want)
		}
	}
}

func TestExtractTar(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "a.tar.gz")

	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	write := func(h *tar.Header, body string) {
		h.Size = int64(len(body))
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	// Alpine's minirootfs uses "./"-prefixed entries, including "./" itself.
	write(&tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755}, "")
	write(&tar.Header{Name: "./tmp/", Typeflag: tar.TypeDir, Mode: 0o1777}, "")
	write(&tar.Header{Name: "./bin/hello", Typeflag: tar.TypeReg, Mode: 0o755}, "#!/bin/sh\n")
	write(&tar.Header{Name: "./bin/link", Typeflag: tar.TypeSymlink, Linkname: "hello"}, "")
	write(&tar.Header{Name: "./bin/hard", Typeflag: tar.TypeLink, Linkname: "./bin/hello"}, "")
	write(&tar.Header{Name: "./dev/null", Typeflag: tar.TypeChar, Mode: 0o666}, "")
	for _, c := range []interface{ Close() error }{tw, gw, f} {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}

	dest := filepath.Join(dir, "out")
	if err := ExtractTar(archive, dest, 0); err != nil {
		t.Fatal(err)
	}

	if data, _ := os.ReadFile(filepath.Join(dest, "bin", "hello")); string(data) != "#!/bin/sh\n" {
		t.Errorf("regular file: %q", data)
	}
	if info, _ := os.Stat(filepath.Join(dest, "tmp")); info == nil || info.Mode().Perm() != 0o777 || info.Mode()&os.ModeSticky == 0 {
		t.Errorf("sticky dir mode not preserved: %v", info)
	}
	if target, _ := os.Readlink(filepath.Join(dest, "bin", "link")); target != "hello" {
		t.Errorf("symlink: %q", target)
	}
	if data, _ := os.ReadFile(filepath.Join(dest, "bin", "hard")); string(data) != "#!/bin/sh\n" {
		t.Errorf("hardlink: %q", data)
	}
	if _, err := os.Lstat(filepath.Join(dest, "dev", "null")); !os.IsNotExist(err) {
		t.Error("device node should be skipped")
	}

	if err := ExtractTar(filepath.Join(dir, "x.tar.bz2"), dest, 0); err == nil {
		t.Error("unsupported extension should be rejected")
	}
}

func TestRootfsAsset(t *testing.T) {
	a, err := DefaultRecipe.RootfsAsset("amd64")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://dl-cdn.alpinelinux.org/alpine/" + alpineBranch + "/releases/x86_64/alpine-minirootfs-" + alpineVersion + "-x86_64.tar.gz"
	if a.URL != want {
		t.Errorf("url: %s", a.URL)
	}
	if len(a.SHA256) != 64 {
		t.Errorf("sha256: %q", a.SHA256)
	}
	if b, _ := DefaultRecipe.RootfsAsset("arm64"); !strings.Contains(b.URL, "aarch64") || b.SHA256 == a.SHA256 {
		t.Errorf("arm64 asset: %+v", b)
	}
	if _, err := DefaultRecipe.RootfsAsset("riscv64"); err == nil {
		t.Error("expected error for unsupported arch")
	}
}

func TestRecipeHashIsStableAndSensitive(t *testing.T) {
	a, err := DefaultRecipe.Hash("amd64")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := DefaultRecipe.Hash("amd64")
	if a != b || len(a) != 16 {
		t.Fatalf("hash not stable: %s %s", a, b)
	}
	if arm, _ := DefaultRecipe.Hash("arm64"); arm == a {
		t.Error("hash should differ by arch")
	}
	changed := DefaultRecipe
	changed.Packages = append([]string{"extra"}, DefaultRecipe.Packages...)
	if c, _ := changed.Hash("amd64"); c == a {
		t.Error("hash should change with package list")
	}
	changed = DefaultRecipe
	changed.ClaudeChannel = "stable"
	if c, _ := changed.Hash("amd64"); c == a {
		t.Error("hash should change with claude channel")
	}
}
