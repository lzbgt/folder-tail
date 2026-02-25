package main

import (
	"os"

	"folder-tail/internal/cli"
)

var version = "dev"

func main() {
	os.Exit(cli.Run(os.Args[1:], version))
}
