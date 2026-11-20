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

// S3ClientPool manages several S3 clients and reuses them by a configuration fingerprint.
type S3ClientPool struct {
	mu      sync.Mutex
	clients map[string]*s3.Client
}

// DefaultPool is the global S3 client pool.
var DefaultPool = &S3ClientPool{clients: make(map[string]*s3.Client)}

// s3Key derives a deterministic fingerprint from the S3 configuration content:
// the same configuration (the same S3) -> the same key -> reuse; a different configuration -> its own client.
func s3Key(cfg config.S3Config) string {
	raw := cfg.Endpoint + "|" + cfg.Region + "|" + cfg.Bucket + "|" + cfg.AccessKey + "|" + cfg.SecretKey
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// Get returns (and reuses) the S3 client for a configuration.
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

// newS3Client creates a new S3 client (lazily connected; no network request happens here).
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
			o.UsePathStyle = true // minio / self-hosted setups usually need path style
		}
	}), nil
}
