package llmtest

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/andrewcgraves/civic-scanner/api/internal/llm"
)

type Fake struct {
	Respond func(llm.Request) (llm.Response, error)

	mu    sync.Mutex
	calls []llm.Request
}

func Returning(text string) *Fake {
	return &Fake{Respond: func(llm.Request) (llm.Response, error) {
		return llm.Response{Text: text, FinishReason: "stop"}, nil
	}}
}

func Failing(err error) *Fake {
	return &Fake{Respond: func(llm.Request) (llm.Response, error) {
		return llm.Response{}, err
	}}
}

func (f *Fake) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	f.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return llm.Response{}, &llm.Error{Retryable: true, Err: err}
	}
	if !req.Tier.Valid() {
		return llm.Response{}, fmt.Errorf("%w %q", llm.ErrUnknownTier, req.Tier)
	}
	if f.Respond == nil {
		return llm.Response{Text: "fake reply", FinishReason: "stop"}, nil
	}
	resp, err := f.Respond(req)
	if err != nil {
		return resp, err
	}
	return resp, llm.CheckReply(resp)
}

func (f *Fake) Calls() []llm.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}
