package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
)

const (
	maxRetries = 2
	partSize   = 16 << 20
)

type s3Store struct {
	client *minio.Client
	bucket string
}

func New(cfg config.Storage) (Store, error) {
	if !cfg.Configured() {
		return unconfigured{}, nil
	}
	if cfg.Bucket == "" || cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return nil, fmt.Errorf("storage: STORAGE_BUCKET, STORAGE_ACCESS_KEY_ID and STORAGE_SECRET_ACCESS_KEY are required once STORAGE_ENDPOINT is set")
	}
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("storage: endpoint: %w", err)
	}
	lookup := minio.BucketLookupDNS
	if cfg.PathStyle {
		lookup = minio.BucketLookupPath
	}
	client, err := minio.New(endpoint.Host, &minio.Options{
		Creds:        credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		Secure:       endpoint.Scheme == "https",
		Region:       cfg.Region,
		BucketLookup: lookup,
		MaxRetries:   maxRetries,
	})
	if err != nil {
		return nil, fmt.Errorf("storage: %w", err)
	}
	return &s3Store{client: client, bucket: cfg.Bucket}, nil
}

func (s *s3Store) Put(ctx context.Context, key string, body io.Reader) error {
	_, err := s.client.PutObject(ctx, s.bucket, key, body, -1, minio.PutObjectOptions{
		ContentType: "application/octet-stream",
		PartSize:    partSize,
	})
	return classify(err, key)
}

func (s *s3Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, classify(err, key)
	}
	if _, err := obj.Stat(); err != nil {
		_ = obj.Close()
		return nil, classify(err, key)
	}
	return &reader{obj: obj, key: key}, nil
}

func (s *s3Store) Delete(ctx context.Context, key string) error {
	err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
	return classify(err, key)
}

func (s *s3Store) Stat(ctx context.Context, key string) (Info, error) {
	st, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return Info{}, classify(err, key)
	}
	return Info{Key: key, Size: st.Size, LastModified: st.LastModified}, nil
}

func (s *s3Store) ListOlderThan(ctx context.Context, cutoff time.Time) ([]string, error) {
	var keys []string
	for obj := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Recursive: true}) {
		if obj.Err != nil {
			return nil, classify(obj.Err, "")
		}
		if obj.LastModified.Before(cutoff) {
			keys = append(keys, obj.Key)
		}
	}
	return keys, nil
}

type reader struct {
	obj *minio.Object
	key string
}

func (r *reader) Read(p []byte) (int, error) {
	n, err := r.obj.Read(p)
	if errors.Is(err, io.EOF) {
		return n, err
	}
	return n, classify(err, r.key)
}

func (r *reader) Close() error { return r.obj.Close() }

type unconfigured struct{}

func (unconfigured) Put(context.Context, string, io.Reader) error { return ErrNotConfigured }

func (unconfigured) Get(context.Context, string) (io.ReadCloser, error) {
	return nil, ErrNotConfigured
}

func (unconfigured) Delete(context.Context, string) error { return ErrNotConfigured }

func (unconfigured) Stat(context.Context, string) (Info, error) { return Info{}, ErrNotConfigured }

func (unconfigured) ListOlderThan(context.Context, time.Time) ([]string, error) {
	return nil, ErrNotConfigured
}
