package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/jeremytondo/atc/internal/api"
)

// Every error the server writes is an api.Problem with a code (ATC-294):
// the typed refusals the handlers build carry their own, the errors Huma
// raises itself — validation, negotiation, an unknown id — are built
// through newProblem, which derives one from the status, and the mux's
// own plain-text answers for paths no operation claims are rewritten by
// problemMux. The OpenAPI document's error schema derives from the same
// type.

// Installed at package initialization, once and before any handler can
// serve: the variable is process-global, and the schema Huma generates at
// registration must match what it writes at runtime.
func init() {
	huma.NewError = newProblem
}

// problem builds a typed refusal. title defaults to the status text.
func problem(status int, code, detail string) *api.Problem {
	return &api.Problem{Title: http.StatusText(status), Status: status, Code: code, Detail: detail}
}

// newProblem is the huma.NewError replacement: framework errors get the
// same shape as the handlers' refusals, with a status-derived code.
func newProblem(status int, msg string, errs ...error) huma.StatusError {
	p := problem(status, statusCode(status), msg)
	for _, err := range errs {
		if err == nil {
			continue
		}
		var detailer huma.ErrorDetailer
		if errors.As(err, &detailer) {
			detail := detailer.ErrorDetail()
			p.Errors = append(p.Errors, api.ErrorDetail{Message: detail.Message, Location: detail.Location, Value: detail.Value})
			continue
		}
		p.Errors = append(p.Errors, api.ErrorDetail{Message: err.Error()})
	}
	return p
}

// statusCode is the code of a framework-raised error: the failures a
// client can meet on any route, named once, with the status text as the
// slug for the rest so no problem is ever codeless.
func statusCode(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return api.CodeUnauthorized
	case http.StatusNotFound:
		return api.CodeNotFound
	case http.StatusUnprocessableEntity, http.StatusBadRequest:
		return api.CodeValidationFailed
	}
	text := http.StatusText(status)
	if text == "" {
		text = "http error"
	}
	return strings.ToLower(strings.NewReplacer(" ", "_", "-", "_", "'", "").Replace(text))
}

// writeProblem serves a problem from plain net/http — the layers outside
// Huma's operations.
func writeProblem(w http.ResponseWriter, p *api.Problem) {
	w.Header().Set("Content-Type", p.ContentType(""))
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

// problemMux serves the mux, rewriting its own plain-text 404 and 405
// (a path no operation claims, a method the path lacks) as problems.
// Status and the Allow header survive; the mux's body is dropped. Every
// other response passes through untouched.
func problemMux(mux http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(&muxErrorWriter{ResponseWriter: w, request: r}, r)
	})
}

type muxErrorWriter struct {
	http.ResponseWriter
	request *http.Request
	// rewritten marks a header written as a problem: the mux's body that
	// follows is discarded.
	rewritten bool
}

func (w *muxErrorWriter) WriteHeader(status int) {
	if (status == http.StatusNotFound || status == http.StatusMethodNotAllowed) &&
		strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		w.rewritten = true
		w.Header().Del("X-Content-Type-Options")
		detail := "no such route: " + w.request.Method + " " + w.request.URL.Path
		writeProblem(w.ResponseWriter, problem(status, statusCode(status), detail))
		return
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *muxErrorWriter) Write(body []byte) (int, error) {
	if w.rewritten {
		return len(body), nil
	}
	return w.ResponseWriter.Write(body)
}

// Unwrap lets http.ResponseController reach the underlying writer (the
// SSE feed flushes and sets write deadlines through it).
func (w *muxErrorWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
