package transcribe

import (
	"errors"
	"testing"
	"time"
)

func TestRetryDelayHonoursRetryAfterUpToAMinuteAndBacksOffOtherwise(t *testing.T) {
	p := &pass{opts: Options{RetryDelay: 5 * time.Second}}
	cases := []struct {
		err     error
		attempt int
		want    time.Duration
	}{
		{&Error{Retryable: true, RetryAfter: 2 * time.Second, Err: errors.New("429")}, 0, 2 * time.Second},
		{&Error{Retryable: true, RetryAfter: 5 * time.Minute, Err: errors.New("429")}, 0, time.Minute},
		{&Error{Retryable: true, Err: errors.New("503")}, 0, 5 * time.Second},
		{&Error{Retryable: true, Err: errors.New("503")}, 2, 20 * time.Second},
		{errors.New("other"), 30, time.Minute},
	}
	for _, tc := range cases {
		if got := p.retryDelay(tc.err, tc.attempt); got != tc.want {
			t.Errorf("retryDelay(%v, %d) = %s, want %s", tc.err, tc.attempt, got, tc.want)
		}
	}
}
