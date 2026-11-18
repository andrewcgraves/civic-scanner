package main

import (
	"os"
	"syscall"

	"github.com/andrewcgraves/civic-scanner/api/internal/limit"
)

func main() {
	os.Exit(limit.Run(os.Args[1:], os.Stderr, syscall.Exec))
}
