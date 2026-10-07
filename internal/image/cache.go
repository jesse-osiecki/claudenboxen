package image

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrNoImage means images/current doesn't resolve to a built image yet —
// callers should build one.
var ErrNoImage = errors.New("no cached image")

// Meta is written alongside a built rootfs so later runs can tell whether
// it's stale relative to the recipe baked into the running binary.
type Meta struct {
	RecipeHash    string    `json:"recipe_hash"`
	Arch          string    `json:"arch"`
	BuiltAt       time.Time `json:"built_at"`
	AlpineVersion string    `json:"alpine_version"`
}

func xdgDir(envVar, fallbackRelHome string) (string, error) {
	if v := os.Getenv(envVar); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, fallbackRelHome), nil
}

// CacheDir is ~/.cache/claudenboxen (respecting $XDG_CACHE_HOME).
func CacheDir() (string, error) {
	base, err := xdgDir("XDG_CACHE_HOME", ".cache")
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "claudenboxen"), nil
}

// RunStateDir is ~/.local/state/claudenboxen/runsc (respecting
// $XDG_STATE_HOME) — passed to runsc as --root.
func RunStateDir() (string, error) {
	base, err := xdgDir("XDG_STATE_HOME", filepath.Join(".local", "state"))
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "claudenboxen", "runsc"), nil
}

func imagesDir() (string, error) {
	c, err := CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(c, "images"), nil
}

// BundlesDir is ~/.cache/claudenboxen/bundles: per-run OCI bundles.
func BundlesDir() (string, error) {
	c, err := CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(c, "bundles"), nil
}

func imageDir(hash string) (string, error) {
	d, err := imagesDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, hash), nil
}

func currentLink() (string, error) {
	d, err := imagesDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "current"), nil
}

// CurrentImage resolves images/current. Returns ErrNoImage if it doesn't
// exist yet.
func CurrentImage() (rootfsPath, hash string, err error) {
	link, err := currentLink()
	if err != nil {
		return "", "", err
	}
	target, err := os.Readlink(link)
	if os.IsNotExist(err) {
		return "", "", ErrNoImage
	}
	if err != nil {
		return "", "", fmt.Errorf("read %s: %w", link, err)
	}
	hash = filepath.Base(target)
	dir, err := imageDir(hash)
	if err != nil {
		return "", "", err
	}
	return filepath.Join(dir, "rootfs"), hash, nil
}

// ReadMeta loads meta.json for a given image hash.
func ReadMeta(hash string) (Meta, error) {
	dir, err := imageDir(hash)
	if err != nil {
		return Meta{}, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return Meta{}, err
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return Meta{}, fmt.Errorf("parse meta.json: %w", err)
	}
	return m, nil
}

// writeMeta writes meta.json for hash.
func writeMeta(hash string, m Meta) error {
	dir, err := imageDir(hash)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "meta.json"), data, 0o644)
}

// setCurrent atomically repoints images/current at hash.
func setCurrent(hash string) error {
	dir, err := imagesDir()
	if err != nil {
		return err
	}
	link := filepath.Join(dir, "current")
	tmp := link + ".tmp"
	os.Remove(tmp)
	if err := os.Symlink(hash, tmp); err != nil {
		return fmt.Errorf("symlink new current: %w", err)
	}
	return os.Rename(tmp, link)
}

// ImageInfo is a listing entry for `claudenboxen clean`.
type ImageInfo struct {
	Hash      string
	Meta      Meta
	IsCurrent bool
}

// ListImages enumerates everything under images/, marking whichever one
// images/current points at.
func ListImages() ([]ImageInfo, error) {
	dir, err := imagesDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	_, currentHash, _ := CurrentImage() // best-effort; "" if none

	var out []ImageInfo
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "current" {
			continue
		}
		meta, _ := ReadMeta(e.Name()) // best-effort; zero value if unreadable
		out = append(out, ImageInfo{
			Hash:      e.Name(),
			Meta:      meta,
			IsCurrent: e.Name() == currentHash,
		})
	}
	return out, nil
}

// PruneImages removes every cached image except keepHash.
func PruneImages(keepHash string) (removed []string, err error) {
	images, err := ListImages()
	if err != nil {
		return nil, err
	}
	dir, err := imagesDir()
	if err != nil {
		return nil, err
	}
	for _, img := range images {
		if img.Hash == keepHash {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, img.Hash)); err != nil {
			return removed, fmt.Errorf("remove image %s: %w", img.Hash, err)
		}
		removed = append(removed, img.Hash)
	}
	return removed, nil
}
