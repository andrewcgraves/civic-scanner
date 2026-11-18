package fetchtest

import (
	"context"
	"slices"
	"sync"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
)

type Fake struct {
	Respond func(fetch.Request) (fetch.Response, error)

	mu    sync.Mutex
	calls []fetch.Request
}

func Returning(body string) *Fake {
	return &Fake{Respond: func(req fetch.Request) (fetch.Response, error) {
		return fetch.Response{
			Status:      200,
			ContentType: "text/html; charset=utf-8",
			Body:        []byte(body),
			FinalURL:    req.URL,
		}, nil
	}}
}

func Failing(err error) *Fake {
	return &Fake{Respond: func(fetch.Request) (fetch.Response, error) {
		return fetch.Response{}, err
	}}
}

func (f *Fake) Fetch(ctx context.Context, req fetch.Request) (fetch.Response, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	f.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return fetch.Response{}, &fetch.Error{Retryable: true, Err: err}
	}
	if f.Respond == nil {
		return fetch.Response{}, nil
	}
	return f.Respond(req)
}

func (f *Fake) Calls() []fetch.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}
