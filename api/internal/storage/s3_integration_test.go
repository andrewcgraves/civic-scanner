//go:build integration

package storage_test

import (
	"crypto/sha256"
	"errors"
	"io"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/storage"
)

func realStore(t *testing.T) storage.Store {
	t.Helper()
	endpoint := os.Getenv("STORAGE_ENDPOINT")
	if endpoint == "" {
		t.Fatal("STORAGE_ENDPOINT must be set for integration tests")
	}
	store, err := storage.New(config.Storage{
		Endpoint:        endpoint,
		Region:          "auto",
		Bucket:          os.Getenv("STORAGE_BUCKET"),
		AccessKeyID:     os.Getenv("STORAGE_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("STORAGE_SECRET_ACCESS_KEY"),
		PathStyle:       os.Getenv("STORAGE_PATH_STYLE") == "true",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return store
}

func uniqueKey(t *testing.T, name string) string {
	t.Helper()
	key := "it/" + uuid.NewString() + "/" + name
	t.Cleanup(func() {
		realStore(t).Delete(t.Context(), key)
	})
	return key
}

func TestIntegrationRoundTrip(t *testing.T) {
	store := realStore(t)
	key := uniqueKey(t, "audio.mp3")

	if err := store.Put(t.Context(), key, strings.NewReader("public comment")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	r, err := store.Get(t.Context(), key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, err := io.ReadAll(r)
	r.Close()
	if err != nil || string(got) != "public comment" {
		t.Errorf("Get body = %q, %v; want public comment", got, err)
	}
	info, err := store.Stat(t.Context(), key)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Key != key || info.Size != int64(len("public comment")) || info.LastModified.IsZero() {
		t.Errorf("Stat = %+v, want key, size 14 and a modified time", info)
	}
}

func TestIntegrationMissingKey(t *testing.T) {
	store := realStore(t)
	key := "it/" + uuid.NewString() + "/missing"

	_, getErr := store.Get(t.Context(), key)
	_, statErr := store.Stat(t.Context(), key)

	for name, err := range map[string]error{"Get": getErr, "Stat": statErr} {
		if !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("%s error = %v, want ErrNotFound", name, err)
		}
		if storage.IsRetryable(err) {
			t.Errorf("%s error is retryable, want permanent", name)
		}
	}
}

func TestIntegrationStreamsLargeObject(t *testing.T) {
	store := realStore(t)
	key := uniqueKey(t, "meeting.opus")
	const size = 40 << 20
	rng := rand.New(rand.NewPCG(1, 2))
	want := sha256.New()
	source := io.TeeReader(io.LimitReader(randomReader{rng}, size), want)

	if err := store.Put(t.Context(), key, source); err != nil {
		t.Fatalf("Put: %v", err)
	}

	info, err := store.Stat(t.Context(), key)
	if err != nil || info.Size != size {
		t.Fatalf("Stat = %+v, %v; want size %d", info, err, size)
	}
	r, err := store.Get(t.Context(), key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer r.Close()
	got := sha256.New()
	n, err := io.Copy(got, r)
	if err != nil || n != size {
		t.Fatalf("read %d bytes, %v; want %d", n, err, size)
	}
	if string(got.Sum(nil)) != string(want.Sum(nil)) {
		t.Error("downloaded bytes differ from uploaded bytes")
	}
}

func TestIntegrationDelete(t *testing.T) {
	store := realStore(t)
	key := uniqueKey(t, "gone.mp3")
	if err := store.Put(t.Context(), key, strings.NewReader("x")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := store.Delete(t.Context(), key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Stat(t.Context(), key); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Stat after Delete = %v, want ErrNotFound", err)
	}
	if err := store.Delete(t.Context(), key); err != nil {
		t.Errorf("Delete of a missing key: %v", err)
	}
}

func TestIntegrationListOlderThan(t *testing.T) {
	store := realStore(t)
	older := uniqueKey(t, "older.mp3")
	newer := uniqueKey(t, "newer.mp3")
	if err := store.Put(t.Context(), older, strings.NewReader("x")); err != nil {
		t.Fatalf("Put older: %v", err)
	}
	time.Sleep(2 * time.Second)
	cutoff := time.Now()
	time.Sleep(2 * time.Second)
	if err := store.Put(t.Context(), newer, strings.NewReader("x")); err != nil {
		t.Fatalf("Put newer: %v", err)
	}

	keys, err := store.ListOlderThan(t.Context(), cutoff)
	if err != nil {
		t.Fatalf("ListOlderThan: %v", err)
	}

	if !slices.Contains(keys, older) {
		t.Errorf("keys do not include %q", older)
	}
	if slices.Contains(keys, newer) {
		t.Errorf("keys include %q, which is newer than the cutoff", newer)
	}
}

func TestIntegrationMissingBucketIsPermanent(t *testing.T) {
	endpoint := os.Getenv("STORAGE_ENDPOINT")
	if endpoint == "" {
		t.Fatal("STORAGE_ENDPOINT must be set for integration tests")
	}
	store, err := storage.New(config.Storage{
		Endpoint:        endpoint,
		Region:          "auto",
		Bucket:          "no-such-bucket-" + uuid.NewString(),
		AccessKeyID:     "k",
		SecretAccessKey: "s",
		PathStyle:       true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = store.Put(t.Context(), "k", strings.NewReader("x"))

	if err == nil || storage.IsRetryable(err) || errors.Is(err, storage.ErrNotFound) {
		t.Errorf("err = %v, want a permanent error that is not ErrNotFound", err)
	}
}

type randomReader struct{ rng *rand.Rand }

func (r randomReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(r.rng.Uint32())
	}
	return len(p), nil
}
