package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/artifacts"
)

// The Artifacts resource (ATC-318): publication as a multipart upload of
// the build and source archives with JSON params, the five standard
// verbs on /v1/artifacts, the version history with per-version source
// download, and the document origin's status. Handlers are thin Huma
// wrappers; policy lives in the artifacts service.

// maxUploadBytes bounds a publication request on the wire; the archives'
// decompressed contents are bounded separately by the service's limits.
const maxUploadBytes = 512 << 20

// DocumentOriginReporter is the document origin's status seam
// (documents.Service in production). Its report also carries the base
// URLs the reader links on every artifact and version are built from.
type DocumentOriginReporter interface {
	Status(ctx context.Context) api.DocumentOrigin
}

// publishForm is the multipart body of a publication: JSON params plus
// the two archives, which a restoration omits.
type publishForm struct {
	Params api.ArtifactPublishParams `form:"params" contentType:"application/json" doc:"Publication parameters."`
	Build  huma.FormFile             `form:"build" contentType:"application/gzip" required:"false" doc:"gzip tar of the static build; index.html at its root, regular files only."`
	Source huma.FormFile             `form:"source" contentType:"application/gzip" required:"false" doc:"gzip tar of the authoring snapshot: document source, platform source, build configuration, dependency manifest, and lockfile."`
}

type publicationOutput struct {
	Status int
	Body   api.ArtifactPublication
}

type artifactOutput struct {
	Body api.Artifact
}

type artifactListOutput struct {
	Body api.ArtifactList
}

type artifactVersionOutput struct {
	Body api.ArtifactVersion
}

type artifactVersionListOutput struct {
	Body api.ArtifactVersionList
}

type artifactIDInput struct {
	ID string `path:"id" doc:"Artifact identifier."`
}

type artifactVersionInput struct {
	ID     string `path:"id" doc:"Artifact identifier."`
	Number int    `path:"number" minimum:"1" doc:"Version number."`
}

type documentOriginOutput struct {
	Body api.DocumentOrigin
}

const publishDescription = "A multipart upload: `params` (JSON), `build` (gzip tar of the static build, index.html at its root), and `source` (gzip tar of the authoring snapshot). Archives may contain only regular files and directories at safe relative paths, within bounded counts and sizes. The version becomes visible only once both snapshots are stored; a repeated `publicationId` returns the version it committed (200) instead of publishing again."

