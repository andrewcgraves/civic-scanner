package primegov

var SBCTA = Config{
	Host:              "sbcta.primegov.com",
	AgendaHost:        "sbcta.primegov.com",
	CommitteeIDs:      []int{1},
	BoardName:         "Board of Directors",
	AgendaTemplates:   []string{"Packet"},
	EvidenceTemplates: []string{"Video Link"},
	CancelTemplates:   []string{"Cancellation Notice"},
	PageURL:           "https://www.gosbcta.com/board/upcoming-meetings-agenda/",
	TimeZone:          "America/Los_Angeles",
	StartTime:         "10:00",
}
