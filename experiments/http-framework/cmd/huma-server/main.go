// Command huma-server serves the Huma candidate on 127.0.0.1:7332 for
// manual curl inspection and binary-size measurement.
package main

import (
	"log"
	"net/http"

	humachassis "github.com/jeremytondo/atc/experiments/http-framework/huma"
)

func main() {
	log.Fatal(http.ListenAndServe("127.0.0.1:7332",
		humachassis.NewHandler("atc_spike-token", "v0.0.0-spike")))
}