func registerArtifacts(humaAPI huma.API, service *artifacts.Service, origin DocumentOriginReporter) {
	links := linker{origin: origin}
	create := huma.Operation{
		OperationID:   "create-artifact",
		Method:        http.MethodPost,
		Path:          "/v1/artifacts",
		Summary:       "Publish a new artifact",
		Description:   "Creates an artifact whose first version is the uploaded publication. " + publishDescription,
		DefaultStatus: http.StatusCreated,
		Responses:     map[string]*huma.Response{"200": {Description: "The publication had already completed; its result."}},
		Errors:        []int{http.StatusUnprocessableEntity, http.StatusConflict, http.StatusGone},
		MaxBodyBytes:  maxUploadBytes,
		Middlewares:   huma.Middlewares{limitBody},
	}
	huma.Register(humaAPI, create, func(ctx context.Context, input *struct {
		RawBody huma.MultipartFormFiles[publishForm]
	}) (*publicationOutput, error) {
		return publish(ctx, service, links, "", input.RawBody.Data())
	})
	create.Responses["200"].Content = create.Responses["201"].Content

	huma.Register(humaAPI, huma.Operation{
		OperationID: "list-artifacts",
		Method:      http.MethodGet,
		Path:        "/v1/artifacts",
		Summary:     "List artifacts",
		Description: "Every artifact in creation order, or only those assigned to one Project.",
	}, func(ctx context.Context, input *struct {
		Project string `query:"project" doc:"Only artifacts assigned to this Project."`
	}) (*artifactListOutput, error) {
		list, err := service.List(ctx, input.Project)
		if err != nil {
			return nil, mapArtifactError(err)
		}
		local, tailnet := links.bases(ctx)
		for i := range list {
			link(&list[i].URL, &list[i].TailnetURL, local, tailnet, artifacts.ReaderPath(list[i].ID, 0))
		}
		return &artifactListOutput{Body: api.ArtifactList{Artifacts: list}}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "get-artifact",
		Method:      http.MethodGet,
		Path:        "/v1/artifacts/{id}",
		Summary:     "Get an artifact",
	}, func(ctx context.Context, input *artifactIDInput) (*artifactOutput, error) {
		artifact, err := service.Get(ctx, input.ID)
		if err != nil {
			return nil, mapArtifactError(err)
		}
		links.artifact(ctx, &artifact)
		return &artifactOutput{Body: artifact}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "update-artifact",
		Method:      http.MethodPatch,
		Path:        "/v1/artifacts/{id}",
		Summary:     "Rename an artifact or change its Project",
		Description: "A merge patch of title and projectId: omitted fields are unchanged, a null projectId unassigns. Renaming changes the current label listings and the main link's reader header use; no URL changes, and no stored version is touched.",
	}, func(ctx context.Context, input *struct {
		ID   string `path:"id" doc:"Artifact identifier."`
		Body api.ArtifactUpdateParams
	}) (*artifactOutput, error) {
		artifact, err := service.Update(ctx, input.ID, input.Body)
		if err != nil {
			return nil, mapArtifactError(err)
		}
		links.artifact(ctx, &artifact)
		return &artifactOutput{Body: artifact}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID:   "delete-artifact",
		Method:        http.MethodDelete,
		Path:          "/v1/artifacts/{id}",
		Summary:       "Delete an artifact and its whole history",
		Description:   "Removes the metadata and every stored version; the reader links stop resolving. Local working copies elsewhere are untouched, and a later publication naming this id fails rather than recreating it.",
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, input *artifactIDInput) (*struct{}, error) {
		if err := service.Delete(ctx, input.ID); err != nil {
			return nil, mapArtifactError(err)
		}
		return nil, nil
	})

	publishVersion := huma.Operation{
		OperationID:   "publish-artifact-version",
		Method:        http.MethodPost,
		Path:          "/v1/artifacts/{id}/versions",
		Summary:       "Publish a new version of an artifact",
		Description:   "Appends a version and makes it current; `baseVersion` must be the current version or the request fails with a conflict naming it (nothing is merged or overwritten). With `restoreFrom`, the named version's stored build and source are republished unchanged as the new current version and the archives are omitted. " + publishDescription,
		DefaultStatus: http.StatusCreated,
		Responses:     map[string]*huma.Response{"200": {Description: "The publication had already completed; its result."}},
		Errors:        []int{http.StatusNotFound, http.StatusUnprocessableEntity, http.StatusConflict, http.StatusGone},
		MaxBodyBytes:  maxUploadBytes,
		Middlewares:   huma.Middlewares{limitBody},
	}
	huma.Register(humaAPI, publishVersion, func(ctx context.Context, input *struct {
		ID      string `path:"id" doc:"Artifact identifier."`
		RawBody huma.MultipartFormFiles[publishForm]
	}) (*publicationOutput, error) {
		return publish(ctx, service, links, input.ID, input.RawBody.Data())
	})
	publishVersion.Responses["200"].Content = publishVersion.Responses["201"].Content

	huma.Register(humaAPI, huma.Operation{
		OperationID: "list-artifact-versions",
		Method:      http.MethodGet,
		Path:        "/v1/artifacts/{id}/versions",
		Summary:     "List an artifact's versions",
		Description: "The whole history, oldest first; every version keeps its permanent link.",
	}, func(ctx context.Context, input *artifactIDInput) (*artifactVersionListOutput, error) {
		versions, err := service.Versions(ctx, input.ID)
		if err != nil {
			return nil, mapArtifactError(err)
		}
		local, tailnet := links.bases(ctx)
		for i := range versions {
			link(&versions[i].URL, &versions[i].TailnetURL, local, tailnet, artifacts.ReaderPath(versions[i].ArtifactID, versions[i].Number))
		}
		return &artifactVersionListOutput{Body: api.ArtifactVersionList{Versions: versions}}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "get-artifact-version",
		Method:      http.MethodGet,
		Path:        "/v1/artifacts/{id}/versions/{number}",
		Summary:     "Get one version",
	}, func(ctx context.Context, input *artifactVersionInput) (*artifactVersionOutput, error) {
		version, err := service.Version(ctx, input.ID, input.Number)
		if err != nil {
			return nil, mapArtifactError(err)
		}
		links.version(ctx, &version)
		return &artifactVersionOutput{Body: version}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "get-artifact-source",
		Method:      http.MethodGet,
		Path:        "/v1/artifacts/{id}/versions/{number}/source",
		Summary:     "Download a version's source snapshot",
		Description: "The authoring snapshot exactly as uploaded: a gzip tar of the document source, platform source, build configuration, dependency manifest, and lockfile.",
		Responses: map[string]*huma.Response{"200": {
			Description: "The source archive.",
			Content:     map[string]*huma.MediaType{"application/gzip": {Schema: &huma.Schema{Type: "string", Format: "binary"}}},
		}},
		Errors: []int{http.StatusNotFound},
	}, func(ctx context.Context, input *artifactVersionInput) (*huma.StreamResponse, error) {
		source, err := service.OpenSource(ctx, input.ID, input.Number)
		if err != nil {
			return nil, mapArtifactError(err)
		}
		return &huma.StreamResponse{Body: func(ctx huma.Context) {
			defer func() { _ = source.Close() }()
			ctx.SetHeader("Content-Type", "application/gzip")
			ctx.SetHeader("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-v%d-source.tar.gz"`, input.ID, input.Number))
			_, _ = io.Copy(ctx.BodyWriter(), source)
		}}, nil
	})
}

// registerDocumentOrigin mounts the document origin's status resource.
func registerDocumentOrigin(humaAPI huma.API, reporter DocumentOriginReporter) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "get-document-origin",
		Method:      http.MethodGet,
		Path:        "/v1/document-origin",
		Summary:     "Document origin status",
		Description: "Readiness of the listener that serves published artifacts to browsers, its local base URL, its tailnet exposure (which follows the API's), and the reason for any failure. Publishing, metadata, and source retrieval work here whatever this reports.",
	}, func(ctx context.Context, _ *struct{}) (*documentOriginOutput, error) {
		return &documentOriginOutput{Body: reporter.Status(ctx)}, nil
	})
}

// linker fills in reader links from the document origin's current
// bases: the local one always, the tailnet one while it is serving.
type linker struct {
	origin DocumentOriginReporter
}

func (l linker) bases(ctx context.Context) (local, tailnet string) {
	status := l.origin.Status(ctx)
	if status.Tailnet.State == api.TailnetReady {
		tailnet = status.Tailnet.URL
	}
	return status.URL, tailnet
}

func (l linker) artifact(ctx context.Context, artifact *api.Artifact) {
	local, tailnet := l.bases(ctx)
	link(&artifact.URL, &artifact.TailnetURL, local, tailnet, artifacts.ReaderPath(artifact.ID, 0))
}

func (l linker) version(ctx context.Context, version *api.ArtifactVersion) {
	local, tailnet := l.bases(ctx)
	link(&version.URL, &version.TailnetURL, local, tailnet, artifacts.ReaderPath(version.ArtifactID, version.Number))
}

func link(url, tailnetURL *string, local, tailnet, path string) {
	*url = local + path
	if tailnet != "" {
		*tailnetURL = tailnet + path
	}
}

// limitBody caps the request body before the multipart parser reads it;
// Huma's own MaxBodyBytes does not apply to multipart bodies.
func limitBody(ctx huma.Context, next func(huma.Context)) {
	r, w := humago.Unwrap(ctx)
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	next(ctx)
}

// publish runs one publication from the parsed form, closing the
// uploaded files.
func publish(ctx context.Context, service *artifacts.Service, links linker, artifactID string, form *publishForm) (*publicationOutput, error) {
	var build, source io.Reader
	if form.Build.IsSet {
		defer func() { _ = form.Build.Close() }()
		build = form.Build
	}
	if form.Source.IsSet {
		defer func() { _ = form.Source.Close() }()
		source = form.Source
	}
	result, replayed, err := service.Publish(ctx, artifactID, form.Params, build, source)
	if err != nil {
		return nil, mapArtifactError(err)
	}
	links.artifact(ctx, &result.Artifact)
	links.version(ctx, &result.Version)
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
	}
	return &publicationOutput{Status: status, Body: result}, nil
}

func mapArtifactError(err error) error {
	var stale *artifacts.StaleBaseError
	var pkg *artifacts.PackageError
	switch {
	case errors.Is(err, artifacts.ErrNotFound):
		return problem(http.StatusNotFound, api.CodeArtifactNotFound, "artifact not found")
	case errors.Is(err, artifacts.ErrVersionNotFound):
		return problem(http.StatusNotFound, api.CodeArtifactVersionNotFound, "artifact version not found")
	case errors.As(err, &stale):
		p := problem(http.StatusConflict, api.CodeArtifactBaseStale, err.Error())
		p.Errors = []api.ErrorDetail{{Message: "current version", Location: "baseVersion", Value: strconv.Itoa(stale.Current)}}
		return p
	case errors.As(err, &pkg):
		return problem(http.StatusUnprocessableEntity, api.CodeArtifactPackageInvalid, err.Error())
	case errors.Is(err, artifacts.ErrProjectUnknown):
		return problem(http.StatusUnprocessableEntity, api.CodeProjectNotFound, err.Error())
	case errors.Is(err, artifacts.ErrInvalidUpdate), errors.Is(err, artifacts.ErrInvalidPublication):
		return problem(http.StatusUnprocessableEntity, api.CodeValidationFailed, err.Error())
	case errors.Is(err, artifacts.ErrPublicationTaken):
		return problem(http.StatusConflict, api.CodeValidationFailed, err.Error())
	case errors.Is(err, artifacts.ErrPublicationDeleted):
		return problem(http.StatusGone, api.CodeArtifactDeleted, err.Error())
	}
	return err
}
