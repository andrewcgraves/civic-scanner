package storage_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/storage"
)

func TestUnconfiguredStoreReturnsNotConfigured(t *testing.T) {
	store, err := storage.New(config.Storage{})
	if err != nil {
		t.Fatalf("New with no settings: %v", err)
	}
	ctx := t.Context()

	calls := map[string]func() error{
		"Put":           func() error { return store.Put(ctx, "k", strings.NewReader("x")) },
		"Get":           func() error { _, err := store.Get(ctx, "k"); return err },
		"Delete":        func() error { return store.Delete(ctx, "k") },
		"Stat":          func() error { _, err := store.Stat(ctx, "k"); return err },
		"ListOlderThan": func() error { _, err := store.ListOlderThan(ctx, time.Now()); return err },
	}
	for name, call := range calls {
		err := call()
		if !errors.Is(err, storage.ErrNotConfigured) {
			t.Errorf("%s error = %v, want ErrNotConfigured", name, err)
		}
		if storage.IsRetryable(err) {
			t.Errorf("%s error is retryable, want permanent", name)
		}
	}
}

func TestNewRejectsPartialConfig(t *testing.T) {
	_, err := storage.New(config.Storage{Endpoint: "http://localhost:9000", Region: "auto"})
	if err == nil {
		t.Fatal("New with only an endpoint succeeded, want error")
	}
}

func serverReplying(t *testing.T, status int) config.Storage {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return config.Storage{
		Endpoint:        srv.URL,
		Region:          "auto",
		Bucket:          "audio",
		AccessKeyID:     "key",
		SecretAccessKey: "secret",
		PathStyle:       true,
	}
}

func TestErrorsAreClassified(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		notFound  bool
		retryable bool
	}{
		{"missing object", http.StatusNotFound, true, false},
		{"forbidden", http.StatusForbidden, false, false},
		{"bad request", http.StatusBadRequest, false, false},
		{"throttled", http.StatusTooManyRequests, false, true},
		{"request timeout", http.StatusRequestTimeout, false, true},
		{"server error", http.StatusInternalServerError, false, true},
		{"unavailable", http.StatusServiceUnavailable, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, err := storage.New(serverReplying(t, tc.status))
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			_, statErr := store.Stat(t.Context(), "k")
			_, getErr := store.Get(t.Context(), "k")
			putErr := store.Put(t.Context(), "k", strings.NewReader("x"))

			for name, err := range map[string]error{"Stat": statErr, "Get": getErr, "Put": putErr} {
				if err == nil {
					t.Fatalf("%s succeeded, want error", name)
				}
				if got := errors.Is(err, storage.ErrNotFound); name != "Put" && got != tc.notFound {
					t.Errorf("%s ErrNotFound = %v, want %v (err %v)", name, got, tc.notFound, err)
				}
				if got := storage.IsRetryable(err); got != tc.retryable {
					t.Errorf("%s retryable = %v, want %v (err %v)", name, got, tc.retryable, err)
				}
			}
		})
	}
}

func TestUnreachableEndpointIsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	cfg := config.Storage{Endpoint: srv.URL, Region: "auto", Bucket: "audio", AccessKeyID: "k", SecretAccessKey: "s", PathStyle: true}
	srv.Close()
	store, err := storage.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = store.Stat(t.Context(), "k")

	if !storage.IsRetryable(err) {
		t.Errorf("err = %v, want retryable", err)
	}
}
