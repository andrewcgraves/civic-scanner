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
	uriMarker          = "\x00"
)

type Playlist struct {
	Duration      time.Duration
	AudioDuration time.Duration
	Entries       int
	Playlists     int

	top      *parsedPlaylist
	children map[string]*parsedPlaylist
}

func isHLS(rawURL string) bool {
	u, err := url.Parse(rawURL)
	return err == nil && strings.EqualFold(path.Ext(u.Path), ".m3u8")
}

type playlistLine struct {
	text string
	uri  string
}

type parsedPlaylist struct {
	name      string
	master    bool
	children  []string
	audio     map[string]bool
	resources []string
	segments  int
	duration  float64
	ended     bool
	lines     []playlistLine
}

func InspectPlaylist(ctx context.Context, fetcher fetch.Fetcher, hosts []string, guard Guard, playlistURL string) (*Playlist, error) {
	top, err := fetchPlaylist(ctx, fetcher, hosts, guard, playlistURL)
	if err != nil {
		return nil, err
	}
	out := &Playlist{top: top, children: map[string]*parsedPlaylist{}, Playlists: 1, Entries: len(top.resources)}
	media := []*parsedPlaylist{top}
	if top.master {
		if len(top.children) == 0 {
			return nil, fmt.Errorf("%w: master playlist lists no variants", ErrNoMedia)
		}
		if len(top.children) > maxVariants {
			return nil, fmt.Errorf("%w: %d variants, more than %d", ErrLimitExceeded, len(top.children), maxVariants)
		}
		media = media[:0]
		seen := map[string]bool{playlistURL: true}
		for _, child := range top.children {
			if seen[child] {
				return nil, fmt.Errorf("%w: playlist %s is listed twice or refers to itself", ErrMalformed, child)
			}
			seen[child] = true
			p, err := fetchPlaylist(ctx, fetcher, hosts, guard, child)
			if err != nil {
				return nil, err
			}
			if p.master {
				return nil, fmt.Errorf("%w: nested master playlist %s", ErrMalformed, child)
			}
			out.children[child] = p
			out.Playlists++
			out.Entries += len(p.resources)
			media = append(media, p)
		}
	}
	for _, p := range media {
		if !p.ended {
			return nil, fmt.Errorf("%w: playlist has no EXT-X-ENDLIST, the stream is live or unfinished", ErrIncomplete)
		}
		if p.segments == 0 {
			return nil, fmt.Errorf("%w: playlist has no segments", ErrNoMedia)
		}
		d := time.Duration(p.duration * float64(time.Second))
		out.Duration = max(out.Duration, d)
	}
	out.AudioDuration = out.Duration
	if len(top.audio) > 0 {
		out.AudioDuration = 0
		for child := range top.audio {
			out.AudioDuration = max(out.AudioDuration, time.Duration(out.children[child].duration*float64(time.Second)))
		}
	}
	if out.Entries > maxPlaylistEntries {
		return nil, fmt.Errorf("%w: %d playlist entries, more than %d", ErrLimitExceeded, out.Entries, maxPlaylistEntries)
	}
	return out, nil
}

func (p *Playlist) Serve(g *Gateway) string {
	return p.serve(g, p.top)
}

func (p *Playlist) serve(g *Gateway, doc *parsedPlaylist) string {
	var b strings.Builder
	for _, line := range doc.lines {
		if line.uri == "" {
			b.WriteString(line.text + "\n")
			continue
		}
		local := ""
		if child, ok := p.children[line.uri]; ok && doc == p.top {
			local = p.serve(g, child)
		} else {
			local = g.Register(line.uri)
		}
		if line.text == "" {
			b.WriteString(local + "\n")
		} else {
			b.WriteString(strings.Replace(line.text, uriMarker, local, 1) + "\n")
		}
	}
	return g.Serve([]byte(b.String()), doc.name)
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
	for _, ref := range append(append([]string(nil), p.children...), p.resources...) {
		if ref == playlistURL || ref == base.String() {
			return nil, fmt.Errorf("%w: playlist %s refers to itself", ErrMalformed, playlistURL)
		}
	}
	p.name = path.Base(base.Path)
	if !strings.EqualFold(path.Ext(p.name), ".m3u8") {
		p.name += ".m3u8"
	}
	return p, nil
}

func parsePlaylist(body []byte, base *url.URL, hosts []string, guard Guard) (*parsedPlaylist, error) {
	body = bytes.TrimLeft(bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf}), " \t\r\n")
	if !bytes.HasPrefix(body, []byte("#EXTM3U")) {
		return nil, fmt.Errorf("%w: not an HLS playlist", ErrMalformed)
	}
	p := &parsedPlaylist{audio: map[string]bool{}}
	resolve := func(ref string) (string, error) {
		u, err := base.Parse(ref)
		if err != nil {
			return "", fmt.Errorf("%w: playlist entry %q", ErrMalformed, ref)
		}
		if err := guard.checkURL(u, hosts); err != nil {
			return "", fmt.Errorf("playlist entry: %w", err)
		}
		if len(p.resources)+len(p.children) >= maxPlaylistEntries {
			return "", fmt.Errorf("%w: more than %d playlist entries", ErrLimitExceeded, maxPlaylistEntries)
		}
		return u.String(), nil
	}
	tagURI := func(line string) (string, error) {
		uri, start, end, ok := attribute(line, "URI")
		if !ok {
			p.lines = append(p.lines, playlistLine{text: line})
			return "", nil
		}
		u, err := resolve(uri)
		if err != nil {
			return "", err
		}
		p.lines = append(p.lines, playlistLine{text: line[:start] + uriMarker + line[end:], uri: u})
		return u, nil
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
			p.lines = append(p.lines, playlistLine{text: line})
		case strings.HasPrefix(line, "#EXT-X-MEDIA:"), strings.HasPrefix(line, "#EXT-X-I-FRAME-STREAM-INF"):
			p.master = true
			u, err := tagURI(line)
			if err != nil {
				return nil, err
			}
			switch {
			case u == "":
			case strings.HasPrefix(line, "#EXT-X-MEDIA:") && strings.Contains(line, "TYPE=AUDIO"):
				p.children = append(p.children, u)
				p.audio[u] = true
			default:
				p.resources = append(p.resources, u)
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
			if u != "" {
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
			p.lines = append(p.lines, playlistLine{text: line})
		case line == "#EXT-X-ENDLIST":
			p.ended = true
			p.lines = append(p.lines, playlistLine{text: line})
		case strings.HasPrefix(line, "#") && strings.Contains(strings.ToUpper(line), "URI="):
		case strings.HasPrefix(line, "#"):
			p.lines = append(p.lines, playlistLine{text: line})
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
			p.lines = append(p.lines, playlistLine{uri: u})
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
