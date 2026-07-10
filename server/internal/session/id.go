package session

import "github.com/jeremytondo/atc/internal/publicid"

// newID generates an opaque atc-owned session id, independent from any
// multiplexer naming scheme.
func newID() (string, error) {
	return publicid.New("ses_")
}
