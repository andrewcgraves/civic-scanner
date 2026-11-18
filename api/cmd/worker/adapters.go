package main

import (
	_ "time/tzdata"

	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/granicus"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/legistar"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/vctc"
)

var adapters = map[string]sources.Constructor{
	"la-metro":  legistar.Constructor(legistar.LAMetro),
	"metrolink": granicus.Constructor(granicus.Metrolink),
	"octa":      legistar.Constructor(legistar.OCTA),
	"vctc":      vctc.Constructor(vctc.VCTC),
}
