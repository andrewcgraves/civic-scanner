package fetchtest_test

import (
	"context"
	"errors"
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch/fetchtest"
)

var _ fetch.Fetcher = (*fetchtest.Fake)(nil)

func TestReturningAnswersEveryCall(t *testing.T) {
	fake := fetchtest.Returning("<p>hi</p>")
	got, err := fake.Fetch(context.Background(), fetch.Request{URL: "https://agency.test/a"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.Status != 200 || string(got.Body) != "<p>hi</p>" || got.FinalURL != "https://agency.test/a" {
		t.Errorf("response = %+v", got)
	}
}

func TestFailingReturnsError(t *testing.T) {
	want := &fetch.Error{StatusCode: 403, Err: errors.New("blocked")}
	_, err := fetchtest.Failing(want).Fetch(context.Background(), fetch.Request{URL: "https://agency.test/a"})
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
}

func TestRespondBuildsResponsePerRequest(t *testing.T) {
	fake := &fetchtest.Fake{Respond: func(req fetch.Request) (fetch.Response, error) {
		return fetch.Response{Status: 200, Body: []byte(req.URL)}, nil
	}}
	got, _ := fake.Fetch(context.Background(), fetch.Request{URL: "https://agency.test/b"})
	if string(got.Body) != "https://agency.test/b" {
		t.Errorf("body = %q", got.Body)
	}
}

func TestZeroFakeReturnsEmptyResponse(t *testing.T) {
	var fake fetchtest.Fake
	got, err := fake.Fetch(context.Background(), fetch.Request{URL: "https://agency.test/a"})
	if err != nil || got.Status != 0 {
		t.Errorf("got %+v, %v", got, err)
	}
}

func TestCallsRecordsRequestsInOrder(t *testing.T) {
	fake := fetchtest.Returning("x")
	_, _ = fake.Fetch(context.Background(), fetch.Request{URL: "https://agency.test/1"})
	_, _ = fake.Fetch(context.Background(), fetch.Request{URL: "https://agency.test/2", MaxBytes: 5})

	calls := fake.Calls()
	if len(calls) != 2 || calls[0].URL != "https://agency.test/1" || calls[1].MaxBytes != 5 {
		t.Errorf("calls = %+v", calls)
	}
}

func TestCanceledContextFailsRetryably(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := fetchtest.Returning("x").Fetch(ctx, fetch.Request{URL: "https://agency.test/a"})
	if !errors.Is(err, context.Canceled) || !fetch.IsRetryable(err) {
		t.Errorf("err = %v, want retryable context.Canceled", err)
	}
}
