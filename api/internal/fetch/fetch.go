package fetch

import "context"

type Request struct {
	URL      string
	MaxBytes int64
}

type Response struct {
	Status      int
	ContentType string
	Body        []byte
	FinalURL    string
}

type Fetcher interface {
	Fetch(ctx context.Context, req Request) (Response, error)
}
