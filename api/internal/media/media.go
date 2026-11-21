package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
)

var (
	ErrUnsupported   = errors.New("media: unsupported recording url")
	ErrNoMedia       = errors.New("media: no media found for the recording")
	ErrNoAudio       = errors.New("media: recording has no usable audio track")
	ErrBlocked       = errors.New("media: got a bot check or error page instead of the player")
	ErrMalformed     = errors.New("media: media could not be read")
	ErrLimitExceeded = errors.New("media: media exceeds limits")
	ErrUnavailable   = errors.New("media: tool unavailable")
	ErrRefused       = errors.New("media: media host refused the request")
	ErrNetwork       = errors.New("media: network error reading media")
	ErrIncomplete    = errors.New("media: audio is shorter than the recording")
	ErrInvalidURL    = errors.New("media: recording url is not an http or https url")
	ErrUnrecognized  = errors.New("media: unrecognised failure")
)

const (
	KindInvalidURL  = "invalid_url"
	KindUnsupported = "unsupported"
	KindNoMedia     = "no_media"
	KindNoAudio     = "no_audio"
	KindMalformed   = "malformed"
	KindRefused     = "refused"
	KindLimit       = "limit"
	KindUnavailable = "unavailable"
	KindIncomplete  = "incomplete"
	KindTransient   = "transient"
)

func FailureKind(err error) string {
	var fetchErr *fetch.Error
	switch {
	case errors.Is(err, ErrInvalidURL):
		return KindInvalidURL
	case errors.Is(err, ErrUnsupported), errors.Is(err, ErrForbiddenHost), errors.Is(err, ErrForbiddenAddress),
		errors.Is(err, fetch.ErrInvalidURL):
		return KindUnsupported
	case errors.Is(err, ErrNoMedia):
		return KindNoMedia
	case errors.Is(err, ErrNoAudio):
		return KindNoAudio
	case errors.Is(err, ErrLimitExceeded), errors.Is(err, fetch.ErrTooLarge):
		return KindLimit
	case errors.Is(err, ErrRefused), errors.Is(err, fetch.ErrTooManyRedirects):
		return KindRefused
	case errors.Is(err, ErrUnavailable), errors.Is(err, fetch.ErrNotConfigured):
		return KindUnavailable
	case errors.Is(err, ErrIncomplete):
		return KindIncomplete
	case IsRetryable(err):
		return KindTransient
	case errors.As(err, &fetchErr) && fetchErr.StatusCode >= 400:
		return KindRefused
	case errors.Is(err, ErrMalformed):
		return KindMalformed
	}
	return KindTransient
}

const DefaultMinBytes = 40_000

func IsRetryable(err error) bool {
	return errors.Is(err, ErrNetwork) || errors.Is(err, ErrBlocked) || errors.Is(err, ErrUnrecognized) || fetch.IsRetryable(err) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

type Hosts struct {
	Players []string
	Media   []string
	YouTube []string
}

var DefaultHosts = Hosts{
	Players: []string{".granicus.com"},
	Media:   []string{"archive-stream.granicus.com", "archive-video.granicus.com", ".googlevideo.com", "dlttx48mxf9m3.cloudfront.net"},
	YouTube: []string{"youtube.com", "www.youtube.com", "m.youtube.com", "youtu.be"},
}

func matchHost(list []string, host string) bool {
	host = strings.ToLower(host)
	for _, entry := range list {
		if strings.HasPrefix(entry, ".") && strings.HasSuffix(host, entry) || host == entry {
			return true
		}
	}
	return false
}

type Audio struct {
	MediaURL string
	Via      string
	Bytes    int64
}

type Downloader struct {
	Resolver Resolver
	Tool     Tool
	MinBytes int64
}

func (d Downloader) DownloadAudio(ctx context.Context, recordingURL, dst string) (Audio, error) {
	src, err := d.Resolver.Resolve(ctx, recordingURL)
	if err != nil {
		return Audio{}, err
	}
	if err := d.Tool.ExtractAudio(ctx, src.URL, dst); err != nil {
		return Audio{}, err
	}
	minBytes := d.MinBytes
	if minBytes <= 0 {
		minBytes = DefaultMinBytes
	}
	size, err := CheckMP3(dst, minBytes)
	if err != nil {
		return Audio{}, err
	}
	return Audio{MediaURL: src.URL, Via: src.Via, Bytes: size}, nil
}

func CheckMP3(path string, minBytes int64) (int64, error) {
	f, err := os.Open(path) //nolint:gosec
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrNoAudio, err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrNoAudio, err)
	}
	if info.Size() < minBytes {
		return 0, fmt.Errorf("%w: %d bytes of audio, fewer than %d", ErrNoAudio, info.Size(), minBytes)
	}
	head := make([]byte, 3)
	if _, err := io.ReadFull(f, head); err != nil {
		return 0, fmt.Errorf("%w: %w", ErrNoAudio, err)
	}
	if !bytes.Equal(head, []byte("ID3")) && (head[0] != 0xff || head[1]&0xe0 != 0xe0) {
		return 0, fmt.Errorf("%w: output is not mp3", ErrNoAudio)
	}
	return info.Size(), nil
}
