package main

import (
	"fmt"
	"os"

	"github.com/jeremytondo/atg-poc/internal/portal"
)

func main() {
	app, err := portal.New(os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "portal:", err)
		os.Exit(1)
	}

	if err := app.Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "portal:", err)
		os.Exit(1)
	}
}
