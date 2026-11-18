package llmtest_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/llm"
	"github.com/andrewcgraves/civic-scanner/api/internal/llm/llmtest"
)

func summarize(ctx context.Context, client llm.Client, text string) (string, error) {
	resp, err := client.Complete(ctx, llm.Request{Tier: llm.TierRoutine, System: "Summarize.", User: text})
	return resp.Text, err
}

func TestReturningRepliesWithTextAndRecordsCalls(t *testing.T) {
	fake := llmtest.Returning("summary")

	got, err := summarize(t.Context(), fake, "agenda")
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if got != "summary" {
		t.Errorf("text = %q, want summary", got)
	}
	want := []llm.Request{{Tier: llm.TierRoutine, System: "Summarize.", User: "agenda"}}
	if !slices.Equal(fake.Calls(), want) {
		t.Errorf("calls = %+v, want %+v", fake.Calls(), want)
	}
}

func TestFailingReturnsTheError(t *testing.T) {
	boom := &llm.Error{StatusCode: 503, Retryable: true, Err: errors.New("unavailable")}

	_, err := summarize(t.Context(), llmtest.Failing(boom), "agenda")

	if !errors.Is(err, boom) || !llm.IsRetryable(err) {
		t.Errorf("err = %v, want the retryable error", err)
	}
}

func TestRespondSeesEachRequest(t *testing.T) {
	fake := &llmtest.Fake{Respond: func(req llm.Request) (llm.Response, error) {
		return llm.Response{Text: string(req.Tier) + ":" + req.User, Usage: llm.Usage{InputTokens: 3, OutputTokens: 1}}, nil
	}}

	resp, err := fake.Complete(t.Context(), llm.Request{Tier: llm.TierStrong, User: "q"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Text != "strong:q" || resp.Usage.InputTokens != 3 {
		t.Errorf("response = %+v", resp)
	}
}

func TestFakeBehavesLikeTheGatewayOnBadInput(t *testing.T) {
	fake := llmtest.Returning("never")

	if _, err := fake.Complete(t.Context(), llm.Request{Tier: "premium"}); !errors.Is(err, llm.ErrUnknownTier) {
		t.Errorf("unknown tier: err = %v, want ErrUnknownTier", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := fake.Complete(ctx, llm.Request{Tier: llm.TierRoutine}); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: err = %v, want context.Canceled", err)
	}

	if len(fake.Calls()) != 2 {
		t.Errorf("recorded %d calls, want 2", len(fake.Calls()))
	}
}

func TestZeroFakeReturnsEmptyResponse(t *testing.T) {
	var fake llmtest.Fake

	resp, err := fake.Complete(t.Context(), llm.Request{Tier: llm.TierRoutine})

	if err != nil || resp != (llm.Response{}) {
		t.Errorf("Complete = %+v, %v; want zero response, nil", resp, err)
	}
}

func TestFakeIsSafeForConcurrentUse(t *testing.T) {
	fake := llmtest.Returning("ok")
	done := make(chan struct{})
	for range 8 {
		go func() {
			_, _ = fake.Complete(t.Context(), llm.Request{Tier: llm.TierRoutine})
			done <- struct{}{}
		}()
	}
	for range 8 {
		<-done
	}
	if len(fake.Calls()) != 8 {
		t.Errorf("recorded %d calls, want 8", len(fake.Calls()))
	}
}
