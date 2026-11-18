package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
	"time"

	"flame/nexus/shared/config"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// S3Storage S3 对象存储实现。
type S3Storage struct {
	client   *s3.Client
	uploader *manager.Uploader
	bucket   string
	prefix   string
}

// NewS3Storage 创建 S3 存储实例（client 从统一池子复用）。
func NewS3Storage(cfg config.S3Config) (*S3Storage, error) {
	client, err := DefaultPool.Get(cfg)
	if err != nil {
		return nil, err
	}
	return &S3Storage{
		client:   client,
		uploader: manager.NewUploader(client),
		bucket:   cfg.Bucket,
		prefix:   cfg.Prefix,
	}, nil
}

// objectKey 把逻辑 path 拼成 S3 object key（prefix + path）。
func (s *S3Storage) objectKey(p string) string {
	p = path.Clean("/" + p)
	if s.prefix == "" {
		return p[1:]
	}
	return path.Join(s.prefix, p[1:])
}

func (s *S3Storage) ReadFile(ctx context.Context, p string) ([]byte, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.objectKey(p)),
	})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

func (s *S3Storage) WriteFile(ctx context.Context, p string, data []byte) error {
	return s.WriteStream(ctx, p, bytes.NewReader(data))
}

// Stat 用 HeadObject 拿大小与修改时间（比 List 父目录少一次枚举）。
func (s *S3Storage) Stat(ctx context.Context, p string) (int64, time.Time, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.objectKey(p)),
	})
	if err != nil {
		return 0, time.Time{}, err
	}
	var size int64
	if out.ContentLength != nil {
		size = *out.ContentLength
	}
	var mod time.Time
	if out.LastModified != nil {
		mod = *out.LastModified
	}
	return size, mod, nil
}

func (s *S3Storage) ReadStream(ctx context.Context, p string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.objectKey(p)),
	})
	if err != nil {
		return nil, err
	}
	return out.Body, nil
}

// WriteStream 用 Uploader 分片上传，适合大文件。
func (s *S3Storage) WriteStream(ctx context.Context, p string, reader io.Reader) error {
	_, err := s.uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.objectKey(p)),
		Body:   reader,
	})
	return err
}

func (s *S3Storage) Delete(ctx context.Context, p string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.objectKey(p)),
	})
	return err
}

func (s *S3Storage) Exists(ctx context.Context, p string) (bool, error) {
	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.objectKey(p)),
	})
	if err == nil {
		return true, nil
	}
	var nf *types.NotFound
	if errors.As(err, &nf) {
		return false, nil
	}
	return false, err
}

// prefixOf 返回目录对应的 S3 前缀（以 / 结尾；根目录为空串）。
func (s *S3Storage) prefixOf(dir string) string {
	p := s.objectKey(dir)
	if p != "" && !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

// WalkPrefix 递归枚举前缀下的全部对象（不带 Delimiter，一次翻页拿一整棵子树）。
// 返回的 rel 是相对存储根的逻辑路径（与 Delete 同口径）；顺序为 S3 的字典序，
// 需要「最旧优先」的调用方自行按 mod 排序。
func (s *S3Storage) WalkPrefix(ctx context.Context, dir string, fn func(rel string, size int64, mod time.Time) error) error {
	prefix := s.prefixOf(dir)
	rootPrefix := ""
	if s.prefix != "" {
		rootPrefix = strings.TrimSuffix(s.prefix, "/") + "/"
	}
	var token *string
	for {
		res, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			return err
		}
		for _, obj := range res.Contents {
			if obj.Key == nil {
				continue
			}
			key := *obj.Key
			if strings.HasSuffix(key, "/") {
				continue // 目录占位对象（0 字节），不计入文件统计
			}
			rel := key
			if rootPrefix != "" {
				rel = strings.TrimPrefix(rel, rootPrefix)
			}
			if isHiddenPath(rel) {
				continue
			}
			var size int64
			if obj.Size != nil {
				size = *obj.Size
			}
			var mod time.Time
			if obj.LastModified != nil {
				mod = *obj.LastModified
			}
			if err := fn(rel, size, mod); err != nil {
				return err
			}
		}
		if res.IsTruncated == nil || !*res.IsTruncated || res.NextContinuationToken == nil {
			return nil
		}
		token = res.NextContinuationToken
	}
}

// isHiddenPath 判断路径是否有任意一段以 `.` 开头（与 filesystem 实现的跳过规则一致）。
func isHiddenPath(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}

// List 列出目录下的直接子条目（一层，不递归）。
// 用 Delimiter="/" 让 S3 把「子目录」归入 CommonPrefixes。
func (s *S3Storage) List(ctx context.Context, dir string) ([]Entry, error) {
	prefix := s.prefixOf(dir)
	out := make([]Entry, 0)
	var token *string
	for {
		res, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket),
			Prefix:            aws.String(prefix),
			Delimiter:         aws.String("/"),
			ContinuationToken: token,
		})
		if err != nil {
			return nil, err
		}
		for _, obj := range res.Contents {
			name := strings.TrimPrefix(*obj.Key, prefix)
			if name == "" || strings.HasSuffix(name, "/") {
				continue // 目录占位对象由 CommonPrefixes 表达
			}
			var size int64
			if obj.Size != nil {
				size = *obj.Size
			}
			var mod time.Time
			if obj.LastModified != nil {
				mod = *obj.LastModified
			}
			out = append(out, Entry{Name: name, Size: size, ModTime: mod})
		}
		for _, cp := range res.CommonPrefixes {
			name := strings.TrimSuffix(strings.TrimPrefix(*cp.Prefix, prefix), "/")
			if name == "" {
				continue
			}
			out = append(out, Entry{Name: name, IsDir: true})
		}
		if res.IsTruncated == nil || !*res.IsTruncated {
			break
		}
		token = res.NextContinuationToken
	}
	return out, nil
}

