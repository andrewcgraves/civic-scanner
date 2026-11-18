package vctc

import "time"

var VCTC = Config{
	Host: "www.goventura.org",
	Boards: []Board{
		{TypeID: 25, Name: "Commission Meeting"},
		{TypeID: 33, Name: "Special Meeting"},
	},
	CommitteeWords: []string{"TRANSCOM", "TTAC", "CTAC", "SSTAC", "HVPAC", "HVTAC", "CEPAC", "CETAC", "SPBLAC", "CALVANS", "Committee"},
	TimeZone:       "America/Los_Angeles",
	StartTime:      "09:00",
}

var VCTCYouTube = YouTube{
	ChannelID:   "UCer8W37pepfQH8mhDbTBRjg",
	TitlePrefix: "VCTC Commission Meeting",
	MaxDelay:    72 * time.Hour,
}
