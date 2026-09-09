package documents

// The reader: what the document origin serves. Every artifact page is
// the version's own index.html with two things done at serve time — the
// build's relative asset references pinned to that version's permanent
// path, so a page resolves one version and loads that version's assets
// throughout, and a JSON <script> carrying the current metadata and
// history the reader header shows. Assets are served verbatim. Nothing
// here is built, executed, or fetched at request time.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/artifacts"
)

// MetadataID is the id of the JSON <script> element the reader injects
// into every page; the platform's reader header reads it.
const MetadataID = "atc-document"

// Metadata is the injected document: the artifact as it currently
// stands, the version being read, and the whole history, with paths
// (not absolute URLs) so links work on every origin the page is read
// from.
type Metadata struct {
	ArtifactID     string    `json:"artifactId"`
	Title          string    `json:"title"`
	CurrentVersion int       `json:"currentVersion"`
	LatestPath     string    `json:"latestPath"`
	Latest         bool      `json:"latest"`
	Version        Version   `json:"version"`
	Versions       []Version `json:"versions"`
}

// Version is one history entry.
type Version struct {
	Number       int                `json:"number"`
	Title        string             `json:"title"`
	PublishedAt  time.Time          `json:"publishedAt"`
	Platform     string             `json:"platform,omitempty"`
	RestoredFrom int                `json:"restoredFrom,omitempty"`
	Source       api.ArtifactSource `json:"source,omitzero"`
	Path         string             `json:"path"`
}

// policy is the browser response policy on every response: only the
// published assets may load, no live network, no workers, no embedding,
// no form submission. Inline styles are allowed because the platform's
// production output (Radix, Tailwind) sets them; scripts must be files.
const policy = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; font-src 'self' data:; media-src 'self'; connect-src 'none'; " +
	"worker-src 'none'; child-src 'none'; frame-src 'none'; object-src 'none'; manifest-src 'none'; " +
	"form-action 'none'; base-uri 'none'; frame-ancestors 'none'"

// Resolver is the reader's view of the Artifacts domain
// (artifacts.Service in production).
type Resolver interface {
	Document(ctx context.Context, id string, version int) (artifacts.Document, error)
}

// Handler serves published artifacts: /a/{id}/ resolves to the current
// version, /a/{id}/v/{n}/ to one version, and assets beneath a version
// path come from that version's build.
func Handler(resolver Resolver, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	h := &handler{resolver: resolver, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", h.root)
	mux.HandleFunc("GET /a/{id}", h.redirectToSlash)
	mux.HandleFunc("GET /a/{id}/{$}", h.page)
	mux.HandleFunc("GET /a/{id}/v/{number}", h.redirectToSlash)
	mux.HandleFunc("GET /a/{id}/v/{number}/{$}", h.page)
	mux.HandleFunc("GET /a/{id}/v/{number}/{asset...}", h.asset)
	return withPolicy(mux)
}

type handler struct {
	resolver Resolver
	logger   *slog.Logger
}

// withPolicy sets the response policy headers on every response, error
// pages included, and turns the mux's own text errors into ours.
func withPolicy(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("Content-Security-Policy", policy)
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("X-Frame-Options", "DENY")
		header.Set("Cross-Origin-Opener-Policy", "same-origin")
		header.Set("Cross-Origin-Resource-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func (h *handler) root(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte("ATC document origin: open an artifact link.\n"))
}

func (h *handler) redirectToSlash(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
}

// resolve reads the route's artifact and version (0 for the main link).
func (h *handler) resolve(w http.ResponseWriter, r *http.Request) (artifacts.Document, bool) {
	number := 0
	if raw := r.PathValue("number"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			http.NotFound(w, r)
			return artifacts.Document{}, false
		}
		number = n
	}
	doc, err := h.resolver.Document(r.Context(), r.PathValue("id"), number)
	switch {
	case errors.Is(err, artifacts.ErrNotFound), errors.Is(err, artifacts.ErrVersionNotFound):
		http.NotFound(w, r)
		return artifacts.Document{}, false
	case err != nil:
		h.logger.Error("resolving document", "path", r.URL.Path, "error", err)
		http.Error(w, "document unavailable", http.StatusInternalServerError)
		return artifacts.Document{}, false
	}
	return doc, true
}

func (h *handler) page(w http.ResponseWriter, r *http.Request) {
	doc, ok := h.resolve(w, r)
	if !ok {
		return
	}
	h.servePage(w, r, doc)
}

func (h *handler) servePage(w http.ResponseWriter, r *http.Request, doc artifacts.Document) {
	raw, err := os.ReadFile(path.Join(doc.BuildDir, "index.html"))
	if err != nil {
		h.logger.Error("reading document page", "artifact", doc.Artifact.ID, "version", doc.Version.Number, "error", err)
		http.Error(w, "document content missing", http.StatusInternalServerError)
		return
	}
	page, err := Render(raw, doc)
	if err != nil {
		h.logger.Error("rendering document page", "artifact", doc.Artifact.ID, "version", doc.Version.Number, "error", err)
		http.Error(w, "document unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The main link must resolve afresh on every load; a fixed link's
	// page changes too, as history grows and the artifact is renamed.
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "", time.Time{}, strings.NewReader(page))
}

func (h *handler) asset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("asset")
	if !fs.ValidPath(name) {
		http.NotFound(w, r)
		return
	}
	doc, ok := h.resolve(w, r)
	if !ok {
		return
	}
	if name == "index.html" {
		h.servePage(w, r, doc)
		return
	}
	file, err := os.DirFS(doc.BuildDir).Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	seeker, ok := file.(io.ReadSeeker)
	if !ok {
		http.Error(w, "document content unavailable", http.StatusInternalServerError)
		return
	}
	// A version's assets never change: cache them for as long as browsers
	// allow.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, name, info.ModTime(), seeker)
}

// Render produces the page served for doc from its build's index.html:
// relative asset references pinned to the version's permanent path and
// the metadata script injected at the end of <head>. Exported so the
// platform's own tests can render exactly what the server would.
func Render(index []byte, doc artifacts.Document) (string, error) {
	metadata := Metadata{
		ArtifactID:     doc.Artifact.ID,
		Title:          doc.Artifact.Title,
		CurrentVersion: doc.Artifact.CurrentVersion,
		LatestPath:     artifacts.ReaderPath(doc.Artifact.ID, 0),
		Latest:         doc.Latest(),
		Version:        version(doc.Version),
	}
	for _, v := range doc.Versions {
		metadata.Versions = append(metadata.Versions, version(v))
	}
	// json.Marshal escapes <, >, and & so the document cannot close the
	// script element early.
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	base := artifacts.ReaderPath(doc.Artifact.ID, doc.Version.Number)
	page := string(index)
	page = strings.ReplaceAll(page, `src="./`, `src="`+base)
	page = strings.ReplaceAll(page, `href="./`, `href="`+base)
	script := `<script id="` + MetadataID + `" type="application/json">` + string(encoded) + `</script>`
	if at := strings.Index(strings.ToLower(page), "</head>"); at >= 0 {
		return page[:at] + script + page[at:], nil
	}
	return script + page, nil
}

func version(v api.ArtifactVersion) Version {
	return Version{
		Number: v.Number, Title: v.Title, PublishedAt: v.PublishedAt, Platform: v.Platform,
		RestoredFrom: v.RestoredFrom, Source: v.Source, Path: artifacts.ReaderPath(v.ArtifactID, v.Number),
	}
}
