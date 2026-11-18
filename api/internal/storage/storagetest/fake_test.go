package storagetest_test

import (
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/storage"
	"github.com/andrewcgraves/civic-scanner/api/internal/storage/storagetest"
)

var _ storage.Store = (*storagetest.Fake)(nil)

func TestFakeRoundTrip(t *testing.T) {
	fake := storagetest.New()

	if err := fake.Put(t.Context(), "meetings/1/audio.mp3", strings.NewReader("sound")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	r, err := fake.Get(t.Context(), "meetings/1/audio.mp3")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer r.Close()
	got, _ := io.ReadAll(r)
	if string(got) != "sound" {
		t.Errorf("body = %q, want sound", got)
	}
	info, err := fake.Stat(t.Context(), "meetings/1/audio.mp3")
	if err != nil || info.Size != 5 || info.Key != "meetings/1/audio.mp3" {
		t.Errorf("Stat = %+v, %v; want key and size 5", info, err)
	}
}

func TestFakeMissingKeyIsNotFound(t *testing.T) {
	fake := storagetest.New()

	_, getErr := fake.Get(t.Context(), "nope")
	_, statErr := fake.Stat(t.Context(), "nope")

	if !errors.Is(getErr, storage.ErrNotFound) || !errors.Is(statErr, storage.ErrNotFound) {
		t.Errorf("Get = %v, Stat = %v; want ErrNotFound", getErr, statErr)
	}
}

func TestFakeDeleteIsIdempotent(t *testing.T) {
	fake := storagetest.New()
	fake.Put(t.Context(), "k", strings.NewReader("x"))

	if err := fake.Delete(t.Context(), "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := fake.Delete(t.Context(), "k"); err != nil {
		t.Errorf("Delete of a missing key: %v", err)
	}
	if _, err := fake.Stat(t.Context(), "k"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Stat after Delete = %v, want ErrNotFound", err)
	}
}

func TestFakePutReplacesAndCopiesBody(t *testing.T) {
	fake := storagetest.New()
	fake.Put(t.Context(), "k", strings.NewReader("first"))
	fake.Put(t.Context(), "k", strings.NewReader("second"))

	r, _ := fake.Get(t.Context(), "k")
	got, _ := io.ReadAll(r)
	if string(got) != "second" {
		t.Errorf("body = %q, want second", got)
	}
}

func TestFakeListOlderThanUsesTheClock(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := storagetest.New()
	fake.Now = func() time.Time { return now }
	fake.Put(t.Context(), "old", strings.NewReader("x"))
	now = now.Add(48 * time.Hour)
	fake.Put(t.Context(), "new", strings.NewReader("x"))

	keys, err := fake.ListOlderThan(t.Context(), now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("ListOlderThan: %v", err)
	}
	if !slices.Equal(keys, []string{"old"}) {
		t.Errorf("keys = %v, want [old]", keys)
	}
}

func TestFailingReturnsTheErrorFromEveryOperation(t *testing.T) {
	boom := &storage.Error{Retryable: true, Err: errors.New("unavailable")}
	fake := storagetest.Failing(boom)

	errs := []error{
		fake.Put(t.Context(), "k", strings.NewReader("x")),
		fake.Delete(t.Context(), "k"),
	}
	_, getErr := fake.Get(t.Context(), "k")
	_, statErr := fake.Stat(t.Context(), "k")
	_, listErr := fake.ListOlderThan(t.Context(), time.Now())
	errs = append(errs, getErr, statErr, listErr)

	for i, err := range errs {
		if !errors.Is(err, boom) || !storage.IsRetryable(err) {
			t.Errorf("operation %d error = %v, want the retryable error", i, err)
		}
	}
}