// DeletePrefix 批删整个前缀（S3 无目录概念，逐个删对象）。
func (s *S3Storage) DeletePrefix(ctx context.Context, dir string) error {
	prefix := s.prefixOf(dir)
	var token *string
	for {
		res, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			return err
		}
		if len(res.Contents) > 0 {
			objs := make([]types.ObjectIdentifier, 0, len(res.Contents))
			for _, o := range res.Contents {
				objs = append(objs, types.ObjectIdentifier{Key: o.Key})
			}
			if _, derr := s.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
				Bucket: aws.String(s.bucket),
				Delete: &types.Delete{Objects: objs, Quiet: aws.Bool(true)},
			}); derr != nil {
				return derr
			}
		}
		if res.IsTruncated == nil || !*res.IsTruncated {
			break
		}
		token = res.NextContinuationToken
	}
	return nil
}

// Rename 移动/重命名一个文件或目录。源不存在时返回 nil。
//
// 文件与目录必须分开处理，且这件事不能交给调用方声明 —— 猜错就是静默的数据事故：
// S3 上目录是一组 key 的共同前缀，单文件是一个 key。若一律按前缀匹配，
// 重命名 a.md 会把 a.md.bak 一起搬走；而「精确 key 是否存在」是唯一不会判错的依据。
// 所以先 HEAD 精确 key：命中即单对象移动，未命中才当目录走前缀重写。
func (s *S3Storage) Rename(ctx context.Context, oldPath, newPath string) error {
	oldKey := s.objectKey(oldPath)
	newKey := s.objectKey(newPath)
	if oldKey == newKey {
		return nil
	}
	if isFile, err := s.Exists(ctx, oldPath); err != nil {
		return err
	} else if isFile {
		// 单对象：S3 没有原子重命名，只能先复制再删。
		// 复制失败 → 旧对象完好，可安全重试；删除失败 → 新旧两份都在，
		// 如实报错（不假装成功：留下重复对象是用户看得见的事实）。
		if _, cerr := s.client.CopyObject(ctx, &s3.CopyObjectInput{
			Bucket:     aws.String(s.bucket),
			Key:        aws.String(newKey),
			CopySource: aws.String(url.PathEscape(s.bucket + "/" + oldKey)),
		}); cerr != nil {
			return cerr
		}
		if _, derr := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(oldKey),
		}); derr != nil {
			return fmt.Errorf("已复制到 %s，但删除旧对象 %s 失败（两处都在）: %w", newPath, oldPath, derr)
		}
		return nil
	}

	// 目录：逐个对象 CopyObject 后删旧前缀
	oldPrefix := s.prefixOf(oldPath)
	newPrefix := s.prefixOf(newPath)
	if oldPrefix == newPrefix {
		return nil
	}
	found := false
	var token *string
	for {
		res, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket),
			Prefix:            aws.String(oldPrefix),
			ContinuationToken: token,
		})
		if err != nil {
			return err
		}
		for _, obj := range res.Contents {
			found = true
			rel := strings.TrimPrefix(*obj.Key, oldPrefix)
			if _, cerr := s.client.CopyObject(ctx, &s3.CopyObjectInput{
				Bucket:     aws.String(s.bucket),
				Key:        aws.String(newPrefix + rel),
				CopySource: aws.String(url.PathEscape(s.bucket + "/" + *obj.Key)),
			}); cerr != nil {
				return cerr
			}
		}
		if res.IsTruncated == nil || !*res.IsTruncated {
			break
		}
		token = res.NextContinuationToken
	}
	if !found {
		return nil
	}
	return s.DeletePrefix(ctx, oldPath)
}

func (s *S3Storage) PresignedGetURL(ctx context.Context, p string, expire time.Duration, downloadName string) (string, error) {
	in := &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.objectKey(p)),
	}
	if downloadName != "" {
		// 覆盖头也在签名范围内：改一个字符就验签失败，别人没法把「下载 A」改成「下载 B」。
		in.ResponseContentDisposition = aws.String(contentDisposition(downloadName))
	}
	pc := s3.NewPresignClient(s.client)
	req, err := pc.PresignGetObject(ctx, in, func(o *s3.PresignOptions) {
		o.Expires = expire
	})
	if err != nil {
		return "", err
	}
	return req.URL, nil
}

// contentDisposition 拼 RFC 6266 的附件头：filename= 是纯 ASCII 兜底（老客户端 / 中文被抹掉时
// 至少不是乱码），filename*= 是 RFC 5987 百分号编码（现代浏览器优先用它，中文正确）。
// 注意这个字符串会被 SDK 当作查询参数再编码一次，S3 解一次后原样写进响应头 —— 不要提前二次转义。
func contentDisposition(name string) string {
	return `attachment; filename="` + asciiName(name) + `"; filename*=UTF-8''` + url.PathEscape(name)
}

// asciiName 把文件名降级成 HTTP 头里安全的纯 ASCII：引号、反斜杠、控制字符与非 ASCII 一律换成 _。
func asciiName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r >= 0x7f || r == '"' || r == '\\' {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	if out := strings.Trim(b.String(), " ."); out != "" {
		return out
	}
	return "download"
}
