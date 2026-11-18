package fetch_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch/fetchtest"
)

func statusFailure(code int) error {
	return &fetch.Error{StatusCode: code, Retryable: code == 429 || code >= 500, Err: errors.New("plain failed")}
}

func TestFallbackSkipsSecondaryOnSuccess(t *testing.T) {
	primary := fetchtest.Returning("plain")
	secondary := fetchtest.Returning("firecrawl")

	got, err := fetch.NewFallback(primary, secondary).Fetch(context.Background(), fetch.Request{URL: "https://agency.test/a"})
	if err != nil || string(got.Body) != "plain" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if n := len(secondary.Calls()); n != 0 {
		t.Errorf("secondary called %d times", n)
	}
}

func TestFallbackUsesSecondaryOnBotBlockStatuses(t *testing.T) {
	for _, code := range []int{401, 403, 406, 429, 999} {
		secondary := fetchtest.Returning("firecrawl")
		got, err := fetch.NewFallback(fetchtest.Failing(statusFailure(code)), secondary).Fetch(context.Background(), fetch.Request{URL: "https://agency.test/a", MaxBytes: 9})
		if err != nil || string(got.Body) != "firecrawl" {
			t.Errorf("status %d: got %+v, %v", code, got, err)
		}
		if calls := secondary.Calls(); len(calls) != 1 || calls[0].MaxBytes != 9 {
			t.Errorf("status %d: secondary calls = %+v", code, calls)
		}
	}
}

func TestFallbackKeepsPrimaryErrorForOtherFailures(t *testing.T) {
	failures := []error{
		statusFailure(404),
		statusFailure(410),
		statusFailure(500),
		statusFailure(503),
		&fetch.Error{Retryable: true, Err: errors.New("connection refused")},
		&fetch.Error{Err: fetch.ErrTooLarge},
		&fetch.Error{Err: fetch.ErrTooManyRedirects},
		errors.New("plain"),
	}
	for _, failure := range failures {
		secondary := fetchtest.Returning("firecrawl")
		_, err := fetch.NewFallback(fetchtest.Failing(failure), secondary).Fetch(context.Background(), fetch.Request{URL: "https://agency.test/a"})
		if err != failure {
			t.Errorf("err = %v, want %v", err, failure)
		}
		if n := len(secondary.Calls()); n != 0 {
			t.Errorf("%v: secondary called %d times", failure, n)
		}
	}
}

func TestFallbackReportsPrimaryErrorWhenSecondaryNotConfigured(t *testing.T) {
	primary := statusFailure(403)
	_, err := fetch.NewFallback(fetchtest.Failing(primary), fetchtest.Failing(fetch.ErrNotConfigured)).Fetch(context.Background(), fetch.Request{URL: "https://agency.test/a"})
	if err != primary {
		t.Errorf("err = %v, want the plain 403", err)
	}
}

func TestFallbackReportsSecondaryError(t *testing.T) {
	secondaryErr := &fetch.Error{StatusCode: 402, Err: errors.New("out of credits")}
	_, err := fetch.NewFallback(fetchtest.Failing(statusFailure(403)), fetchtest.Failing(secondaryErr)).Fetch(context.Background(), fetch.Request{URL: "https://agency.test/a"})
	if err != secondaryErr {
		t.Errorf("err = %v, want %v", err, secondaryErr)
	}
}

func TestNewFallsBackToFirecrawlWhenSiteBlocksPlainHTTP(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer site.Close()
	firecrawl := firecrawlServer(t, 200, `{"success":true,"data":{"markdown":"rendered","metadata":{"statusCode":200}}}`)
	defer firecrawl.Close()

	cfg := plainConfig()
	cfg.FirecrawlAPIKey = "fc-key"
	cfg.FirecrawlBaseURL = firecrawl.URL
	cfg.FirecrawlTimeout = cfg.Timeout

	got, err := fetch.New(cfg).Fetch(context.Background(), fetch.Request{URL: site.URL})
	if err != nil || string(got.Body) != "rendered" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestNewWithoutFirecrawlKeyReturnsPlainBlock(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer site.Close()

	_, err := fetch.New(plainConfig()).Fetch(context.Background(), fetch.Request{URL: site.URL})
	var fe *fetch.Error
	if !errors.As(err, &fe) || fe.StatusCode != 403 {
		t.Fatalf("err = %v, want plain 403", err)
	}
}
