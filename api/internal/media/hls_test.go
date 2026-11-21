package media

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch/fetchtest"
)

const hlsBase = "https://archive-stream.granicus.com/OnDemand/_definst_/mp4:archive/metrolink/clip.mp4/"

func playlists(pages map[string]string) *fetchtest.Fake {
	return &fetchtest.Fake{Respond: func(req fetch.Request) (fetch.Response, error) {
		body, ok := pages[req.URL]
		if !ok {
			return fetch.Response{}, &fetch.Error{StatusCode: 404, Err: errors.New("not found")}
		}
		return fetch.Response{Status: 200, ContentType: "application/vnd.apple.mpegurl", Body: []byte(body), FinalURL: req.URL}, nil
	}}
}

const wowzaMaster = "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-STREAM-INF:BANDWIDTH=1281368,CODECS=\"avc1.77.31,mp4a.40.2\",RESOLUTION=1280x720\nchunklist_w1.m3u8\n"

func mediaPlaylist(segments int, extra string) string {
	var b strings.Builder
	b.WriteString("\xef\xbb\xbf#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:10\n" + extra)
	for i := range segments {
		fmt.Fprintf(&b, "#EXTINF:10.0,\nmedia_w1_%d.ts\n", i)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

func inspect(t *testing.T, pages map[string]string) (*Playlist, []string, error) {
	t.Helper()
	fake := playlists(pages)
	top := hlsBase + "playlist.m3u8"
	source, err := InspectHLS(t.Context(), fake, DefaultHosts.Media, Guard{}, top)
	if err != nil {
		return nil, nil, err
	}
	if source.Media != nil {
		return source.Media, nil, nil
	}
	media, err := LoadMediaPlaylist(t.Context(), fake, DefaultHosts.Media, Guard{}, source.Candidates[0])
	return media, source.Candidates, err
}

func TestInspectHLSReadsAWowzaStyleMasterAndChunklist(t *testing.T) {
	got, candidates, err := inspect(t, map[string]string{
		hlsBase + "playlist.m3u8":     wowzaMaster,
		hlsBase + "chunklist_w1.m3u8": mediaPlaylist(3, "#EXT-X-MAP:URI=\"init.mp4\"\n#EXT-X-KEY:METHOD=NONE\n#EXT-X-SESSION-DATA:DATA-ID=\"x\",URI=\"https://evil.test/x.json\"\n"),
	})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	want := []string{hlsBase + "init.mp4", hlsBase + "media_w1_0.ts", hlsBase + "media_w1_1.ts", hlsBase + "media_w1_2.ts"}
	if got.Duration != 30*time.Second || len(got.Runs) != 1 || strings.Join(got.Runs[0], " ") != strings.Join(want, " ") || got.parts() != 4 {
		t.Errorf("playlist = %+v", got)
	}
	if len(candidates) != 1 || candidates[0] != hlsBase+"chunklist_w1.m3u8" {
		t.Errorf("candidates = %v", candidates)
	}
}

func TestInspectHLSSplitsRunsAtDiscontinuitiesAndInitChanges(t *testing.T) {
	body := "#EXTM3U\n#EXT-X-DISCONTINUITY-SEQUENCE:0\n#EXT-X-MAP:URI=\"init1.mp4\"\n#EXTINF:4,\na.m4s\n#EXTINF:4,\nb.m4s\n" +
		"#EXT-X-DISCONTINUITY\n#EXTINF:4,\nc.m4s\n#EXT-X-MAP:URI=\"init2.mp4\"\n#EXTINF:4,\nd.m4s\n#EXT-X-MAP:URI=\"init2.mp4\"\n#EXTINF:4,\ne.m4s\n#EXT-X-ENDLIST\n"
	got, _, err := inspect(t, map[string]string{hlsBase + "playlist.m3u8": body})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	want := [][]string{
		{hlsBase + "init1.mp4", hlsBase + "a.m4s", hlsBase + "b.m4s"},
		{hlsBase + "init1.mp4", hlsBase + "c.m4s"},
		{hlsBase + "init2.mp4", hlsBase + "d.m4s", hlsBase + "e.m4s"},
	}
	if fmt.Sprint(got.Runs) != fmt.Sprint(want) || got.Duration != 20*time.Second {
		t.Errorf("runs = %v, want %v", got.Runs, want)
	}
}

func TestInspectHLSOrdersCandidatesByAudio(t *testing.T) {
	top := hlsBase + "playlist.m3u8"
	cases := map[string]struct {
		master string
		want   []string
	}{
		"audio rendition shared by two groups": {
			"#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"lo\",URI=\"audio.m3u8\"\n#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"hi\",URI=\"audio.m3u8\"\n" +
				"#EXT-X-STREAM-INF:BANDWIDTH=1,AUDIO=\"lo\"\nlow.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=2,AUDIO=\"hi\"\nhigh.m3u8\n",
			[]string{"audio.m3u8", "low.m3u8", "high.m3u8"},
		},
		"video-only lowest variant is skipped": {
			"#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=100,CODECS=\"avc1.42e00a\"\nvideo.m3u8\n" +
				"#EXT-X-STREAM-INF:BANDWIDTH=900,CODECS=\"avc1.42e00a,mp4a.40.2\"\nfull.m3u8\n",
			[]string{"full.m3u8"},
		},
		"audio-only, then audio with video, then unknown": {
			"#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=100\nunknown.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=900,CODECS=\"avc1.4d401f,mp4a.40.2\"\nbig.m3u8\n" +
				"#EXT-X-STREAM-INF:BANDWIDTH=500,CODECS=\"hvc1.1.6.L93.B0,ec-3\"\nsmall.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=64000,CODECS=\"mp4a.40.2\"\naudio-only.m3u8\n" +
				"#EXT-X-STREAM-INF:CODECS=\"avc1.4d401f,mp4a.40.2\"\nno-bandwidth.m3u8\n#EXT-X-I-FRAME-STREAM-INF:BANDWIDTH=1,URI=\"iframes.m3u8\"\n",
			[]string{"audio-only.m3u8", "small.m3u8", "big.m3u8", "no-bandwidth.m3u8", "unknown.m3u8"},
		},
		"same variant twice": {
			"#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nv.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=2\nv.m3u8\n",
			[]string{"v.m3u8"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			source, err := InspectHLS(t.Context(), playlists(map[string]string{top: tc.master}), DefaultHosts.Media, Guard{}, top)
			if err != nil {
				t.Fatalf("InspectHLS: %v", err)
			}
			var want []string
			for _, w := range tc.want {
				want = append(want, hlsBase+w)
			}
			if fmt.Sprint(source.Candidates) != fmt.Sprint(want) {
				t.Errorf("candidates = %v, want %v", source.Candidates, want)
			}
		})
	}
}

func TestInspectHLSRejectsUnsafeOrBrokenPlaylists(t *testing.T) {
	top := hlsBase + "playlist.m3u8"
	cases := map[string]struct {
		pages map[string]string
		want  error
	}{
		"not a playlist":          {map[string]string{top: "<html>error</html>"}, ErrMalformed},
		"segment off the list":    {map[string]string{top: "#EXTM3U\n#EXTINF:10,\nhttps://evil.test/seg.ts\n#EXT-X-ENDLIST\n"}, ErrForbiddenHost},
		"local file segment":      {map[string]string{top: "#EXTM3U\n#EXTINF:10,\nfile:///etc/passwd\n#EXT-X-ENDLIST\n"}, ErrForbiddenHost},
		"key off the list":        {map[string]string{top: "#EXTM3U\n#EXT-X-KEY:METHOD=NONE,URI=\"http://169.254.169.254/key\"\n#EXTINF:10,\ns.ts\n#EXT-X-ENDLIST\n"}, ErrForbiddenHost},
		"encrypted":               {map[string]string{top: "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\"\n#EXTINF:10,\ns.ts\n#EXT-X-ENDLIST\n"}, ErrUnsupported},
		"key without method":      {map[string]string{top: "#EXTM3U\n#EXT-X-KEY:URI=\"key.bin\"\n#EXTINF:10,\ns.ts\n#EXT-X-ENDLIST\n"}, ErrUnsupported},
		"byte ranges":             {map[string]string{top: "#EXTM3U\n#EXTINF:4,\n#EXT-X-BYTERANGE:1000@0\nsingle.ts\n#EXTINF:4,\n#EXT-X-BYTERANGE:1000\nsingle.ts\n#EXT-X-ENDLIST\n"}, ErrUnsupported},
		"init byte range":         {map[string]string{top: "#EXTM3U\n#EXT-X-MAP:URI=\"single.mp4\",BYTERANGE=\"720@0\"\n#EXTINF:4,\na.m4s\n#EXT-X-ENDLIST\n"}, ErrUnsupported},
		"segment listed twice":    {map[string]string{top: "#EXTM3U\n#EXTINF:4,\nsingle.ts\n#EXTINF:4,\nsingle.ts\n#EXT-X-ENDLIST\n"}, ErrMalformed},
		"httpproxy segment":       {map[string]string{top: "#EXTM3U\n#EXTINF:10,\nhttpproxy://archive-stream.granicus.com/s.ts\n#EXT-X-ENDLIST\n"}, ErrForbiddenHost},
		"crypto segment":          {map[string]string{top: "#EXTM3U\n#EXTINF:10,\ncrypto+https://archive-stream.granicus.com/s.ts\n#EXT-X-ENDLIST\n"}, ErrForbiddenHost},
		"concat segment":          {map[string]string{top: "#EXTM3U\n#EXTINF:10,\nconcat:https://archive-stream.granicus.com/a.ts\n#EXT-X-ENDLIST\n"}, ErrForbiddenHost},
		"subfile segment":         {map[string]string{top: "#EXTM3U\n#EXTINF:10,\nsubfile,,start,0,end,0,,:https://archive-stream.granicus.com/a.ts\n#EXT-X-ENDLIST\n"}, ErrMalformed},
		"data segment":            {map[string]string{top: "#EXTM3U\n#EXTINF:10,\ndata:video/mp2t;base64,AAAA\n#EXT-X-ENDLIST\n"}, ErrForbiddenHost},
		"segment on port 8443":    {map[string]string{top: "#EXTM3U\n#EXTINF:10,\nhttps://archive-stream.granicus.com:8443/s.ts\n#EXT-X-ENDLIST\n"}, ErrForbiddenHost},
		"map off the list":        {map[string]string{top: "#EXTM3U\n#EXT-X-MAP:URI=\"https://evil.test/init.mp4\"\n#EXTINF:10,\ns.ts\n#EXT-X-ENDLIST\n"}, ErrForbiddenHost},
		"variant off the list":    {map[string]string{top: "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nhttps://evil.test/v.m3u8\n"}, ErrForbiddenHost},
		"audio off the list":      {map[string]string{top: "#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"a\",URI=\"https://evil.test/a.m3u8\"\n"}, ErrForbiddenHost},
		"iframe off the list":     {map[string]string{top: "#EXTM3U\n#EXT-X-I-FRAME-STREAM-INF:BANDWIDTH=1,URI=\"https://evil.test/i.m3u8\"\n"}, ErrForbiddenHost},
		"self reference":          {map[string]string{top: "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nplaylist.m3u8\n"}, ErrMalformed},
		"segment is the list":     {map[string]string{top: "#EXTM3U\n#EXTINF:10,\nplaylist.m3u8\n#EXT-X-ENDLIST\n"}, ErrMalformed},
		"nested master":           {map[string]string{top: wowzaMaster, hlsBase + "chunklist_w1.m3u8": wowzaMaster}, ErrMalformed},
		"only video variants":     {map[string]string{top: "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1,CODECS=\"avc1.42e00a\"\nv.m3u8\n"}, ErrNoAudio},
		"master without variants": {map[string]string{top: "#EXTM3U\n#EXT-X-I-FRAME-STREAM-INF:BANDWIDTH=1,URI=\"i.m3u8\"\n"}, ErrNoAudio},
		"missing variant":         {map[string]string{top: wowzaMaster}, fetch.ErrInvalidURL},
		"live":                    {map[string]string{top: "#EXTM3U\n#EXTINF:10,\ns.ts\n"}, ErrIncomplete},
		"no segments":             {map[string]string{top: "#EXTM3U\n#EXT-X-ENDLIST\n"}, ErrNoMedia},
		"bad duration":            {map[string]string{top: "#EXTM3U\n#EXTINF:ten,\ns.ts\n#EXT-X-ENDLIST\n"}, ErrMalformed},
		"stray uri":               {map[string]string{top: "#EXTM3U\ns.ts\n#EXT-X-ENDLIST\n"}, ErrMalformed},
		"master and media":        {map[string]string{top: "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nv.m3u8\n#EXTINF:10,\ns.ts\n"}, ErrMalformed},
		"too many segments":       {map[string]string{top: mediaPlaylist(maxPlaylistEntries+1, "")}, ErrLimitExceeded},
		"too many variants":       {map[string]string{top: "#EXTM3U\n" + variantList(maxVariants+1)}, ErrLimitExceeded},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := inspect(t, tc.pages)
			if errors.Is(tc.want, fetch.ErrInvalidURL) {
				var fetchErr *fetch.Error
				if !errors.As(err, &fetchErr) || fetchErr.StatusCode != 404 {
					t.Errorf("inspect = %v, want the 404", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("inspect = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestAttribute(t *testing.T) {
	line := `#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a",URI="audio.m3u8",KEYURI="x"`
	if v, start, end, ok := attribute(line, "URI"); !ok || v != "audio.m3u8" || line[start:end] != "audio.m3u8" {
		t.Errorf("attribute(URI) = %q %v", v, ok)
	}
	if v, _, _, ok := attribute("#EXT-X-KEY:METHOD=NONE,IV=0x1", "METHOD"); !ok || v != "NONE" {
		t.Errorf("attribute(METHOD) = %q %v", v, ok)
	}
	if v, _, _, ok := attribute(`#EXT-X-STREAM-INF:AVERAGE-BANDWIDTH=5,BANDWIDTH=7`, "BANDWIDTH"); !ok || v != "7" {
		t.Errorf("attribute(BANDWIDTH) = %q %v", v, ok)
	}
	if _, _, _, ok := attribute(`#EXT-X-MAP:URI="unterminated`, "URI"); ok {
		t.Error("unterminated attribute accepted")
	}
	if _, _, _, ok := attribute("#EXTINF:10,", "URI"); ok {
		t.Error("missing attribute found")
	}
}

func TestIsHLS(t *testing.T) {
	cases := map[string]bool{
		hlsBase + "playlist.m3u8":          true,
		"https://a.test/PLAYLIST.M3U8?x=1": true,
		"https://a.test/clip.mp4":          false,
		"https://a.test/m3u8/clip.mp4":     false,
		"::bad":                            false,
	}
	for u, want := range cases {
		if got := isHLS(u); got != want {
			t.Errorf("isHLS(%q) = %v, want %v", u, got, want)
		}
	}
}

func variantList(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=1\nv%d.m3u8\n", i)
	}
	return b.String()
}

func TestInspectHLSReadsTheRecordedCastusMasterPlaylist(t *testing.T) {
	const base = "https://dlttx48mxf9m3.cloudfront.net/outputs/6af62b4f05fc227eedc662d8/Default/HLS/"
	fake := playlists(map[string]string{
		base + "out.m3u8":     page(t, "castus_rctc_out.m3u8"),
		base + "out360p.m3u8": mediaPlaylist(2, "#EXT-X-PLAYLIST-TYPE:VOD\n"),
	})
	source, err := InspectHLS(t.Context(), fake, DefaultHosts.Media, Guard{}, base+"out.m3u8")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	want := []string{base + "out360p.m3u8", base + "out720p.m3u8", base + "out1080p.m3u8"}
	if source.Media != nil || strings.Join(source.Candidates, " ") != strings.Join(want, " ") {
		t.Fatalf("source = %+v, want the variants from the lowest bandwidth up", source)
	}
	media, err := LoadMediaPlaylist(t.Context(), fake, DefaultHosts.Media, Guard{}, source.Candidates[0])
	if err != nil || media.parts() != 2 || media.Runs[0][0] != base+"media_w1_0.ts" {
		t.Errorf("media playlist = %+v, %v", media, err)
	}
}
