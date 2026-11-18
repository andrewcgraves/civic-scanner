package main

import (
	_ "time/tzdata"

	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/legistar"
)

var adapters = map[string]sources.Constructor{
	"la-metro": legistar.Constructor(legistar.LAMetro),
}
