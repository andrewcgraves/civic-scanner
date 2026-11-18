package media

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"fmt"
	"net/url"
	"path"
	"slices"
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

var audioCodecPrefixes = []string{"mp4a", "ac-3", "ec-3", "ac-4", "opus", "flac", "alac", "mp3", "vorbis", "dtsc", "dtse", "dtsh", "dtsl"}

type Playlist struct {
	Duration time.Duration
	Runs     [][]string
}

func (p *Playlist) parts() int {
	n := 0
	for _, run := range p.Runs {
		n += len(run)
	}
	return n
}

type HLSSource struct {
	Media      *Playlist
	Candidates []string
}

func isHLS(rawURL string) bool {
	u, err := url.Parse(rawURL)
	return err == nil && strings.EqualFold(path.Ext(u.Path), ".m3u8")
}

type variant struct {
	url       string
	bandwidth int64
	codecs    string
	hasCodecs bool
}

type parsedPlaylist struct {
	master   bool
	variants []variant
	audio    []string
	runs     [][]string
	refs     []string
	segments int
	duration float64
	ended    bool
}

func InspectHLS(ctx context.Context, fetcher fetch.Fetcher, hosts []string, guard Guard, playlistURL string) (*HLSSource, error) {
	top, err := fetchPlaylist(ctx, fetcher, hosts, guard, playlistURL)
	if err != nil {
		return nil, err
	}
	if !top.master {
		media, err := top.media()
		if err != nil {
			return nil, err
		}
		return &HLSSource{Media: media}, nil
	}
	candidates, err := top.candidates()
	if err != nil {
		return nil, err
	}
	return &HLSSource{Candidates: candidates}, nil
}

func LoadMediaPlaylist(ctx context.Context, fetcher fetch.Fetcher, hosts []string, guard Guard, playlistURL string) (*Playlist, error) {
	p, err := fetchPlaylist(ctx, fetcher, hosts, guard, playlistURL)
	if err != nil {
		return nil, err
	}
	if p.master {
		return nil, fmt.Errorf("%w: nested master playlist %s", ErrMalformed, playlistURL)
	}
	return p.media()
}

func (p *parsedPlaylist) media() (*Playlist, error) {
	if !p.ended {
		return nil, fmt.Errorf("%w: playlist has no EXT-X-ENDLIST, the stream is live or unfinished", ErrIncomplete)
	}
	if p.segments == 0 {
		return nil, fmt.Errorf("%w: playlist has no segments", ErrNoMedia)
	}
	return &Playlist{Duration: time.Duration(p.duration * float64(time.Second)), Runs: p.runs}, nil
}

func audioClass(v variant) (int, bool) {
	if !v.hasCodecs {
		return 2, true
	}
	hasAudio, hasOther := false, false
	for _, codec := range strings.Split(strings.ToLower(v.codecs), ",") {
		codec = strings.TrimSpace(codec)
		if codec == "" {
			continue
		}
		audio := false
		for _, prefix := range audioCodecPrefixes {
			if strings.HasPrefix(codec, prefix) {
				audio = true
				break
			}
		}
		hasAudio = hasAudio || audio
		hasOther = hasOther || !audio
	}
	switch {
	case !hasAudio:
		return 0, false
	case !hasOther:
		return 0, true
	}
	return 1, true
}

func (p *parsedPlaylist) candidates() ([]string, error) {
	type ranked struct {
		url       string
		class     int
		bandwidth int64
	}
	var variants []ranked
	for _, v := range p.variants {
		class, ok := audioClass(v)
		if !ok {
			continue
		}
		bw := v.bandwidth
		if bw <= 0 {
			bw = 1<<63 - 1
		}
		variants = append(variants, ranked{url: v.url, class: class, bandwidth: bw})
	}
	slices.SortStableFunc(variants, func(a, b ranked) int {
		return cmp.Or(cmp.Compare(a.class, b.class), cmp.Compare(a.bandwidth, b.bandwidth))
	})
	seen := map[string]bool{}
	var out []string
	add := func(u string) {
		if !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	for _, u := range p.audio {
		add(u)
	}
	for _, v := range variants {
		add(v.url)
	}
	switch {
	case len(out) == 0:
		return nil, fmt.Errorf("%w: master playlist lists no variant that may carry audio", ErrNoAudio)
	case len(out) > maxVariants:
		return nil, fmt.Errorf("%w: %d variants, more than %d", ErrLimitExceeded, len(out), maxVariants)
	}
	return out, nil
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
	for _, ref := range p.refs {
		if ref == playlistURL || ref == base.String() {
			return nil, fmt.Errorf("%w: playlist %s refers to itself", ErrMalformed, playlistURL)
		}
	}
	return p, nil
}

func parsePlaylist(body []byte, base *url.URL, hosts []string, guard Guard) (*parsedPlaylist, error) {
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
		if err := guard.checkURL(u, hosts); err != nil {
			return "", fmt.Errorf("playlist entry: %w", err)
		}
		if len(p.refs) >= maxPlaylistEntries {
			return "", fmt.Errorf("%w: more than %d playlist entries", ErrLimitExceeded, maxPlaylistEntries)
		}
		p.refs = append(p.refs, u.String())
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
		nextIsVariant, nextIsSegment, newRun bool
		pending                              variant
		currentMap                           string
	)
	newRun = true
	segments := map[string]bool{}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 64<<10), 64<<10)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == "":
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF"):
			p.master, nextIsVariant = true, true
			pending = variant{}
			if bw, _, _, ok := attribute(line, "BANDWIDTH"); ok {
				pending.bandwidth, _ = strconv.ParseInt(bw, 10, 64)
			}
			pending.codecs, _, _, pending.hasCodecs = attribute(line, "CODECS")
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
		case strings.HasPrefix(line, "#EXT-X-BYTERANGE"):
			return nil, fmt.Errorf("%w: byte-range HLS (%s)", ErrUnsupported, line)
		case strings.HasPrefix(line, "#EXT-X-MAP"):
			if _, _, _, ok := attribute(line, "BYTERANGE"); ok {
				return nil, fmt.Errorf("%w: byte-range HLS (%s)", ErrUnsupported, line)
			}
			u, err := tagURI(line)
			if err != nil {
				return nil, err
			}
			if u != "" && u != currentMap {
				currentMap, newRun = u, true
			}
		case strings.HasPrefix(line, "#EXT-X-DISCONTINUITY") && !strings.HasPrefix(line, "#EXT-X-DISCONTINUITY-SEQUENCE"):
			newRun = true
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
				pending.url = u
				p.variants = append(p.variants, pending)
			case nextIsSegment:
				if segments[u] {
					return nil, fmt.Errorf("%w: segment %s is listed twice", ErrMalformed, u)
				}
				segments[u] = true
				if newRun {
					p.runs = append(p.runs, nil)
					if currentMap != "" {
						p.runs[len(p.runs)-1] = append(p.runs[len(p.runs)-1], currentMap)
					}
					newRun = false
				}
				p.runs[len(p.runs)-1] = append(p.runs[len(p.runs)-1], u)
				p.segments++
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
