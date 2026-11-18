package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"

	"flame/nexus/shared/config"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3ClientPool 管理多个 S3 client，按配置指纹复用。
type S3ClientPool struct {
	mu      sync.Mutex
	clients map[string]*s3.Client
}

// DefaultPool 全局 S3 client 池。
var DefaultPool = &S3ClientPool{clients: make(map[string]*s3.Client)}

// s3Key 由 S3 配置内容生成确定性指纹：
// 相同配置（同一个 S3）→ 相同 key → 复用；不同配置 → 独立 client。
func s3Key(cfg config.S3Config) string {
	raw := cfg.Endpoint + "|" + cfg.Region + "|" + cfg.Bucket + "|" + cfg.AccessKey + "|" + cfg.SecretKey
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// Get 按配置返回（并复用）S3 client。
func (p *S3ClientPool) Get(cfg config.S3Config) (*s3.Client, error) {
	key := s3Key(cfg)
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.clients[key]; ok {
		return c, nil
	}
	c, err := newS3Client(cfg)
	if err != nil {
		return nil, err
	}
	p.clients[key] = c
	return c, nil
}

// newS3Client 创建一个新的 S3 client（懒连接，此处不产生网络请求）。
func newS3Client(cfg config.S3Config) (*s3.Client, error) {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		),
	}
	awscfg, err := awsconfig.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, err
	}
	return s3.NewFromConfig(awscfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.UsePathStyle = true // minio/自建通常需要 path-style
		}
	}), nil
}
