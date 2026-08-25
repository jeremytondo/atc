package stdlib_test

import (
	"testing"

	"github.com/jeremytondo/atc/experiments/http-framework/internal/conformance"
	"github.com/jeremytondo/atc/experiments/http-framework/stdlib"
)

func TestConformance(t *testing.T) {
	conformance.Run(t, stdlib.NewHandler(conformance.Token, conformance.ServerVersion))
}
