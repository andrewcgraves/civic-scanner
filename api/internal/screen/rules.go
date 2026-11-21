package screen

import (
	"bytes"
	"net/url"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const maxLinksPerPage = 5000

type link struct {
	url      *url.URL
	text     string
	embedded bool
}

type signal struct {
	supports Supports
	signal   string
}

var agendaPlatforms = map[string]string{
	"legistar.com":         "legistar",
	"primegov.com":         "primegov",
	"civicclerk.com":       "civicclerk",
	"boarddocs.com":        "boarddocs",
	"novusagenda.com":      "novusagenda",
	"iqm2.com":             "iqm2",
	"civicweb.net":         "civicweb",
	"escribemeetings.com":  "escribe",
	"municodemeetings.com": "municode_meetings",
	"agendasuite.org":      "agendasuite",
}

var recordingPlatforms = map[string]string{
	"swagit.com":  "swagit",
	"boxcast.tv":  "boxcast",
	"boxcast.com": "boxcast",
	"viebit.com":  "viebit",
}

var videoHosts = map[string]string{
	"youtube.com":          "youtube",
	"youtu.be":             "youtube",
	"youtube-nocookie.com": "youtube",
	"vimeo.com":            "vimeo",
}

var mediaSuffixes = []string{".mp4", ".m3u8", ".mp3", ".m4a", ".mov"}

func under(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}

func lookup(host string, platforms map[string]string) (string, bool) {
	for domain, name := range platforms {
		if under(host, domain) {
			return name, true
		}
	}
	return "", false
}

func videoHost(host string) bool {
	_, ok := lookup(host, videoHosts)
	return ok
}

func meetingPlatform(host string) bool {
	if under(host, "granicus.com") {
		return true
	}
	if _, ok := lookup(host, agendaPlatforms); ok {
		return true
	}
	_, ok := lookup(host, recordingPlatforms)
	return ok
}

func classify(l link, home bool) []signal {
	host := strings.ToLower(l.url.Hostname())
	path := strings.ToLower(l.url.Path)
	words := strings.ToLower(l.text + " " + l.url.Path)
	var out []signal
	switch {
	case under(host, "granicus.com"):
		switch {
		case strings.Contains(path, "mediaplayer") || strings.Contains(path, "/player/"):
			out = append(out, signal{SupportsRecordings, "granicus_video"})
		case containsAny(path, []string{"viewpublisher", "agendaviewer", "metaviewer", "agendaonline"}):
			out = append(out, signal{SupportsAgendas, "granicus"})
		}
	case under(host, "legistar.com") && strings.Contains(path, "video.aspx"):
		out = append(out, signal{SupportsRecordings, "legistar_video"})
	}
	if name, ok := lookup(host, agendaPlatforms); ok && len(out) == 0 {
		out = append(out, signal{SupportsAgendas, name})
	}
	if name, ok := lookup(host, recordingPlatforms); ok {
		out = append(out, signal{SupportsRecordings, name})
	}
	if name, ok := lookup(host, videoHosts); ok && ((l.embedded && !home) || containsAny(words, recordingWords)) {
		out = append(out, signal{SupportsRecordings, name})
	}
	if strings.HasSuffix(path, ".pdf") && strings.Contains(words, "agenda") {
		out = append(out, signal{SupportsAgendas, "agenda_pdf"})
	}
	if strings.Contains(path, "/agendacenter") {
		out = append(out, signal{SupportsAgendas, "agenda_center"})
	}
	for _, suffix := range mediaSuffixes {
		if strings.HasSuffix(path, suffix) {
			out = append(out, signal{SupportsRecordings, "media_file"})
			break
		}
	}
	return out
}

func extractLinks(page *url.URL, body []byte) []link {
	z := html.NewTokenizer(bytes.NewReader(body))
	var links []link
	var open *link
	var text strings.Builder
	for len(links) < maxLinksPerPage {
		switch z.Next() {
		case html.ErrorToken:
			return links
		case html.TextToken:
			if open != nil {
				text.Write(z.Text())
				text.WriteByte(' ')
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			if open != nil && atom.Lookup(name) == atom.A {
				open.text = strings.Join(strings.Fields(open.text+" "+text.String()), " ")
				links = append(links, *open)
				open, text = nil, strings.Builder{}
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			tag := atom.Lookup(name)
			attrs := map[string]string{}
			for hasAttr {
				var key, val []byte
				key, val, hasAttr = z.TagAttr()
				attrs[string(key)] = string(val)
			}
			switch tag {
			case atom.A, atom.Area:
				u := resolve(page, attrs["href"])
				if u == nil {
					continue
				}
				l := link{url: u, text: attrs["title"] + " " + attrs["aria-label"]}
				if tag == atom.Area {
					links = append(links, l)
					continue
				}
				if open != nil {
					open.text = strings.Join(strings.Fields(open.text+" "+text.String()), " ")
					links = append(links, *open)
				}
				open, text = &l, strings.Builder{}
			case atom.Iframe, atom.Embed, atom.Video, atom.Audio, atom.Source:
				if u := resolve(page, attrs["src"]); u != nil {
					links = append(links, link{url: u, text: attrs["title"], embedded: true})
				}
			}
		}
	}
	return links
}

func resolve(page *url.URL, ref string) *url.URL {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.HasPrefix(ref, "#") {
		return nil
	}
	u, err := page.Parse(ref)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil
	}
	u.Fragment = ""
	return u
}
