package media

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
)

const (
	maxPlaylistBytes   = 2 << 20
	maxPlaylistEntries = 20_000
	maxVariants        = 16
)

type Playlist struct {
	Duration  time.Duration
	Entries   int
	Playlists int
}

func isHLS(rawURL string) bool {
	u, err := url.Parse(rawURL)
	return err == nil && strings.EqualFold(path.Ext(u.Path), ".m3u8")
}

type parsedPlaylist struct {
	master    bool
	children  []string
	resources []string
	segments  int
	duration  float64
	ended     bool
}

func InspectPlaylist(ctx context.Context, fetcher fetch.Fetcher, hosts []string, playlistURL string) (Playlist, error) {
	top, err := fetchPlaylist(ctx, fetcher, hosts, playlistURL)
	if err != nil {
		return Playlist{}, err
	}
	media := []*parsedPlaylist{top}
	if top.master {
		if len(top.children) == 0 {
			return Playlist{}, fmt.Errorf("%w: master playlist lists no variants", ErrNoMedia)
		}
		if len(top.children) > maxVariants {
			return Playlist{}, fmt.Errorf("%w: %d variants, more than %d", ErrLimitExceeded, len(top.children), maxVariants)
		}
		media = media[:0]
		seen := map[string]bool{playlistURL: true}
		for _, child := range top.children {
			if seen[child] {
				return Playlist{}, fmt.Errorf("%w: playlist %s is listed twice or refers to itself", ErrMalformed, child)
			}
			seen[child] = true
			p, err := fetchPlaylist(ctx, fetcher, hosts, child)
			if err != nil {
				return Playlist{}, err
			}
			if p.master {
				return Playlist{}, fmt.Errorf("%w: nested master playlist %s", ErrMalformed, child)
			}
			media = append(media, p)
		}
	}
	out := Playlist{Playlists: 1 + len(top.children), Entries: len(top.resources)}
	for _, p := range media {
		if !p.ended {
			return Playlist{}, fmt.Errorf("%w: playlist has no EXT-X-ENDLIST, the stream is live or unfinished", ErrIncomplete)
		}
		if p.segments == 0 {
			return Playlist{}, fmt.Errorf("%w: playlist has no segments", ErrNoMedia)
		}
		if p != top {
			out.Entries += len(p.resources)
		}
		if d := time.Duration(p.duration * float64(time.Second)); d > out.Duration {
			out.Duration = d
		}
	}
	if out.Entries > maxPlaylistEntries {
		return Playlist{}, fmt.Errorf("%w: %d playlist entries, more than %d", ErrLimitExceeded, out.Entries, maxPlaylistEntries)
	}
	return out, nil
}

func fetchPlaylist(ctx context.Context, fetcher fetch.Fetcher, hosts []string, playlistURL string) (*parsedPlaylist, error) {
	resp, err := fetcher.Fetch(ctx, fetch.Request{URL: playlistURL, MaxBytes: maxPlaylistBytes})
	if err != nil {
		return nil, fmt.Errorf("fetch playlist: %w", err)
	}
	base, err := url.Parse(resp.FinalURL)
	if err != nil || resp.FinalURL == "" {
		base, _ = url.Parse(playlistURL)
	}
	p, err := parsePlaylist(resp.Body, base, hosts)
	if err != nil {
		return nil, err
	}
	for _, ref := range append(append([]string(nil), p.children...), p.resources...) {
		if ref == playlistURL || ref == base.String() {
			return nil, fmt.Errorf("%w: playlist %s refers to itself", ErrMalformed, playlistURL)
		}
	}
	return p, nil
}

func parsePlaylist(body []byte, base *url.URL, hosts []string) (*parsedPlaylist, error) {
	body = bytes.TrimLeft(bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf}), " \t\r\n")
	if !bytes.HasPrefix(body, []byte("#EXTM3U")) {
		return nil, fmt.Errorf("%w: not an HLS playlist", ErrMalformed)
	}
	p := &parsedPlaylist{}
	resolve := func(ref string) (string, error) {
		u, err := base.Parse(ref)
		if err != nil {
			return "", fmt.Errorf("%w: playlist entry %q", ErrMalformed, ref)
		}
		if err := checkURL(u, hosts); err != nil {
			return "", fmt.Errorf("playlist entry: %w", err)
		}
		if len(p.resources)+len(p.children) >= maxPlaylistEntries {
			return "", fmt.Errorf("%w: more than %d playlist entries", ErrLimitExceeded, maxPlaylistEntries)
		}
		return u.String(), nil
	}
	nextIsVariant, nextIsSegment := false, false
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 64<<10), 64<<10)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == "":
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF"):
			p.master, nextIsVariant = true, true
		case strings.HasPrefix(line, "#EXT-X-MEDIA:"), strings.HasPrefix(line, "#EXT-X-I-FRAME-STREAM-INF"):
			p.master = true
			if uri, ok := attribute(line, "URI"); ok {
				u, err := resolve(uri)
				if err != nil {
					return nil, err
				}
				if strings.HasPrefix(line, "#EXT-X-MEDIA:") && strings.Contains(line, "TYPE=AUDIO") {
					p.children = append(p.children, u)
				} else {
					p.resources = append(p.resources, u)
				}
			}
		case strings.HasPrefix(line, "#EXT-X-KEY"), strings.HasPrefix(line, "#EXT-X-SESSION-KEY"), strings.HasPrefix(line, "#EXT-X-MAP"):
			if uri, ok := attribute(line, "URI"); ok {
				u, err := resolve(uri)
				if err != nil {
					return nil, err
				}
				p.resources = append(p.resources, u)
			}
		case strings.HasPrefix(line, "#EXTINF:"):
			value, _, _ := strings.Cut(strings.TrimPrefix(line, "#EXTINF:"), ",")
			seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
			if err != nil || seconds < 0 || seconds > 24*60*60 {
				return nil, fmt.Errorf("%w: segment duration %q", ErrMalformed, value)
			}
			p.duration += seconds
			nextIsSegment = true
		case line == "#EXT-X-ENDLIST":
			p.ended = true
		case strings.HasPrefix(line, "#"):
		default:
			u, err := resolve(line)
			if err != nil {
				return nil, err
			}
			switch {
			case nextIsVariant:
				p.children = append(p.children, u)
			case nextIsSegment:
				p.segments++
				p.resources = append(p.resources, u)
			default:
				return nil, fmt.Errorf("%w: uri %q outside a segment or variant", ErrMalformed, line)
			}
			nextIsVariant, nextIsSegment = false, false
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%w: read playlist: %w", ErrMalformed, err)
	}
	if p.master && p.segments > 0 {
		return nil, fmt.Errorf("%w: playlist is both master and media", ErrMalformed)
	}
	return p, nil
}

func attribute(line, name string) (string, bool) {
	idx := strings.Index(line, name+"=\"")
	if idx < 0 {
		return "", false
	}
	rest := line[idx+len(name)+2:]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return "", false
	}
	return rest[:end], true
}
