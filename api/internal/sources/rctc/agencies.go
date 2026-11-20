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
}
