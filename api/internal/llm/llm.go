package llm

import "context"

type Tier string

const (
	TierRoutine Tier = "routine"
	TierStrong  Tier = "strong"
)

func (t Tier) Valid() bool { return t == TierRoutine || t == TierStrong }

type Request struct {
	Tier   Tier
	System string
	User   string
}

type Usage struct {
	InputTokens  int
	OutputTokens int
}

type Response struct {
	Text  string
	Model string
	Usage Usage
}

type Client interface {
	Complete(ctx context.Context, req Request) (Response, error)
}
