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
	Parts     []string
	Playlists int
}

func isHLS(rawURL string) bool {
	u, err := url.Parse(rawURL)
	return err == nil && strings.EqualFold(path.Ext(u.Path), ".m3u8")
}

type parsedPlaylist struct {
	master    bool
	variants  []string
	bandwidth map[string]int64
	audio     []string
	parts     []string
	segments  int
	duration  float64
	ended     bool
}

func InspectPlaylist(ctx context.Context, fetcher fetch.Fetcher, hosts []string, guard Guard, playlistURL string) (*Playlist, error) {
	top, err := fetchPlaylist(ctx, fetcher, hosts, guard, playlistURL)
	if err != nil {
		return nil, err
	}
	media, playlists := top, 1
	if top.master {
		child, err := top.choose()
		if err != nil {
			return nil, err
		}
		if media, err = fetchPlaylist(ctx, fetcher, hosts, guard, child); err != nil {
			return nil, err
		}
		if media.master {
			return nil, fmt.Errorf("%w: nested master playlist %s", ErrMalformed, child)
		}
		playlists++
	}
	if !media.ended {
		return nil, fmt.Errorf("%w: playlist has no EXT-X-ENDLIST, the stream is live or unfinished", ErrIncomplete)
	}
	if media.segments == 0 {
		return nil, fmt.Errorf("%w: playlist has no segments", ErrNoMedia)
	}
	return &Playlist{
		Duration:  time.Duration(media.duration * float64(time.Second)),
		Parts:     media.parts,
		Playlists: playlists,
	}, nil
}

func (p *parsedPlaylist) choose() (string, error) {
	candidates := len(p.variants) + len(p.audio)
	switch {
	case candidates == 0:
		return "", fmt.Errorf("%w: master playlist lists no variants", ErrNoMedia)
	case candidates > maxVariants:
		return "", fmt.Errorf("%w: %d variants, more than %d", ErrLimitExceeded, candidates, maxVariants)
	case len(p.audio) > 0:
		return p.audio[0], nil
	}
	best := p.variants[0]
	for _, v := range p.variants[1:] {
		if bw := p.bandwidth[v]; bw > 0 && (p.bandwidth[best] == 0 || bw < p.bandwidth[best]) {
			best = v
		}
	}
	return best, nil
}

func fetchPlaylist(ctx context.Context, fetcher fetch.Fetcher, hosts []string, guard Guard, playlistURL string) (*parsedPlaylist, error) {
	resp, err := fetcher.Fetch(ctx, fetch.Request{URL: playlistURL, MaxBytes: maxPlaylistBytes})
	if err != nil {
		return nil, fmt.Errorf("fetch playlist: %w", err)
	}
	base, err := url.Parse(resp.FinalURL)
	if err != nil || resp.FinalURL == "" {
		base, _ = url.Parse(playlistURL)
	}
	p, err := parsePlaylist(resp.Body, base, hosts, guard)
	if err != nil {
		return nil, err
	}
	refs := append(append(append([]string(nil), p.variants...), p.audio...), p.parts...)
	seen := map[string]bool{}
	for _, ref := range refs {
		if ref == playlistURL || ref == base.String() {
			return nil, fmt.Errorf("%w: playlist %s refers to itself", ErrMalformed, playlistURL)
		}
		if p.master && seen[ref] {
			return nil, fmt.Errorf("%w: playlist %s is listed twice", ErrMalformed, ref)
		}
		seen[ref] = true
	}
	return p, nil
}

func parsePlaylist(body []byte, base *url.URL, hosts []string, guard Guard) (*parsedPlaylist, error) {
	body = bytes.TrimLeft(bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf}), " \t\r\n")
	if !bytes.HasPrefix(body, []byte("#EXTM3U")) {
		return nil, fmt.Errorf("%w: not an HLS playlist", ErrMalformed)
	}
	p := &parsedPlaylist{bandwidth: map[string]int64{}}
	entries := 0
	resolve := func(ref string) (string, error) {
		u, err := base.Parse(ref)
		if err != nil {
			return "", fmt.Errorf("%w: playlist entry %q", ErrMalformed, ref)
		}
		if err := guard.checkURL(u, hosts); err != nil {
			return "", fmt.Errorf("playlist entry: %w", err)
		}
		if entries++; entries > maxPlaylistEntries {
			return "", fmt.Errorf("%w: more than %d playlist entries", ErrLimitExceeded, maxPlaylistEntries)
		}
		return u.String(), nil
	}
	tagURI := func(line string) (string, error) {
		uri, _, _, ok := attribute(line, "URI")
		if !ok {
			return "", nil
		}
		return resolve(uri)
	}
	var (
		nextIsVariant, nextIsSegment bool
		bandwidth                    int64
		currentMap                   string
	)
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 64<<10), 64<<10)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == "":
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF"):
			p.master, nextIsVariant = true, true
			bandwidth = 0
			if bw, _, _, ok := attribute(line, "BANDWIDTH"); ok {
				bandwidth, _ = strconv.ParseInt(bw, 10, 64)
			}
		case strings.HasPrefix(line, "#EXT-X-MEDIA:"):
			p.master = true
			u, err := tagURI(line)
			if err != nil {
				return nil, err
			}
			if u != "" && strings.Contains(line, "TYPE=AUDIO") {
				p.audio = append(p.audio, u)
			}
		case strings.HasPrefix(line, "#EXT-X-I-FRAME-STREAM-INF"):
			p.master = true
			if _, err := tagURI(line); err != nil {
				return nil, err
			}
		case strings.HasPrefix(line, "#EXT-X-KEY"), strings.HasPrefix(line, "#EXT-X-SESSION-KEY"):
			if method, _, _, ok := attribute(line, "METHOD"); !ok || method != "NONE" {
				return nil, fmt.Errorf("%w: encrypted HLS (%s)", ErrUnsupported, line)
			}
			if _, err := tagURI(line); err != nil {
				return nil, err
			}
		case strings.HasPrefix(line, "#EXT-X-MAP"):
			u, err := tagURI(line)
			if err != nil {
				return nil, err
			}
			if u != "" && u != currentMap {
				currentMap = u
				p.parts = append(p.parts, u)
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
				p.variants = append(p.variants, u)
				p.bandwidth[u] = bandwidth
			case nextIsSegment:
				p.segments++
				p.parts = append(p.parts, u)
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

func attribute(line, name string) (value string, start, end int, ok bool) {
	for _, sep := range []string{":", ","} {
		key := sep + name + "="
		idx := strings.Index(line, key)
		if idx < 0 {
			continue
		}
		start = idx + len(key)
		if strings.HasPrefix(line[start:], `"`) {
			start++
			closing := strings.IndexByte(line[start:], '"')
			if closing < 0 {
				return "", 0, 0, false
			}
			return line[start : start+closing], start, start + closing, true
		}
		rest := line[start:]
		if comma := strings.IndexByte(rest, ','); comma >= 0 {
			rest = rest[:comma]
		}
		return rest, start, start + len(rest), true
	}
	return "", 0, 0, false
}
