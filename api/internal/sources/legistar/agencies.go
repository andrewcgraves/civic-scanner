package legistar

var LAMetro = Config{
	Client:               "metro",
	BodyIDs:              []int{138, 199},
	TimeZone:             "America/Los_Angeles",
	Granicus:             Granicus{Host: "metro.granicus.com", ViewID: 2},
	FinalMinutesMeanHeld: true,
}

var OCTA = Config{
	Client:   "octa",
	BodyIDs:  []int{138},
	TimeZone: "America/Los_Angeles",
	Granicus: Granicus{Host: "octa.granicus.com", ViewID: 2},
}
