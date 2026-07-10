package main

import (
	"os"

	"github.com/jeremytondo/atc/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
