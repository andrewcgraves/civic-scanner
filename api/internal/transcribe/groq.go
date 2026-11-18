package transcribe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const maxResponseBytes = 32 << 20

type verboseResponse struct {
	Duration float64 `json:"duration"`
	Text     string  `json:"text"`
	Segments []struct {
		Start float64 `json:"start"`
		End   float64 `json:"end"`
		Text  string  `json:"text"`
	} `json:"segments"`
}

type errorBody struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func seconds(v float64) time.Duration {
	return time.Duration(math.Round(v * float64(time.Second)))
}

func (g *groq) transcribeFile(ctx context.Context, path string) (Transcript, error) {
	file, err := os.Open(path)
	if err != nil {
		return Transcript{}, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return Transcript{}, err
	}

	head, tail, contentType, err := g.multipartFrame(filepath.Base(path))
	if err != nil {
		return Transcript{}, err
	}
	body := io.MultiReader(bytes.NewReader(head), file, bytes.NewReader(tail))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(g.cfg.BaseURL, "/")+"/audio/transcriptions", body)
	if err != nil {
		return Transcript{}, err
	}
	req.ContentLength = int64(len(head)) + info.Size() + int64(len(tail))
	req.Header.Set("Authorization", "Bearer "+g.cfg.APIKey)
	req.Header.Set("Content-Type", contentType)

	resp, err := g.http.Do(req)
	if err != nil {
		return Transcript{}, &Error{Retryable: true, Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return Transcript{}, &Error{StatusCode: resp.StatusCode, Retryable: true, Err: err}
	}
	if resp.StatusCode != http.StatusOK {
		return Transcript{}, statusError(resp, raw)
	}
	if len(raw) > maxResponseBytes {
		return Transcript{}, fmt.Errorf("%w: over %d bytes", ErrResponseTooLarge, maxResponseBytes)
	}

	var parsed verboseResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Transcript{}, &Error{StatusCode: resp.StatusCode, Retryable: true, Err: fmt.Errorf("decode response: %w", err)}
	}
	segments := make([]Segment, 0, len(parsed.Segments))
	for _, s := range parsed.Segments {
		segments = append(segments, Segment{Start: seconds(s.Start), End: seconds(s.End), Text: strings.TrimSpace(s.Text)})
	}
	return Transcript{
		Text:     joinText(segments, parsed.Text),
		Segments: segments,
		Duration: seconds(parsed.Duration),
	}, nil
}

func (g *groq) multipartFrame(filename string) (head, tail []byte, contentType string, err error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fields := [][2]string{
		{"model", g.cfg.Model},
		{"response_format", "verbose_json"},
		{"timestamp_granularities[]", "segment"},
		{"temperature", "0"},
	}
	for _, f := range fields {
		if err = mw.WriteField(f[0], f[1]); err != nil {
			return nil, nil, "", err
		}
	}
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
	header.Set("Content-Type", "application/octet-stream")
	if _, err = mw.CreatePart(header); err != nil {
		return nil, nil, "", err
	}
	headLen := buf.Len()
	if err = mw.Close(); err != nil {
		return nil, nil, "", err
	}
	all := buf.Bytes()
	return all[:headLen], all[headLen:], mw.FormDataContentType(), nil
}

func statusError(resp *http.Response, raw []byte) *Error {
	message := http.StatusText(resp.StatusCode)
	var parsed errorBody
	if json.Unmarshal(raw, &parsed) == nil && parsed.Error.Message != "" {
		message = parsed.Error.Message
	}
	e := &Error{
		StatusCode: resp.StatusCode,
		Retryable:  retryableStatus(resp.StatusCode),
		Err:        fmt.Errorf("groq returned %d: %s", resp.StatusCode, message),
	}
	if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
		e.RetryAfter = time.Duration(s) * time.Second
	}
	return e
}

func retryableStatus(code int) bool {
	return code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= http.StatusInternalServerError
}
