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

// FilesystemStorage 本地文件系统存储（local / nfs 共用）。
// NFS 挂载后对操作系统而言就是一个本地路径，读写方式与本地盘一致。
type FilesystemStorage struct {
	Root string
}

// NewFilesystemStorage 创建文件系统存储实例。
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

// Stat 返回单个文件的元信息（目录视为错误，下载只针对文件）。
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

// OpenSeekCloser 打开可随机读的句柄，供 http.ServeContent 处理 Range / 206。
func (f *FilesystemStorage) OpenSeekCloser(_ context.Context, p string) (io.ReadSeekCloser, error) {
	return os.Open(f.fullPath(p)) // *os.File 天然满足 io.ReadSeekCloser
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

// DeletePrefix 递归删除目录及其全部子孙（目录不存在时返回 nil）。
func (f *FilesystemStorage) DeletePrefix(_ context.Context, dir string) error {
	err := os.RemoveAll(f.fullPath(dir))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Rename 重命名/移动文件或目录（源不存在时返回 nil）。
// os.Rename 对两者通用且在同一条文件系统内是原子的 —— 不存在 S3 那种「复制完再删」的中间态。
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

// PresignedGetURL 本地文件没有「外部可达的临时链接」：返回空串，由 nexus 自己发字节。
func (f *FilesystemStorage) PresignedGetURL(_ context.Context, _ string, _ time.Duration, _ string) (string, error) {
	return "", nil
}

// WalkPrefix 递归枚举目录下的全部文件（跳过 `.` 开头的名字）。
// 顺序是 filepath.WalkDir 的字典序，需要「最旧优先」的调用方自行排序。
func (f *FilesystemStorage) WalkPrefix(ctx context.Context, dir string, fn func(rel string, size int64, mod time.Time) error) error {
	root := f.fullPath(dir)
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) { // 并发删除：跳过即可，不是异常
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
