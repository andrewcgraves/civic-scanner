package transcribetest_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/transcribe"
	"github.com/andrewcgraves/civic-scanner/api/internal/transcribe/transcribetest"
)

func wordCount(ctx context.Context, client transcribe.Client, path string) (int, error) {
	got, err := client.Transcribe(ctx, path)
	return len(got.Segments), err
}

func TestReturningRepliesAndRecordsCalls(t *testing.T) {
	fake := transcribetest.Returning(transcribe.Transcript{
		Text:     "hello",
		Segments: []transcribe.Segment{{End: time.Second, Text: "hello"}},
	})

	n, err := wordCount(t.Context(), fake, "a.mp3")
	if err != nil || n != 1 {
		t.Fatalf("got %d, %v", n, err)
	}
	if !slices.Equal(fake.Calls(), []string{"a.mp3"}) {
		t.Errorf("calls = %v", fake.Calls())
	}
}

func TestZeroFakeRepliesWithPlaceholder(t *testing.T) {
	n, err := wordCount(t.Context(), &transcribetest.Fake{}, "a.mp3")

	if err != nil || n != 1 {
		t.Errorf("got %d, %v", n, err)
	}
}

func TestFailingReturnsTheError(t *testing.T) {
	boom := &transcribe.Error{Retryable: true, Err: errors.New("boom")}

	_, err := wordCount(t.Context(), transcribetest.Failing(boom), "a.mp3")

	if !errors.Is(err, boom) || !transcribe.IsRetryable(err) {
		t.Errorf("err = %v", err)
	}
}

func TestCanceledContextIsRetryable(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := wordCount(ctx, transcribetest.Returning(transcribe.Transcript{}), "a.mp3")

	if !transcribe.IsRetryable(err) || !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}
