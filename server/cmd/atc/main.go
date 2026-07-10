package main

import (
	"os"

	"github.com/jeremytondo/atelier-code/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
