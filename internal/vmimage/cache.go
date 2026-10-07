package vmimage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jesse-osiecki/claudenboxen/internal/image"
)

// ErrNoImage means vm-images/current doesn't resolve to a built image yet —
// callers should build one.
var ErrNoImage = errors.New("no cached VM image")

// Meta is written alongside a built VM image so later runs can tell
// whether it's stale relative to the recipe baked into the running binary.
type Meta struct {
	RecipeHash string    `json:"recipe_hash"`
	Arch       string    `json:"arch"`
	BuiltAt    time.Time `json:"built_at"`
}

// Image is one built VM image: the ext4 base disk (booted through a
// per-run qcow2 overlay) and the kernel/initrd extracted from it.
type Image struct {
	Dir    string // vm-images/<hash>
	Disk   string // .../disk.img
	Kernel string // .../kernel
	Initrd string // .../initrd
}

// rootCacheDir is ~/.cache/claudenboxen/vm-images (respecting
// $XDG_CACHE_HOME via internal/image.CacheDir).
func rootCacheDir() (string, error) {
	c, err := image.CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(c, "vm-images"), nil
}

// BundlesDir is ~/.cache/claudenboxen/vm-bundles: per-run state (overlay
// disk, virtiofsd sockets, control share).
func BundlesDir() (string, error) {
	c, err := image.CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(c, "vm-bundles"), nil
}

func imageDir(hash string) (string, error) {
	d, err := rootCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, hash), nil
}

func imageAt(hash string) (Image, error) {
	dir, err := imageDir(hash)
	if err != nil {
		return Image{}, err
	}
	return Image{
		Dir:    dir,
		Disk:   filepath.Join(dir, "disk.img"),
		Kernel: filepath.Join(dir, "kernel"),
		Initrd: filepath.Join(dir, "initrd"),
	}, nil
}

func currentLink() (string, error) {
	d, err := rootCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "current"), nil
}

// CurrentImage resolves vm-images/current. Returns ErrNoImage if it
// doesn't exist yet.
func CurrentImage() (img Image, hash string, err error) {
	link, err := currentLink()
	if err != nil {
		return Image{}, "", err
	}
	target, err := os.Readlink(link)
	if os.IsNotExist(err) {
		return Image{}, "", ErrNoImage
	}
	if err != nil {
		return Image{}, "", fmt.Errorf("read %s: %w", link, err)
	}
	hash = filepath.Base(target)
	img, err = imageAt(hash)
	return img, hash, err
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

// setCurrent atomically repoints vm-images/current at hash.
func setCurrent(hash string) error {
	dir, err := rootCacheDir()
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

// ListImages enumerates everything under vm-images/, marking whichever one
// vm-images/current points at.
func ListImages() ([]ImageInfo, error) {
	dir, err := rootCacheDir()
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
		if !e.IsDir() || e.Name() == "current" || e.Name() == "tmp" {
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

// PruneImages removes every cached VM image except keepHash.
func PruneImages(keepHash string) (removed []string, err error) {
	images, err := ListImages()
	if err != nil {
		return nil, err
	}
	dir, err := rootCacheDir()
	if err != nil {
		return nil, err
	}
	for _, img := range images {
		if img.Hash == keepHash {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, img.Hash)); err != nil {
			return removed, fmt.Errorf("remove VM image %s: %w", img.Hash, err)
		}
		removed = append(removed, img.Hash)
	}
	return removed, nil
}
