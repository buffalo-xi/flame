package storage

import (
	"context"
	"fmt"
	"io"
	"time"

	"flame/nexus/shared/config"
)

// Entry 目录下的一个条目。
type Entry struct {
	Name    string    `json:"name"`     // 名字（不含路径）
	IsDir   bool      `json:"is_dir"`   // 是否目录
	Size    int64     `json:"size"`     // 字节数（目录为 0）
	ModTime time.Time `json:"mod_time"` // 修改时间；S3 的「目录」没有该属性，为零值
}

// Storage 文件存储抽象：统一 local / nfs / s3 的读写。
type Storage interface {
	ReadFile(ctx context.Context, path string) ([]byte, error)
	WriteFile(ctx context.Context, path string, data []byte) error
	ReadStream(ctx context.Context, path string) (io.ReadCloser, error)
	WriteStream(ctx context.Context, path string, reader io.Reader) error
	Delete(ctx context.Context, path string) error
	// DeletePrefix 递归删除一个目录及其全部子孙（目录不存在时返回 nil）。
	DeletePrefix(ctx context.Context, dir string) error
	// Rename 移动/重命名一个文件或目录（源不存在时返回 nil）。
	// 文件还是目录由实现自己判定，调用方不必也不能声明 —— 声明错了就是静默的数据事故
	// （S3 上按前缀搬目录的算法套到单文件身上，会连带搬走「同名前缀」的其它对象）。
	Rename(ctx context.Context, oldPath, newPath string) error
	Exists(ctx context.Context, path string) (bool, error)
	// Stat 返回单个文件的元信息（字节数与修改时间）。目录或不存在时返回错误。
	// 下载用它拿 Content-Length / Last-Modified —— 比 List 一次父目录便宜得多（S3 上是 HEAD 而非 LIST）。
	Stat(ctx context.Context, path string) (size int64, modTime time.Time, err error)
	// List 列出目录下的直接子条目（一层，不递归）。
	List(ctx context.Context, dir string) ([]Entry, error)
	// PresignedGetURL 生成临时下载链接。
	// 后端不支持预签名（local / nfs）时返回 ("", nil) —— 空串表示「没有这个能力」，不是错误，
	// 调用方据此退回本进程发送，而不是把下载做成 try/fallback 的错误分支。
	// downloadName 非空时把「另存为」的文件名签进链接（S3 走 response-content-disposition）：
	// 浏览器收到的是 attachment，中文名按 RFC 5987 编码后不会乱码。
	PresignedGetURL(ctx context.Context, path string, expire time.Duration, downloadName string) (string, error)
}

// Walker 可选能力：递归枚举一个目录下的**全部文件**（含子孙目录）。
//
// 存在的理由只有一个：配额超限时要按「最旧优先」淘汰，就必须拿到整棵树的
// (路径, 大小, mtime)。List 只给一层，逐层递归在 S3 上会放大成 N 次 LIST 请求。
//
// 回调参数 rel 是**相对存储根**的路径（与 Delete 的参数同口径，可直接回传删除），
// size 是该文件字节数，mod 是修改时间。回调返回错误时立即中止并原样返回。
// 隐藏文件（`.` 开头的文件名或目录）由实现自行跳过 —— 它们不是用户产物。
type Walker interface {
	WalkPrefix(ctx context.Context, dir string, fn func(rel string, size int64, mod time.Time) error) error
}

// SeekableStorage 可选能力：能以可随机读的句柄打开文件（local / nfs）。
// 有这个能力才能用 http.ServeContent —— 自动处理 Range / If-Modified-Since / 206，
// 浏览器这才有进度条和断点续传。S3 不支持（对象存储按 Range 请求头取片段，不走 Seek）。
type SeekableStorage interface {
	OpenSeekCloser(ctx context.Context, path string) (io.ReadSeekCloser, error)
}

// New 根据 driver 创建对应实现。
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
