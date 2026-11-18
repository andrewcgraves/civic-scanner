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

func TestInspectPlaylistReadsAWowzaStyleMasterAndChunklist(t *testing.T) {
	fake := playlists(map[string]string{
		hlsBase + "playlist.m3u8":     wowzaMaster,
		hlsBase + "chunklist_w1.m3u8": mediaPlaylist(3, "#EXT-X-MAP:URI=\"init.mp4\"\n#EXT-X-KEY:METHOD=NONE\n#EXT-X-SESSION-DATA:DATA-ID=\"x\",URI=\"https://evil.test/x.json\"\n"),
	})
	got, err := InspectPlaylist(t.Context(), fake, DefaultHosts.Media, Guard{}, hlsBase+"playlist.m3u8")
	if err != nil {
		t.Fatalf("InspectPlaylist: %v", err)
	}
	if got.Duration != 30*time.Second || got.AudioDuration != 30*time.Second || got.Entries != 4 || got.Playlists != 2 {
		t.Errorf("InspectPlaylist = %+v", got)
	}

	gw := startGateway(t, GatewayConfig{Hosts: DefaultHosts.Media})
	local := got.Serve(gw)
	if !strings.HasSuffix(local, "/playlist.m3u8") {
		t.Errorf("served top playlist = %s", local)
	}
	gw.mu.Lock()
	defer gw.mu.Unlock()
	var served, upstream []string
	for _, r := range gw.routes {
		if r.body != nil {
			served = append(served, string(r.body))
		} else {
			upstream = append(upstream, r.upstream)
		}
	}
	if len(served) != 2 || len(upstream) != 4 || upstream[0] != hlsBase+"init.mp4" || upstream[1] != hlsBase+"media_w1_0.ts" {
		t.Fatalf("routes: served %d playlists, upstream %v", len(served), upstream)
	}
	for _, body := range served {
		for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
			if strings.Contains(line, "://") && !strings.Contains(line, "http://127.0.0.1:") {
				t.Errorf("served playlist line %q does not go through the gateway", line)
			}
		}
		if strings.Contains(body, "evil.test") || strings.Contains(body, "SESSION-DATA") {
			t.Errorf("unknown URI tag passed through:\n%s", body)
		}
	}
	if !strings.Contains(served[0], "#EXT-X-MAP:URI=\"http://127.0.0.1:") || !strings.Contains(served[0], "#EXT-X-KEY:METHOD=NONE") {
		t.Errorf("chunklist not rewritten:\n%s", served[0])
	}
	if !strings.Contains(served[1], "/r/4/chunklist_w1.m3u8") {
		t.Errorf("master does not point at the served chunklist:\n%s", served[1])
	}
}

