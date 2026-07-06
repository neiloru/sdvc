// Package scan handles folder change detection, zip creation and safe extraction.
package scan

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	stagingSuffix = ".sdvc-new"
	backupSuffix  = ".sdvc-old"
)

// ContentHash returns a stable SHA-256 over the folder's file tree (relative
// paths + file contents). A missing or empty folder yields an empty string.
//
// This is independent of any zip encoding, so it reliably detects whether the
// save data changed since the last sync.
func ContentHash(folder string) (string, error) {
	info, err := os.Stat(folder)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory: %s", folder)
	}

	files, err := collectFiles(folder)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", nil
	}

	h := sha256.New()
	for _, p := range files {
		rel, err := filepath.Rel(folder, p)
		if err != nil {
			return "", err
		}
		fh, err := hashFile(p)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\n%s\n", filepath.ToSlash(rel), fh)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// CreateZip writes a zip of the folder to a temp file and returns its path and
// the SHA-256 of the produced zip bytes. The caller must remove the temp file.
func CreateZip(folder string) (zipPath, hash string, err error) {
	files, err := collectFiles(folder)
	if err != nil {
		return "", "", err
	}

	tmp, err := os.CreateTemp("", "sdvc-upload-*.zip")
	if err != nil {
		return "", "", err
	}
	tmpName := tmp.Name()
	success := false
	defer func() {
		if !success {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	hasher := sha256.New()
	zw := zip.NewWriter(io.MultiWriter(tmp, hasher))

	for _, p := range files {
		if err := addFileToZip(zw, folder, p); err != nil {
			zw.Close()
			return "", "", err
		}
	}
	if err := zw.Close(); err != nil {
		return "", "", err
	}
	if err := tmp.Sync(); err != nil {
		return "", "", err
	}
	if err := tmp.Close(); err != nil {
		return "", "", err
	}

	success = true
	return tmpName, hex.EncodeToString(hasher.Sum(nil)), nil
}

// ExtractReplace atomically replaces the folder's contents with the zip's.
// It extracts into a staging directory first, then swaps directories so a
// failure never leaves a half-written folder.
func ExtractReplace(folder, zipPath string) error {
	parent := filepath.Dir(folder)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}

	staging := folder + stagingSuffix
	backup := folder + backupSuffix
	_ = os.RemoveAll(staging)
	_ = os.RemoveAll(backup)

	if err := extractTo(staging, zipPath); err != nil {
		os.RemoveAll(staging)
		return err
	}

	folderExists := false
	if _, err := os.Stat(folder); err == nil {
		folderExists = true
	}

	if folderExists {
		if err := os.Rename(folder, backup); err != nil {
			os.RemoveAll(staging)
			return fmt.Errorf("move existing folder aside: %w", err)
		}
	}
	if err := os.Rename(staging, folder); err != nil {
		if folderExists {
			_ = os.Rename(backup, folder) // best-effort restore
		}
		return fmt.Errorf("swap in new folder: %w", err)
	}
	_ = os.RemoveAll(backup)
	return nil
}

func collectFiles(folder string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(folder, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if strings.HasSuffix(name, stagingSuffix) || strings.HasSuffix(name, backupSuffix) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil // skip symlinks for safety
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func addFileToZip(zw *zip.Writer, root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = filepath.ToSlash(rel)
	header.Method = zip.Deflate

	w, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

func extractTo(dest, zipPath string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()

	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	cleanDest := filepath.Clean(dest)

	for _, f := range zr.File {
		target := filepath.Join(cleanDest, f.Name)
		// Zip-slip guard: ensure target stays within dest.
		if target != cleanDest && !strings.HasPrefix(target, cleanDest+string(os.PathSeparator)) {
			return fmt.Errorf("unsafe path in archive: %q", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := writeZipEntry(f, target); err != nil {
			return err
		}
	}
	return nil
}

func writeZipEntry(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()

	// Limit copy to the declared uncompressed size to guard against zip bombs.
	if _, err := io.Copy(out, io.LimitReader(rc, int64(f.UncompressedSize64)+1)); err != nil {
		return err
	}
	return nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
