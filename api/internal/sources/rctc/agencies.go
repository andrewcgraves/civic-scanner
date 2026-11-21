package rctc

var RCTC = Config{
	PageURL:      "https://www.rctc.org/meetings-and-agendas/",
	AgendaDomain: "rctc.org",
	Boards: []Board{
		{Name: "Commission Meeting", Monthly: true},
		{Name: "Special Commission Meeting"},
	},
	CommitteeWords: []string{"Committee", "Executive", "BI", "WRC", "TAC", "CSTAC", "TPOC"},
	TimeZone:       "America/Los_Angeles",
	StartTime:      "09:30",
	Castus: Castus{
		ListingURL:  "https://2kbyogxrg4.execute-api.us-west-2.amazonaws.com/5fe25821fa91b30007ea7498/playlist/Riverside%20County%20Transportation%20Commission",
		TitlePrefix: "RCTC General Commission",
	},
}
