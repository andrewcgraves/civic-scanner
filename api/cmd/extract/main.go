package main

import (
	"os"

	"github.com/andrewcgraves/civic-scanner/api/internal/extract"
)

func main() {
	os.Exit(extract.RunChild(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Exit))
}
