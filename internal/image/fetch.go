package image

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
)

// DownloadVerified streams url to destPath while hashing it, and fails if
// the result doesn't match wantSHA256 (lowercase hex). destPath is removed
// on any failure.
func DownloadVerified(url, wantSHA256, destPath string) (err error) {
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch %s: unexpected status %s", url, resp.Status)
	}

	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", destPath, err)
	}
	defer func() {
		f.Close()
		if err != nil {
			os.Remove(destPath)
		}
	}()

	h := sha256.New()
	if _, err = io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}

	got := hex.EncodeToString(h.Sum(nil))
	if got != wantSHA256 {
		err = fmt.Errorf("checksum mismatch for %s: got %s, want %s", url, got, wantSHA256)
		return err
	}
	return nil
}
