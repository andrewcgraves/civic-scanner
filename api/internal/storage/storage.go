package storage

import (
	"context"
	"io"
	"time"
)

type Info struct {
	Key          string
	Size         int64
	LastModified time.Time
}

type Store interface {
	Put(ctx context.Context, key string, body io.Reader) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
	Stat(ctx context.Context, key string) (Info, error)
	ListOlderThan(ctx context.Context, cutoff time.Time) ([]string, error)
	ListPrefix(ctx context.Context, prefix string) ([]string, error)
}
