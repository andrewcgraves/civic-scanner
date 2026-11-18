package fetch_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
)

func firecrawlConfig(baseURL string) config.Fetch {
	return config.Fetch{
		FirecrawlAPIKey:  "fc-key",
		FirecrawlBaseURL: baseURL,
		FirecrawlTimeout: 5 * time.Second,
		MaxBytes:         1 << 20,
	}
}

func firecrawlServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestFirecrawlWithoutKeyIsNotConfigured(t *testing.T) {
	cfg := firecrawlConfig("http://unused.test")
	cfg.FirecrawlAPIKey = ""
	_, err := fetch.NewFirecrawl(cfg).Fetch(context.Background(), fetch.Request{URL: "https://agency.test/agenda"})
	if !errors.Is(err, fetch.ErrNotConfigured) {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}
	if fetch.IsRetryable(err) {
		t.Error("not configured must be permanent")
	}
}

func TestFirecrawlSendsScrapeRequest(t *testing.T) {
	var gotPath, gotAuth, gotType string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotType = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"success":true,"data":{"markdown":"# Agenda","metadata":{"statusCode":200,"url":"https://agency.test/final"}}}`))
	}))
	defer srv.Close()

	got, err := fetch.NewFirecrawl(firecrawlConfig(srv.URL)).Fetch(context.Background(), fetch.Request{URL: "https://agency.test/agenda"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotPath != "/v2/scrape" || gotAuth != "Bearer fc-key" || gotType != "application/json" {
		t.Errorf("path=%q auth=%q type=%q", gotPath, gotAuth, gotType)
	}
	if gotBody["url"] != "https://agency.test/agenda" {
		t.Errorf("url = %v", gotBody["url"])
	}
	formats, _ := gotBody["formats"].([]any)
	if len(formats) != 1 || formats[0] != "markdown" {
		t.Errorf("formats = %v", gotBody["formats"])
	}
	if gotBody["maxAge"] != float64(0) {
		t.Errorf("maxAge = %v, want 0 for a live fetch", gotBody["maxAge"])
	}
	if got.Status != 200 || string(got.Body) != "# Agenda" || got.FinalURL != "https://agency.test/final" {
		t.Errorf("response = %+v", got)
	}
	if !strings.HasPrefix(got.ContentType, "text/markdown") {
		t.Errorf("ContentType = %q", got.ContentType)
	}
}

func TestFirecrawlDefaultsStatusAndFinalURL(t *testing.T) {
	srv := firecrawlServer(t, 200, `{"success":true,"data":{"markdown":"hi","metadata":{}}}`)
	defer srv.Close()

	got, err := fetch.NewFirecrawl(firecrawlConfig(srv.URL)).Fetch(context.Background(), fetch.Request{URL: "https://agency.test/a"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.Status != 200 || got.FinalURL != "https://agency.test/a" {
		t.Errorf("response = %+v", got)
	}
}

func TestFirecrawlTargetStatusErrors(t *testing.T) {
	tests := []struct {
		status    int
		retryable bool
	}{
		{404, false},
		{403, false},
		{429, true},
		{500, true},
		{503, true},
	}
	for _, tt := range tests {
		body, _ := json.Marshal(map[string]any{
			"success": true,
			"data":    map[string]any{"markdown": "", "metadata": map[string]any{"statusCode": tt.status}},
		})
		srv := firecrawlServer(t, 200, string(body))
		_, err := fetch.NewFirecrawl(firecrawlConfig(srv.URL)).Fetch(context.Background(), fetch.Request{URL: "https://agency.test/a"})
		srv.Close()

		var fe *fetch.Error
		if !errors.As(err, &fe) || fe.StatusCode != tt.status {
			t.Fatalf("target %d: err = %v", tt.status, err)
		}
		if fetch.IsRetryable(err) != tt.retryable {
			t.Errorf("target %d: IsRetryable = %v", tt.status, fetch.IsRetryable(err))
		}
	}
}

func TestFirecrawlAPIStatusErrors(t *testing.T) {
	tests := []struct {
		status    int
		retryable bool
	}{
		{400, false},
		{401, false},
		{402, false},
		{408, true},
		{429, true},
		{500, true},
		{502, true},
	}
	for _, tt := range tests {
		srv := firecrawlServer(t, tt.status, `{"success":false,"error":"nope from firecrawl"}`)
		_, err := fetch.NewFirecrawl(firecrawlConfig(srv.URL)).Fetch(context.Background(), fetch.Request{URL: "https://agency.test/a"})
		srv.Close()

		var fe *fetch.Error
		if !errors.As(err, &fe) || fe.StatusCode != tt.status {
			t.Fatalf("api %d: err = %v", tt.status, err)
		}
		if !strings.Contains(err.Error(), "nope from firecrawl") {
			t.Errorf("api %d: message missing from %q", tt.status, err)
		}
		if fetch.IsRetryable(err) != tt.retryable {
			t.Errorf("api %d: IsRetryable = %v", tt.status, fetch.IsRetryable(err))
		}
	}
}

func TestFirecrawlReadsRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "12")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	_, err := fetch.NewFirecrawl(firecrawlConfig(srv.URL)).Fetch(context.Background(), fetch.Request{URL: "https://agency.test/a"})
	var fe *fetch.Error
	if !errors.As(err, &fe) || fe.RetryAfter != 12*time.Second {
		t.Fatalf("err = %v, want RetryAfter 12s", err)
	}
}

func TestFirecrawlUnusableSuccessResponsesAreRetryable(t *testing.T) {
	for _, body := range []string{`not json`, `{"success":false,"error":"scrape failed"}`, `{"success":true}`} {
		srv := firecrawlServer(t, 200, body)
		_, err := fetch.NewFirecrawl(firecrawlConfig(srv.URL)).Fetch(context.Background(), fetch.Request{URL: "https://agency.test/a"})
		srv.Close()
		if err == nil || !fetch.IsRetryable(err) {
			t.Errorf("body %q: err = %v, want retryable", body, err)
		}
	}
}

func TestFirecrawlRejectsContentOverCap(t *testing.T) {
	srv := firecrawlServer(t, 200, `{"success":true,"data":{"markdown":"`+strings.Repeat("x", 200)+`","metadata":{}}}`)
	defer srv.Close()

	_, err := fetch.NewFirecrawl(firecrawlConfig(srv.URL)).Fetch(context.Background(), fetch.Request{URL: "https://agency.test/a", MaxBytes: 100})
	if !errors.Is(err, fetch.ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if fetch.IsRetryable(err) {
		t.Error("too large must be permanent")
	}
}

func TestFirecrawlRejectsOversizedAPIResponse(t *testing.T) {
	srv := firecrawlServer(t, 200, `{"success":true,"data":{"markdown":"`+strings.Repeat("x", 3<<20)+`","metadata":{}}}`)
	defer srv.Close()

	cfg := firecrawlConfig(srv.URL)
	cfg.MaxBytes = 100
	_, err := fetch.NewFirecrawl(cfg).Fetch(context.Background(), fetch.Request{URL: "https://agency.test/a"})
	if !errors.Is(err, fetch.ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestFirecrawlNetworkFailureIsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()

	_, err := fetch.NewFirecrawl(firecrawlConfig(base)).Fetch(context.Background(), fetch.Request{URL: "https://agency.test/a"})
	if err == nil || !fetch.IsRetryable(err) {
		t.Fatalf("err = %v, want retryable", err)
	}
}

func TestFirecrawlRejectsBadTargetURL(t *testing.T) {
	_, err := fetch.NewFirecrawl(firecrawlConfig("http://unused.test")).Fetch(context.Background(), fetch.Request{URL: "ftp://agency.test/a"})
	if !errors.Is(err, fetch.ErrInvalidURL) || fetch.IsRetryable(err) {
		t.Fatalf("err = %v, want permanent ErrInvalidURL", err)
	}
}

func TestFirecrawlBadBaseURLIsPermanent(t *testing.T) {
	cfg := firecrawlConfig("://bad")
	_, err := fetch.NewFirecrawl(cfg).Fetch(context.Background(), fetch.Request{URL: "https://agency.test/a"})
	if err == nil || fetch.IsRetryable(err) {
		t.Fatalf("err = %v, want permanent", err)
	}
}
