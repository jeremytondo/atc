// Command stdlib-server serves the stdlib candidate on 127.0.0.1:7331 for
// manual curl inspection and binary-size measurement.
package main

import (
	"log"
	"net/http"

	"github.com/jeremytondo/atc/experiments/http-framework/stdlib"
)

func main() {
	log.Fatal(http.ListenAndServe("127.0.0.1:7331",
		stdlib.NewHandler("atc_spike-token", "v0.0.0-spike")))
}
