package run

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	specs "github.com/opencontainers/runtime-spec/specs-go"
)

// shadowRestrictedFiles walks hostDir and, for every regular file with no
// "other" read bit, copies it into shadowDir with relaxed permissions and
// returns a bind mount overriding just that path inside sandboxRoot.
//
// Rootless gVisor returns EACCES for a bind-mounted file with no
// group/other read bit even when the sandboxed uid owns it. Claude Code
// stores its OAuth token at 0600, so without this the credentials would be
// silently unreadable in the sandbox.
//
// The returned writeback copies each shadow's content back to its host
// path (os.WriteFile keeps an existing file's mode, so the original 0600
// is preserved) and must run before shadowDir is removed.
func shadowRestrictedFiles(hostDir, sandboxRoot, shadowDir string) (mounts []specs.Mount, writeback func() error, err error) {
	type shadowed struct{ hostPath, shadowPath string }
	var shadows []shadowed

	err = filepath.WalkDir(hostDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode().Perm()&0o004 != 0 {
			return nil
		}

		rel, err := filepath.Rel(hostDir, path)
		if err != nil {
			return err
		}
		shadowPath := filepath.Join(shadowDir, rel)
		if err := os.MkdirAll(filepath.Dir(shadowPath), 0o700); err != nil {
			return err
		}
		if err := copyFileContent(path, shadowPath, 0o644); err != nil {
			return err
		}

		shadows = append(shadows, shadowed{hostPath: path, shadowPath: shadowPath})
		mounts = append(mounts, specs.Mount{
			Destination: filepath.Join(sandboxRoot, rel),
			Type:        "bind",
			Source:      shadowPath,
			Options:     []string{"rbind", "rw"},
		})
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("scan %s for owner-only files: %w", hostDir, err)
	}

	writeback = func() error {
		for _, s := range shadows {
			if err := copyFileContent(s.shadowPath, s.hostPath, 0); err != nil {
				return fmt.Errorf("write back %s: %w", s.hostPath, err)
			}
		}
		return nil
	}
	return mounts, writeback, nil
}

// shadowRestrictedFile is shadowRestrictedFiles for a single file outside
// any mounted directory (~/.claude.json). Returns a nil mount and no-op
// writeback if the file doesn't need shadowing.
func shadowRestrictedFile(hostPath, sandboxPath, shadowDir string) (mount *specs.Mount, writeback func() error, err error) {
	noop := func() error { return nil }

	info, err := os.Stat(hostPath)
	if err != nil {
		return nil, noop, err
	}
	if info.Mode().Perm()&0o004 != 0 {
		return nil, noop, nil
	}

	shadowPath := filepath.Join(shadowDir, filepath.Base(hostPath))
	if err := copyFileContent(hostPath, shadowPath, 0o644); err != nil {
		return nil, nil, err
	}
	m := specs.Mount{Destination: sandboxPath, Type: "bind", Source: shadowPath, Options: []string{"rbind", "rw"}}
	writeback = func() error {
		return copyFileContent(shadowPath, hostPath, 0)
	}
	return &m, writeback, nil
}

// copyFileContent overwrites dst with src's content. mode applies only
// when dst is created; an existing file keeps its permission bits.
func copyFileContent(src, dst string, mode fs.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, mode)
}
