package transcribe

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
)

const (
	gatewayPath      = "/v4/ai/transcription-model"
	gatewayMediaType = "audio/mpeg"
	gatewayBodyHead  = `{"audio":"`
)

var ErrInvalidProviderOptions = errors.New("transcribe: provider options must be a JSON object")

type gateway struct {
	cfg  config.Transcription
	http *http.Client
}

func newGateway(cfg config.Transcription) *gateway {
	return &gateway{cfg: cfg, http: &http.Client{Timeout: cfg.Timeout}}
}

func (g *gateway) acceptsDirect(path string) bool {
	return strings.EqualFold(filepath.Ext(path), chunkExtension)
}

type gatewayReply struct {
	Text              string  `json:"text"`
	DurationInSeconds float64 `json:"durationInSeconds"`
	Segments          []struct {
		Text        string  `json:"text"`
		StartSecond float64 `json:"startSecond"`
		EndSecond   float64 `json:"endSecond"`
	} `json:"segments"`
	ProviderMetadata struct {
		ElevenLabs struct {
			Words []elevenLabsWord `json:"words"`
		} `json:"elevenlabs"`
		Azure struct {
			Phrases []azurePhrase `json:"phrases"`
		} `json:"azure"`
	} `json:"providerMetadata"`
}

func (g *gateway) transcribeFile(ctx context.Context, path string) (Transcript, error) {
	suffix, err := g.requestSuffix()
	if err != nil {
		return Transcript{}, err
	}
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return Transcript{}, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return Transcript{}, err
	}

	body := newBase64Body(file, gatewayBodyHead, suffix)
	defer func() { _ = body.Close() }()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(g.cfg.BaseURL, "/")+gatewayPath, body)
	if err != nil {
		return Transcript{}, err
	}
	req.ContentLength = int64(len(gatewayBodyHead)) + int64(base64.StdEncoding.EncodedLen(int(info.Size()))) + int64(len(suffix))
	req.Header.Set("Authorization", "Bearer "+g.cfg.APIKey)
	req.Header.Set("ai-gateway-protocol-version", "0.0.1")
	req.Header.Set("ai-transcription-model-specification-version", "4")
	req.Header.Set("ai-model-id", g.cfg.Model)
	req.Header.Set("Content-Type", "application/json")

	raw, err := send(g.http, req, "gateway", gatewayRetryableStatus)
	if err != nil {
		return Transcript{}, err
	}
	var parsed gatewayReply
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Transcript{}, &Error{Retryable: true, Err: fmt.Errorf("decode response: %w", err)}
	}
	segments := gatewaySegments(parsed)
	return Transcript{
		Text:     joinText(segments, parsed.Text),
		Segments: segments,
		Duration: seconds(parsed.DurationInSeconds),
	}, nil
}

func (g *gateway) requestSuffix() (string, error) {
	suffix := `","mediaType":"` + gatewayMediaType + `"`
	if g.cfg.ProviderOptions != "" {
		var object map[string]json.RawMessage
		if err := json.Unmarshal([]byte(g.cfg.ProviderOptions), &object); err != nil {
			return "", fmt.Errorf("%w: %w", ErrInvalidProviderOptions, err)
		}
		suffix += `,"providerOptions":` + g.cfg.ProviderOptions
	}
	return suffix + "}", nil
}

func gatewayRetryableStatus(code int) bool {
	return code == http.StatusPaymentRequired || retryableStatus(code)
}

func gatewaySegments(r gatewayReply) []Segment {
	if segments := azureSegments(r.ProviderMetadata.Azure.Phrases); len(segments) > 0 {
		return segments
	}
	words := r.ProviderMetadata.ElevenLabs.Words
	segments := make([]Segment, 0, len(r.Segments))
	for _, s := range r.Segments {
		segments = append(segments, Segment{Start: seconds(s.StartSecond), End: seconds(s.EndSecond), Text: strings.TrimSpace(s.Text)})
	}
	switch {
	case len(words) == 0:
		return segments
	case len(segments) == 0:
		return segmentsFromWords(words)
	default:
		labelSpeakers(segments, words)
		return segments
	}
}

type base64Body struct {
	io.Reader
	closer io.Closer
	done   <-chan struct{}
}

func newBase64Body(src io.Reader, prefix, suffix string) *base64Body {
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		enc := base64.NewEncoder(base64.StdEncoding, pw)
		_, err := io.Copy(enc, src)
		if err == nil {
			err = enc.Close()
		}
		_ = pw.CloseWithError(err)
	}()
	return &base64Body{
		Reader: io.MultiReader(strings.NewReader(prefix), pr, strings.NewReader(suffix)),
		closer: pr,
		done:   done,
	}
}

func (b *base64Body) Close() error {
	err := b.closer.Close()
	<-b.done
	return err
}
