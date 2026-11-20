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

// S3Storage is the S3 object storage implementation.
type S3Storage struct {
	client   *s3.Client
	uploader *manager.Uploader
	bucket   string
	prefix   string
}

// NewS3Storage creates an S3 storage instance (the client is reused from the shared pool).
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

// objectKey turns a logical path into an S3 object key (prefix + path).
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

// Stat uses HeadObject to get the size and modification time (one enumeration cheaper than listing the parent).
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

// WriteStream uploads in parts through the Uploader, which suits large files.
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

// prefixOf returns the S3 prefix of a directory (ending with /; an empty string for the root).
func (s *S3Storage) prefixOf(dir string) string {
	p := s.objectKey(dir)
	if p != "" && !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

// WalkPrefix recursively enumerates every object under the prefix (no Delimiter, so one page walk takes a whole subtree).
// The rel returned is a logical path relative to the storage root (the same convention as Delete); the order is S3's
// lexical order, and a caller that needs oldest-first sorts by mod itself.
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
				continue // a directory placeholder object (0 bytes); not counted in file statistics
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

// isHiddenPath reports whether any segment of the path starts with `.` (the same skip rule as the filesystem
// implementation).
func isHiddenPath(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}

// List lists the direct children of a directory (one level, not recursive).
// Delimiter="/" makes S3 put "subdirectories" into CommonPrefixes.
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
				continue // directory placeholders are represented by CommonPrefixes
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

// DeletePrefix batch-deletes a whole prefix (S3 has no directories, so objects are deleted one by one).
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

// Rename moves / renames one file or one directory. A missing source returns nil.
//
// Files and directories must be handled separately, and this cannot be delegated to the caller by declaration — guessing
// wrong is a silent data accident: on S3 a directory is the common prefix of a set of keys while a single file is one key.
// Matching by prefix for both would move a.md.bak along with a.md; "does this exact key exist" is the only criterion that
// cannot be wrong.
// So the exact key is HEAD first: a hit means a single-object move, and only a miss falls through to the prefix rewrite.
func (s *S3Storage) Rename(ctx context.Context, oldPath, newPath string) error {
	oldKey := s.objectKey(oldPath)
	newKey := s.objectKey(newPath)
	if oldKey == newKey {
		return nil
	}
	if isFile, err := s.Exists(ctx, oldPath); err != nil {
		return err
	} else if isFile {
		// A single object: S3 has no atomic rename, so it has to be copied and then deleted.
		// A failed copy leaves the old object intact and can be retried safely; a failed delete leaves both copies, and the
		// error is reported honestly (never pretending success: a leftover duplicate object is a fact the user can see).
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
			return fmt.Errorf("copied to %s but deleting the old object %s failed (both exist): %w", newPath, oldPath, derr)
		}
		return nil
	}

	// A directory: CopyObject each object, then delete the old prefix
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
		// The override headers are inside the signature too: changing one character invalidates it, so nobody can turn
		// "download A" into "download B".
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

// contentDisposition assembles the RFC 6266 attachment header: filename= is a plain-ASCII fallback (on an old client, or
// with the non-ASCII part stripped, it is at least not mojibake), filename*= is the RFC 5987 percent encoding (modern
// browsers prefer it, and non-ASCII names come out right).
// Note that the SDK encodes this string again as a query parameter and S3 decodes it once before writing it into the
// response header verbatim — do not escape it twice up front.
func contentDisposition(name string) string {
	return `attachment; filename="` + asciiName(name) + `"; filename*=UTF-8''` + url.PathEscape(name)
}

// asciiName degrades a file name to plain ASCII that is safe in an HTTP header: quotes, backslashes, control characters
// and anything non-ASCII become _.
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