func TestInspectPlaylistRejectsUnsafeOrBrokenPlaylists(t *testing.T) {
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
		"httpproxy segment":       {map[string]string{top: "#EXTM3U\n#EXTINF:10,\nhttpproxy://archive-stream.granicus.com/s.ts\n#EXT-X-ENDLIST\n"}, ErrForbiddenHost},
		"crypto segment":          {map[string]string{top: "#EXTM3U\n#EXTINF:10,\ncrypto+https://archive-stream.granicus.com/s.ts\n#EXT-X-ENDLIST\n"}, ErrForbiddenHost},
		"concat segment":          {map[string]string{top: "#EXTM3U\n#EXTINF:10,\nconcat:https://archive-stream.granicus.com/a.ts\n#EXT-X-ENDLIST\n"}, ErrForbiddenHost},
		"subfile segment":         {map[string]string{top: "#EXTM3U\n#EXTINF:10,\nsubfile,,start,0,end,0,,:https://archive-stream.granicus.com/a.ts\n#EXT-X-ENDLIST\n"}, ErrMalformed},
		"data segment":            {map[string]string{top: "#EXTM3U\n#EXTINF:10,\ndata:video/mp2t;base64,AAAA\n#EXT-X-ENDLIST\n"}, ErrForbiddenHost},
		"segment on port 8443":    {map[string]string{top: "#EXTM3U\n#EXTINF:10,\nhttps://archive-stream.granicus.com:8443/s.ts\n#EXT-X-ENDLIST\n"}, ErrForbiddenHost},
		"map off the list":        {map[string]string{top: "#EXTM3U\n#EXT-X-MAP:URI=\"https://evil.test/init.mp4\"\n#EXTINF:10,\ns.ts\n#EXT-X-ENDLIST\n"}, ErrForbiddenHost},
		"variant off the list":    {map[string]string{top: "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nhttps://evil.test/v.m3u8\n"}, ErrForbiddenHost},
		"audio off the list":      {map[string]string{top: "#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"a\",URI=\"https://evil.test/a.m3u8\"\n"}, ErrForbiddenHost},
		"self reference":          {map[string]string{top: "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nplaylist.m3u8\n"}, ErrMalformed},
		"segment is the list":     {map[string]string{top: "#EXTM3U\n#EXTINF:10,\nplaylist.m3u8\n#EXT-X-ENDLIST\n"}, ErrMalformed},
		"variant listed twice":    {map[string]string{top: "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nv.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=2\nv.m3u8\n", hlsBase + "v.m3u8": mediaPlaylist(1, "")}, ErrMalformed},
		"nested master":           {map[string]string{top: wowzaMaster, hlsBase + "chunklist_w1.m3u8": wowzaMaster}, ErrMalformed},
		"master without variants": {map[string]string{top: "#EXTM3U\n#EXT-X-I-FRAME-STREAM-INF:BANDWIDTH=1,URI=\"i.m3u8\"\n"}, ErrNoMedia},
		"missing variant":         {map[string]string{top: wowzaMaster}, fetch.ErrInvalidURL},
		"live":                    {map[string]string{top: "#EXTM3U\n#EXTINF:10,\ns.ts\n"}, ErrIncomplete},
		"no segments":             {map[string]string{top: "#EXTM3U\n#EXT-X-ENDLIST\n"}, ErrNoMedia},
		"bad duration":            {map[string]string{top: "#EXTM3U\n#EXTINF:ten,\ns.ts\n#EXT-X-ENDLIST\n"}, ErrMalformed},
		"stray uri":               {map[string]string{top: "#EXTM3U\ns.ts\n#EXT-X-ENDLIST\n"}, ErrMalformed},
		"master and media":        {map[string]string{top: "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nv.m3u8\n#EXTINF:10,\ns.ts\n"}, ErrMalformed},
		"too many segments":       {map[string]string{top: mediaPlaylist(maxPlaylistEntries+1, "")}, ErrLimitExceeded},
		"too many variants":       {map[string]string{top: "#EXTM3U\n" + strings.Repeat("#EXT-X-STREAM-INF:BANDWIDTH=1\nv.m3u8\n", maxVariants+1)}, ErrLimitExceeded},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := InspectPlaylist(t.Context(), playlists(tc.pages), DefaultHosts.Media, Guard{}, top)
			if errors.Is(tc.want, fetch.ErrInvalidURL) {
				var fetchErr *fetch.Error
				if !errors.As(err, &fetchErr) || fetchErr.StatusCode != 404 {
					t.Errorf("InspectPlaylist = %v, want the 404", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("InspectPlaylist = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestInspectPlaylistTakesTheLongestRendition(t *testing.T) {
	top := hlsBase + "playlist.m3u8"
	fake := playlists(map[string]string{
		top:                    "#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"a\",URI=\"audio.m3u8\"\n#EXT-X-STREAM-INF:BANDWIDTH=1,AUDIO=\"a\"\nvideo.m3u8\n",
		hlsBase + "audio.m3u8": mediaPlaylist(5, "#EXT-X-MAP:URI=\"init.mp4\"\n"),
		hlsBase + "video.m3u8": mediaPlaylist(6, ""),
	})
	got, err := InspectPlaylist(t.Context(), fake, DefaultHosts.Media, Guard{}, top)
	if err != nil || got.Duration != 60*time.Second || got.AudioDuration != 50*time.Second || got.Playlists != 3 || got.Entries != 12 {
		t.Errorf("InspectPlaylist = %+v, %v", got, err)
	}
}

func TestInspectPlaylistUsesTheAudioRenditionForTheAudioLength(t *testing.T) {
	top := hlsBase + "playlist.m3u8"
	fake := playlists(map[string]string{
		top:                    "#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"a\",URI=\"audio.m3u8\"\n#EXT-X-STREAM-INF:BANDWIDTH=1,AUDIO=\"a\"\nvideo.m3u8\n",
		hlsBase + "audio.m3u8": mediaPlaylist(4, ""),
		hlsBase + "video.m3u8": mediaPlaylist(6, ""),
	})
	got, err := InspectPlaylist(t.Context(), fake, DefaultHosts.Media, Guard{}, top)
	if err != nil || got.Duration != 60*time.Second || got.AudioDuration != 40*time.Second {
		t.Errorf("InspectPlaylist = %+v, %v", got, err)
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
