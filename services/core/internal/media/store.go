// Package media handles report media: bounded upload, real MIME detection, checksum, malware-scan status,
// private object storage and short-lived signed download URLs (never permanent public URLs; ADR-006).
package media

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Store interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	SignedURL(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// ---------------------------------------------------------------------------
// Filesystem store (local development); downloads go through the API with an HMAC-signed URL.
// ---------------------------------------------------------------------------

type FSStore struct {
	Dir     string
	Secret  []byte
	BaseURL string // e.g. http://localhost:8080/api/v1/media/blob
	Now     func() time.Time
}

func (s *FSStore) path(key string) (string, error) {
	clean := filepath.Clean("/" + key)
	if strings.Contains(key, "..") {
		return "", errors.New("invalid key")
	}
	return filepath.Join(s.Dir, clean), nil
}

func (s *FSStore) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	p, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (s *FSStore) Open(_ context.Context, key string) (io.ReadCloser, error) {
	p, err := s.path(key)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

func (s *FSStore) sign(key string, exp int64) string {
	m := hmac.New(sha256.New, s.Secret)
	fmt.Fprintf(m, "%s|%d", key, exp)
	return hex.EncodeToString(m.Sum(nil))
}

func (s *FSStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *FSStore) SignedURL(_ context.Context, key string, ttl time.Duration) (string, error) {
	exp := s.now().Add(ttl).Unix()
	q := url.Values{"k": {key}, "exp": {strconv.FormatInt(exp, 10)}, "sig": {s.sign(key, exp)}}
	return s.BaseURL + "?" + q.Encode(), nil
}

// VerifySignature validates a signed URL's parameters.
func (s *FSStore) VerifySignature(key, expStr, sig string) bool {
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || s.now().Unix() > exp {
		return false
	}
	return hmac.Equal([]byte(sig), []byte(s.sign(key, exp)))
}

// ---------------------------------------------------------------------------
// S3-compatible store (MinIO in compose; any S3 in production). Bucket must be private.
// ---------------------------------------------------------------------------

type S3Store struct {
	c      *minio.Client
	bucket string
}

func NewS3Store(ctx context.Context, endpoint, access, secret, bucket string, useSSL bool) (*S3Store, error) {
	c, err := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4(access, secret, ""), Secure: useSSL})
	if err != nil {
		return nil, err
	}
	exists, err := c.BucketExists(ctx, bucket)
	if err != nil {
		return nil, fmt.Errorf("check bucket: %w", err)
	}
	if !exists {
		if err := c.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
			return nil, fmt.Errorf("create bucket: %w", err)
		}
	}
	return &S3Store{c: c, bucket: bucket}, nil
}

func (s *S3Store) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	_, err := s.c.PutObject(ctx, s.bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType})
	return err
}

func (s *S3Store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	return s.c.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
}

func (s *S3Store) SignedURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	u, err := s.c.PresignedGetObject(ctx, s.bucket, key, ttl, url.Values{})
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// ---------------------------------------------------------------------------
// Malware scanning hook. Production must plug a real scanner (e.g. ClamAV via clamd);
// the NoopScanner marks media "skipped", which is only downloadable when MEDIA_ALLOW_UNSCANNED=true (local).
// ---------------------------------------------------------------------------

type Scanner interface {
	Scan(ctx context.Context, data []byte) (status string, err error) // clean | infected | skipped
}

type NoopScanner struct{}

func (NoopScanner) Scan(context.Context, []byte) (string, error) { return "skipped", nil }
