package extract

import "bytes"

const interstitialWindow = 256 << 10

var interstitialMarkers = []string{
	"pardon our interruption",
	"_incapsula_resource",
	"incapsula incident id",
	"request unsuccessful. incapsula",
	"/cdn-cgi/challenge-platform",
	"cf-browser-verification",
	"attention required! | cloudflare",
	"checking if the site connection is secure",
	"enable javascript and cookies to continue",
	"please verify you are a human",
	"sucuri website firewall",
	"ddos protection by",
	"you don't have permission to access",
	"the requested url was rejected. please consult with your administrator",
	"web page blocked",
}

func Interstitial(body []byte) (string, bool) {
	head := bytes.ToLower(body[:min(len(body), interstitialWindow)])
	for _, marker := range interstitialMarkers {
		if bytes.Contains(head, []byte(marker)) {
			return marker, true
		}
	}
	return "", false
}
