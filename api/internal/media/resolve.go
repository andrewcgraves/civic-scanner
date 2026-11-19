package media

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/andrewcgraves/civic-scanner/api/internal/extract"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
)

const (
	maxPlayerPageBytes = 4 << 20
	ViaDirect          = "direct"
	ViaPlayer          = "granicus-player"
	ViaYouTube         = "youtube"
)

var (
	mediaExtensions = map[string]bool{
		".m3u8": true, ".mp4": true, ".m4a": true, ".m4v": true, ".mov": true, ".mp3": true,
		".aac": true, ".webm": true, ".mkv": true, ".ogg": true, ".wav": true, ".ts": true,
	}
	pageURLPattern = regexp.MustCompile(`https?://[A-Za-z0-9.-]+(?::[0-9]+)?/[^"'\s<>\\]*`)
)

type YouTubeResolver func(ctx context.Context, pageURL string) (string, error)

type Resolver struct {
	Fetcher fetch.Fetcher
	YouTube YouTubeResolver
	Hosts   Hosts
}

type Source struct {
	URL string
	Via string
}

func (r Resolver) Resolve(ctx context.Context, recordingURL string) (Source, error) {
	u, err := url.Parse(recordingURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Source{}, fmt.Errorf("%w: %q", ErrInvalidURL, recordingURL)
	}
	host := u.Hostname()
	switch {
	case matchHost(r.Hosts.Media, host) && isMediaPath(u.Path):
		return Source{URL: u.String(), Via: ViaDirect}, nil
	case matchHost(r.Hosts.Media, host):
		return Source{}, fmt.Errorf("%w: %s is not a media file", ErrUnsupported, u.Path)
	case matchHost(r.Hosts.YouTube, host):
		return r.youtube(ctx, u.String())
	case matchHost(r.Hosts.Players, host):
		return r.player(ctx, u.String())
	}
	return Source{}, fmt.Errorf("%w: host %s is not a known recording host", ErrUnsupported, host)
}

func isMediaPath(p string) bool {
	return mediaExtensions[strings.ToLower(path.Ext(p))]
}

func (r Resolver) youtube(ctx context.Context, pageURL string) (Source, error) {
	if r.YouTube == nil {
		return Source{}, fmt.Errorf("%w: no youtube resolver", ErrUnavailable)
	}
	mediaURL, err := r.YouTube(ctx, pageURL)
	if err != nil {
		return Source{}, err
	}
	u, err := url.Parse(mediaURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !matchHost(r.Hosts.Media, u.Hostname()) {
		return Source{}, fmt.Errorf("%w: youtube gave a media url on an unknown host", ErrNoMedia)
	}
	return Source{URL: mediaURL, Via: ViaYouTube}, nil
}

func (r Resolver) player(ctx context.Context, pageURL string) (Source, error) {
	if r.Fetcher == nil {
		return Source{}, fmt.Errorf("%w: no fetcher for player pages", ErrUnavailable)
	}
	resp, err := r.Fetcher.Fetch(ctx, fetch.Request{URL: pageURL, MaxBytes: maxPlayerPageBytes})
	if err != nil {
		return Source{}, fmt.Errorf("fetch player page: %w", err)
	}
	if marker, blocked := extract.Interstitial(resp.Body); blocked {
		return Source{}, fmt.Errorf("%w: player page contains %q", ErrBlocked, marker)
	}
	if final, err := url.Parse(resp.FinalURL); resp.FinalURL != "" && (err != nil || !matchHost(r.Hosts.Players, final.Hostname())) {
		return Source{}, fmt.Errorf("%w: player page redirected to %s", ErrNoMedia, resp.FinalURL)
	}
	if mediaURL, ok := findMedia(string(resp.Body), r.Hosts.Media); ok {
		return Source{URL: mediaURL, Via: ViaPlayer}, nil
	}
	return Source{}, fmt.Errorf("%w: player page names no media file", ErrNoMedia)
}

func findMedia(page string, mediaHosts []string) (string, bool) {
	page = strings.ReplaceAll(page, `\/`, "/")
	var file string
	for _, candidate := range pageURLPattern.FindAllString(page, -1) {
		u, err := url.Parse(candidate)
		if err != nil || !matchHost(mediaHosts, u.Hostname()) {
			continue
		}
		switch strings.ToLower(path.Ext(u.Path)) {
		case ".m3u8":
			return candidate, true
		case ".mp4", ".m4a", ".mp3":
			if file == "" {
				file = candidate
			}
		}
	}
	return file, file != ""
}
