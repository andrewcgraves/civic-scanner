package transcribe

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

type errorBody struct {
	Message string `json:"message"`
	Error   struct {
		Message string `json:"message"`
	} `json:"error"`
}

func send(client *http.Client, req *http.Request, provider string, retryable func(int) bool) ([]byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, &Error{Retryable: true, Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, &Error{StatusCode: resp.StatusCode, Retryable: true, Err: err}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(provider, resp, raw, retryable(resp.StatusCode))
	}
	if len(raw) > maxResponseBytes {
		return nil, fmt.Errorf("%w: over %d bytes", ErrResponseTooLarge, maxResponseBytes)
	}
	return raw, nil
}

func statusError(provider string, resp *http.Response, raw []byte, retryable bool) *Error {
	message := http.StatusText(resp.StatusCode)
	var parsed errorBody
	if json.Unmarshal(raw, &parsed) == nil {
		if parsed.Error.Message != "" {
			message = parsed.Error.Message
		} else if parsed.Message != "" {
			message = parsed.Message
		}
	}
	e := &Error{
		StatusCode: resp.StatusCode,
		Retryable:  retryable,
		Err:        fmt.Errorf("%s returned %d: %s", provider, resp.StatusCode, message),
	}
	if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
		e.RetryAfter = time.Duration(s) * time.Second
	}
	return e
}
