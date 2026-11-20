package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FilesystemStorage is local filesystem storage (shared by local / nfs).
// Once mounted, NFS is just a local path to the operating system, read and written the same way as a local disk.
type FilesystemStorage struct {
	Root string
}

// NewFilesystemStorage creates a filesystem storage instance.
func NewFilesystemStorage(root string) *FilesystemStorage {
	return &FilesystemStorage{Root: root}
}

func (f *FilesystemStorage) fullPath(p string) string {
	return filepath.Join(f.Root, filepath.Clean("/"+p))
}

func (f *FilesystemStorage) ReadFile(_ context.Context, p string) ([]byte, error) {
	return os.ReadFile(f.fullPath(p))
}

func (f *FilesystemStorage) WriteFile(_ context.Context, p string, data []byte) error {
	full := f.fullPath(p)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, data, 0o644)
}

func (f *FilesystemStorage) ReadStream(_ context.Context, p string) (io.ReadCloser, error) {
	return os.Open(f.fullPath(p))
}

func (f *FilesystemStorage) WriteStream(_ context.Context, p string, reader io.Reader) error {
	full := f.fullPath(p)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	file, err := os.Create(full)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(file, reader)
	return err
}

func (f *FilesystemStorage) Delete(_ context.Context, p string) error {
	return os.Remove(f.fullPath(p))
}

func (f *FilesystemStorage) Exists(_ context.Context, p string) (bool, error) {
	_, err := os.Stat(f.fullPath(p))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// Stat returns the metadata of a single file (a directory counts as an error; downloads target files only).
func (f *FilesystemStorage) Stat(_ context.Context, p string) (int64, time.Time, error) {
	fi, err := os.Stat(f.fullPath(p))
	if err != nil {
		return 0, time.Time{}, err
	}
	if fi.IsDir() {
		return 0, fi.ModTime(), fmt.Errorf("is a directory")
	}
	return fi.Size(), fi.ModTime(), nil
}

// OpenSeekCloser opens a randomly readable handle for http.ServeContent to handle Range / 206.
func (f *FilesystemStorage) OpenSeekCloser(_ context.Context, p string) (io.ReadSeekCloser, error) {
	return os.Open(f.fullPath(p)) // *os.File satisfies io.ReadSeekCloser natively
}

func (f *FilesystemStorage) List(_ context.Context, dir string) ([]Entry, error) {
	entries, err := os.ReadDir(f.fullPath(dir))
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		var size int64
		var mod time.Time
		if fi, ierr := e.Info(); ierr == nil {
			size = fi.Size()
			mod = fi.ModTime()
		}
		out = append(out, Entry{Name: e.Name(), IsDir: e.IsDir(), Size: size, ModTime: mod})
	}
	return out, nil
}

// DeletePrefix recursively deletes a directory and all of its descendants (returns nil when it is missing).
func (f *FilesystemStorage) DeletePrefix(_ context.Context, dir string) error {
	err := os.RemoveAll(f.fullPath(dir))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Rename renames / moves a file or a directory (returns nil when the source is missing).
// os.Rename works for both and is atomic within one filesystem — there is no S3-style intermediate state of "copied, not
// yet deleted".
func (f *FilesystemStorage) Rename(_ context.Context, oldPath, newPath string) error {
	old := f.fullPath(oldPath)
	if _, err := os.Stat(old); os.IsNotExist(err) {
		return nil
	}
	dst := f.fullPath(newPath)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.Rename(old, dst)
}

// PresignedGetURL: a local file has no "externally reachable temporary link", so it returns an empty string and nexus
// sends the bytes itself.
func (f *FilesystemStorage) PresignedGetURL(_ context.Context, _ string, _ time.Duration, _ string) (string, error) {
	return "", nil
}

// WalkPrefix recursively enumerates every file under a directory (skipping names starting with `.`).
// The order is filepath.WalkDir's lexical order; a caller that needs oldest-first sorts itself.
func (f *FilesystemStorage) WalkPrefix(ctx context.Context, dir string, fn func(rel string, size int64, mod time.Time) error) error {
	root := f.fullPath(dir)
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) { // deleted concurrently: skip it, that is not an anomaly
				return nil
			}
			return err
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if p == root {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			if os.IsNotExist(ierr) {
				return nil
			}
			return ierr
		}
		rel, rerr := filepath.Rel(f.Root, p)
		if rerr != nil {
			return rerr
		}
		return fn(filepath.ToSlash(rel), info.Size(), info.ModTime())
	})
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
