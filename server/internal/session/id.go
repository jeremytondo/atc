package session

import "github.com/jeremytondo/atelier-code/internal/publicid"

// newID generates an opaque Atelier Code-owned session id, independent from any
// multiplexer naming scheme.
func newID() (string, error) {
	return publicid.New("ses_")
}
