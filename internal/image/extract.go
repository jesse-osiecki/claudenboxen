package image

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ulikunitz/xz"
)

// ExtractTar extracts a .tar.gz or .tar.xz (chosen by extension) into
// destDir, stripping stripComponents leading path elements from each entry
// (like `tar --strip-components`). Device, FIFO, and socket entries are
// skipped: an unprivileged process can't mknod them, and /dev is always
// mounted fresh at start anyway.
func ExtractTar(archivePath, destDir string, stripComponents int) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open %s: %w", archivePath, err)
	}
	defer f.Close()

	var r io.Reader
	switch {
	case strings.HasSuffix(archivePath, ".gz") || strings.HasSuffix(archivePath, ".tgz"):
		gz, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf("open gzip stream %s: %w", archivePath, err)
		}
		defer gz.Close()
		r = gz
	case strings.HasSuffix(archivePath, ".xz"):
		xr, err := xz.NewReader(f)
		if err != nil {
			return fmt.Errorf("open xz stream %s: %w", archivePath, err)
		}
		r = xr
	default:
		return fmt.Errorf("%s: unsupported archive type (expected .tar.gz or .tar.xz)", archivePath)
	}
	tr := tar.NewReader(r)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar entry from %s: %w", archivePath, err)
		}

		name := stripPathComponents(hdr.Name, stripComponents)
		if name == "" || name == "." {
			continue
		}
		target := filepath.Join(destDir, name)
		if !strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("tar entry %q escapes destination directory", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", target, err)
			}
			// MkdirAll leaves an existing directory's mode alone, so apply
			// the recorded one (full mode, so sticky/setgid bits survive).
			if err := os.Chmod(target, hdr.FileInfo().Mode()); err != nil {
				return fmt.Errorf("chmod %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", filepath.Dir(target), err)
			}
			if err := extractRegularFile(tr, target, hdr.FileInfo().Mode()); err != nil {
				return fmt.Errorf("extract %s: %w", target, err)
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", filepath.Dir(target), err)
			}
			os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return fmt.Errorf("symlink %s: %w", target, err)
			}
		case tar.TypeLink:
			linkTarget := filepath.Join(destDir, stripPathComponents(hdr.Linkname, stripComponents))
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", filepath.Dir(target), err)
			}
			os.Remove(target)
			if err := os.Link(linkTarget, target); err != nil {
				return fmt.Errorf("hardlink %s: %w", target, err)
			}
		}
	}
}

func extractRegularFile(tr *tar.Reader, target string, mode os.FileMode) error {
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, tr)
	return err
}

func stripPathComponents(name string, n int) string {
	clean := filepath.Clean(name)
	if clean == "." {
		return ""
	}
	parts := strings.Split(strings.TrimPrefix(clean, "/"), string(os.PathSeparator))
	if len(parts) <= n {
		return ""
	}
	return filepath.Join(parts[n:]...)
}
