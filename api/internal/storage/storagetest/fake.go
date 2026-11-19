package storagetest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/storage"
)

type object struct {
	data     []byte
	modified time.Time
}

type Fake struct {
	Now func() time.Time

	mu      sync.Mutex
	objects map[string]object
	err     error
}

func New() *Fake {
	return &Fake{Now: time.Now, objects: map[string]object{}}
}

func Failing(err error) *Fake {
	f := New()
	f.err = err
	return f
}

func (f *Fake) Put(_ context.Context, key string, body io.Reader) error {
	if f.err != nil {
		return f.err
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = object{data: data, modified: f.Now()}
	return nil
}

func (f *Fake) Get(_ context.Context, key string) (io.ReadCloser, error) {
	obj, err := f.find(key)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(obj.data)), nil
}

func (f *Fake) Delete(_ context.Context, key string) error {
	if f.err != nil {
		return f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, key)
	return nil
}

func (f *Fake) Stat(_ context.Context, key string) (storage.Info, error) {
	obj, err := f.find(key)
	if err != nil {
		return storage.Info{}, err
	}
	return storage.Info{Key: key, Size: int64(len(obj.data)), LastModified: obj.modified}, nil
}

func (f *Fake) ListOlderThan(_ context.Context, cutoff time.Time) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var keys []string
	for key, obj := range f.objects {
		if obj.modified.Before(cutoff) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys, nil
}

func (f *Fake) ListPrefix(_ context.Context, prefix string) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var keys []string
	for key := range f.objects {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys, nil
}

func (f *Fake) find(key string) (object, error) {
	if f.err != nil {
		return object{}, f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, ok := f.objects[key]
	if !ok {
		return object{}, fmt.Errorf("%w: %q", storage.ErrNotFound, key)
	}
	return obj, nil
}
