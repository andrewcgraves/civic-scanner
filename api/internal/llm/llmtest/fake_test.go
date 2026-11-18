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
	_, err := fake.Complete(ctx, llm.Request{Tier: llm.TierRoutine})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: err = %v, want context.Canceled", err)
	}
	if !llm.IsRetryable(err) {
		t.Error("canceled: reported as permanent, the gateway client reports it as retryable")
	}

	if len(fake.Calls()) != 2 {
		t.Errorf("recorded %d calls, want 2", len(fake.Calls()))
	}
}

func TestZeroFakeReturnsAUsableReply(t *testing.T) {
	var fake llmtest.Fake

	resp, err := fake.Complete(t.Context(), llm.Request{Tier: llm.TierRoutine})

	if err != nil || resp.Text == "" || resp.FinishReason != "stop" {
		t.Errorf("Complete = %+v, %v; want a non-empty stopped reply", resp, err)
	}
}

func TestReturningRepliesWithStopFinishReason(t *testing.T) {
	resp, err := llmtest.Returning("summary").Complete(t.Context(), llm.Request{Tier: llm.TierRoutine})

	if err != nil || resp.FinishReason != "stop" {
		t.Errorf("Complete = %+v, %v; want finish reason stop", resp, err)
	}
}

func TestFakeChecksRepliesLikeTheGateway(t *testing.T) {
	reply := func(resp llm.Response) *llmtest.Fake {
		return &llmtest.Fake{Respond: func(llm.Request) (llm.Response, error) { return resp, nil }}
	}

	resp, err := reply(llm.Response{Text: "partial", FinishReason: "length", Usage: llm.Usage{OutputTokens: 500}}).
		Complete(t.Context(), llm.Request{Tier: llm.TierRoutine})
	if !errors.Is(err, llm.ErrTruncated) || llm.IsRetryable(err) || resp.Usage.OutputTokens != 500 {
		t.Errorf("length: %+v, %v; want permanent ErrTruncated with usage kept", resp, err)
	}

	_, err = reply(llm.Response{FinishReason: "content_filter"}).Complete(t.Context(), llm.Request{Tier: llm.TierRoutine})
	if !errors.Is(err, llm.ErrContentFiltered) || llm.IsRetryable(err) {
		t.Errorf("content_filter: err = %v, want permanent ErrContentFiltered", err)
	}

	_, err = reply(llm.Response{FinishReason: "stop"}).Complete(t.Context(), llm.Request{Tier: llm.TierRoutine})
	if err == nil || !llm.IsRetryable(err) {
		t.Errorf("empty: err = %v, want a retryable error", err)
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
