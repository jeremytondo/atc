package api

import (
	"fmt"
	"net/http"
)

// Problem is the RFC 7807 error body every non-2xx response carries: the
// server hands Huma this type for its own routing and validation errors,
// the handlers' typed refusals build it directly, and the auth middleware
// emits the same shape, so callers see one error contract regardless of
// which layer rejected. Defined fresh rather than importing Huma's model
// to keep this package stdlib-only; GetStatus and ContentType are what
// let Huma serve it as-is.
//
// Code is the stable machine-readable slug (ATC-294) every server-sent
// problem carries: clients branch on it — title and detail are for
// people and may be reworded. A Problem the client synthesizes for a
// non-problem body has none. Callers branch with errors.As when they
// care which failure it was and print the error when they don't.
type Problem struct {
	Title  string        `json:"title"`
	Status int           `json:"status"`
	Code   string        `json:"code" doc:"Stable machine-readable failure slug."`
	Detail string        `json:"detail,omitempty"`
	Errors []ErrorDetail `json:"errors,omitempty"`

	// ServerVersion is the Atc-Server-Version response header, carried on
	// the error so a tokenless probe reads the server's version off a 401.
	ServerVersion string `json:"-"`
}

// ErrorDetail is one validation failure inside a Problem.
type ErrorDetail struct {
	Message  string `json:"message,omitempty"`
	Location string `json:"location,omitempty"`
	Value    any    `json:"value,omitempty"`
}

// Problem codes the server emits for its typed refusals. Framework
// errors (routing, validation, negotiation) carry a code derived from
// the status: unauthorized, not_found, validation_failed, and otherwise
// the status text as a slug (method_not_allowed, internal_server_error).
const (
	CodeUnauthorized             = "unauthorized"
	CodeNotFound                 = "not_found"
	CodeValidationFailed         = "validation_failed"
	CodeTerminalNotFound         = "terminal_not_found"
	CodeTerminalDirectoryInvalid = "terminal_directory_invalid"
	CodeSpaceNotFound            = "space_not_found"
	CodeSpaceDefault             = "space_default"
	CodeSpaceDeleting            = "space_deleting"
	CodeSpaceDirectoryInvalid    = "space_directory_invalid"
	CodeProjectNotFound          = "project_not_found"
	CodeProjectDirectoryInvalid  = "project_directory_invalid"
	CodeProjectDirectoryTaken    = "project_directory_taken"
	CodeThreadNotFound           = "thread_not_found"
	CodeThreadActive             = "thread_active"
	CodeThreadNotResumable       = "thread_not_terminal_resumable"
	CodeIntegrationNotFound      = "integration_not_found"
	CodeAppNotFound              = "app_not_found"
	CodeAppNotTerminalCapable    = "app_not_terminal_capable"
	CodeAppUnavailable           = "app_unavailable"
	CodeLaunchModeConflict       = "launch_mode_conflict"
	CodeResumeUnavailable        = "resume_unavailable"
)

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

// GetStatus is Huma's StatusError seam: the response status the problem
// is written with.
func (p *Problem) GetStatus() int { return p.Status }

// ContentType is Huma's ContentTypeFilter seam: problems are served as
// application/problem+json (or the CBOR equivalent) whatever the
// negotiated type.
func (p *Problem) ContentType(negotiated string) string {
	if negotiated == "application/cbor" {
		return "application/problem+cbor"
	}
	return "application/problem+json"
}
