package storage

import (
	"context"
	"fmt"
	"io"
	"time"

	"flame/nexus/shared/config"
)

// Entry is one entry in a directory.
type Entry struct {
	Name    string    `json:"name"`     // the name (without a path)
	IsDir   bool      `json:"is_dir"`   // whether it is a directory
	Size    int64     `json:"size"`     // size in bytes (0 for a directory)
	ModTime time.Time `json:"mod_time"` // modification time; an S3 "directory" has no such property and gets the zero value
}

// Storage is the file storage abstraction: one read/write surface over local / nfs / s3.
type Storage interface {
	ReadFile(ctx context.Context, path string) ([]byte, error)
	WriteFile(ctx context.Context, path string, data []byte) error
	ReadStream(ctx context.Context, path string) (io.ReadCloser, error)
	WriteStream(ctx context.Context, path string, reader io.Reader) error
	Delete(ctx context.Context, path string) error
	// DeletePrefix recursively deletes a directory and all of its descendants (returns nil when the directory is missing).
	DeletePrefix(ctx context.Context, dir string) error
	// Rename moves / renames one file or one directory (returns nil when the source is missing).
	// Whether it is a file or a directory is decided by the implementation; the caller neither needs to nor may declare it
	// — a wrong declaration is a silent data accident (the algorithm for moving a directory by prefix, applied to a single
	// file on S3, would also move every other object that shares the prefix).
	Rename(ctx context.Context, oldPath, newPath string) error
	Exists(ctx context.Context, path string) (bool, error)
	// Stat returns the metadata of a single file (byte size and modification time). A directory or a missing path returns
	// an error.
	// Downloads use it to get Content-Length / Last-Modified — far cheaper than listing the parent once (a HEAD rather than
	// a LIST on S3).
	Stat(ctx context.Context, path string) (size int64, modTime time.Time, err error)
	// List lists the direct children of a directory (one level, not recursive).
	List(ctx context.Context, dir string) ([]Entry, error)
	// PresignedGetURL generates a temporary download link.
	// When the backend has no presigning (local / nfs) it returns ("", nil) — an empty string means "this capability does
	// not exist", not an error, and the caller uses that to fall back to sending from this process instead of turning the
	// download into a try/fallback error branch.
	// A non-empty downloadName signs the "save as" file name into the link (S3 uses response-content-disposition): the
	// browser receives an attachment, and a non-ASCII name arrives intact after RFC 5987 encoding.
	PresignedGetURL(ctx context.Context, path string, expire time.Duration, downloadName string) (string, error)
}

// Walker is an optional capability: recursively enumerate **every file** under a directory (descendant directories
// included).
//
// There is exactly one reason it exists: when a quota is exceeded, eviction is oldest-first, which needs the whole tree's
// (path, size, mtime). List gives one level only, and recursing level by level would amplify into N LIST requests on S3.
//
// The callback's rel is a path **relative to the storage root** (the same convention as Delete's argument, so it can be
// passed straight back to delete), size is that file's byte size and mod its modification time. When the callback returns
// an error the walk stops immediately and returns it unchanged.
// Hidden files (a file name or directory starting with `.`) are skipped by the implementation — they are not user output.
type Walker interface {
	WalkPrefix(ctx context.Context, dir string, fn func(rel string, size int64, mod time.Time) error) error
}

// SeekableStorage is an optional capability: open a file as a randomly readable handle (local / nfs).
// Only with it can http.ServeContent be used — Range / If-Modified-Since / 206 handled for free, which is what gives the
// browser a progress bar and resumable downloads. S3 does not support it (object storage takes a slice through the Range
// request header rather than seeking).
type SeekableStorage interface {
	OpenSeekCloser(ctx context.Context, path string) (io.ReadSeekCloser, error)
}

// New creates the implementation for a driver.
func New(cfg config.StorageConfig, root string) (Storage, error) {
	switch cfg.Driver {
	case "local", "nfs", "":
		return NewFilesystemStorage(root), nil
	case "s3":
		return NewS3Storage(cfg.S3)
	default:
		return nil, fmt.Errorf("unsupported storage driver: %s", cfg.Driver)
	}
}
