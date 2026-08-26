package api

import (
	"fmt"
	"net/http"
)

// Problem is the RFC 7807 error body every non-2xx response carries: Huma
// emits it for routing and validation errors, and the server's auth
// middleware deliberately mimics the same shape, so callers see one error
// contract regardless of which layer rejected. Defined fresh rather than
// importing Huma's model to keep this package stdlib-only.
//
// Callers branch with errors.As when they care which failure it was and
// print the error when they don't.
type Problem struct {
	Title  string        `json:"title"`
	Status int           `json:"status"`
	Detail string        `json:"detail,omitempty"`
	Errors []ErrorDetail `json:"errors,omitempty"`

	// ServerVersion is the Atc-Server-Version response header, carried on
	// the error so a tokenless probe reads the server's version off a 401.
	ServerVersion string `json:"-"`
}

// ErrorDetail is one Huma validation failure inside a Problem.
type ErrorDetail struct {
	Message  string `json:"message,omitempty"`
	Location string `json:"location,omitempty"`
	Value    any    `json:"value,omitempty"`
}

func (p *Problem) Error() string {
	message := p.Detail
	if message == "" {
		message = p.Title
	}
	if message == "" {
		message = http.StatusText(p.Status)
	}
	return fmt.Sprintf("%s (HTTP %d)", message, p.Status)
}
