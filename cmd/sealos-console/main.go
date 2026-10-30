package main

import (
	"github.com/raymondtc/sealos-console/internal/console"
	"os"
)

var version = "dev"

func main() { os.Exit(console.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, version)) }
