package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// The publication methods stream one multipart request: JSON params,
// then the archives as gzip parts, omitted for a restoration.
func TestArtifactPublishMethods(t *testing.T) {
	type upload struct {
		Path, Params, Build, Source, BuildType string
		Parts                                  []string
	}
	var got upload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data; boundary=") {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parsing form: %v", err)
			return
		}
		got = upload{Path: r.URL.Path, Params: strings.TrimSpace(r.FormValue("params"))}
		for name := range r.MultipartForm.Value {
			got.Parts = append(got.Parts, name)
		}
		for name, headers := range r.MultipartForm.File {
			got.Parts = append(got.Parts, name)
			file, _ := headers[0].Open()
			body, _ := io.ReadAll(file)
			_ = file.Close()
			switch name {
			case "build":
				got.Build, got.BuildType = string(body), headers[0].Header.Get("Content-Type")
			case "source":
				got.Source = string(body)
			}
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(ArtifactPublication{Artifact: Artifact{ID: "artf-x7k2f"}, Version: ArtifactVersion{Number: 2}})
	}))
	defer srv.Close()
	client := NewClient(srv.URL, testToken, testClientVersion, nil, nil)
	ctx := context.Background()

	result, err := client.PublishArtifact(ctx, ArtifactPublishParams{Title: "Design", PublicationID: "pub-1"},
		strings.NewReader("BUILD"), strings.NewReader("SOURCE"))
	if err != nil || result.Artifact.ID != "artf-x7k2f" || result.Version.Number != 2 {
		t.Fatalf("PublishArtifact = %+v, %v", result, err)
	}
	if got.Path != "/v1/artifacts" || got.Params != `{"title":"Design","publicationId":"pub-1"}` || got.Build != "BUILD" || got.Source != "SOURCE" || got.BuildType != "application/gzip" {
		t.Errorf("upload = %+v", got)
	}
	if len(got.Parts) != 3 {
		t.Errorf("parts = %v, want params, build, source", got.Parts)
	}

	got = upload{}
	if _, err := client.PublishArtifactVersion(ctx, "artf-x7k2f", ArtifactPublishParams{Title: "Again", PublicationID: "pub-2", BaseVersion: 2, RestoreFrom: 1}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got.Path != "/v1/artifacts/artf-x7k2f/versions" || got.Params != `{"title":"Again","baseVersion":2,"publicationId":"pub-2","restoreFrom":1}` {
		t.Errorf("restore upload = %+v", got)
	}
	if diff := cmp.Diff([]string{"params"}, got.Parts); diff != "" {
		t.Errorf("restore parts mismatch (-want +got):\n%s", diff)
	}
}

func TestArtifactMethods(t *testing.T) {
	type call struct {
		Method, Path, Query, Body string
	}
	var got call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = call{r.Method, r.URL.Path, r.URL.RawQuery, strings.TrimSpace(string(body))}
		switch {
		case strings.HasSuffix(r.URL.Path, "/source"):
			w.Header().Set("Content-Type", "application/gzip")
			_, _ = w.Write([]byte("GZ"))
		case r.URL.Path == "/v1/artifacts":
			_ = json.NewEncoder(w).Encode(ArtifactList{Artifacts: []Artifact{{ID: "artf-x7k2f"}}})
		case r.URL.Path == "/v1/artifacts/artf-x7k2f/versions":
			_ = json.NewEncoder(w).Encode(ArtifactVersionList{Versions: []ArtifactVersion{{Number: 1}}})
		case strings.HasPrefix(r.URL.Path, "/v1/artifacts/artf-x7k2f/versions/"):
			_ = json.NewEncoder(w).Encode(ArtifactVersion{Number: 3})
		case r.URL.Path == "/v1/document-origin":
			_ = json.NewEncoder(w).Encode(DocumentOrigin{State: OriginReady, URL: "http://127.0.0.1:7332"})
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			_ = json.NewEncoder(w).Encode(Artifact{ID: "artf-x7k2f"})
		}
	}))
	defer srv.Close()
	client := NewClient(srv.URL, testToken, testClientVersion, nil, nil)
	ctx := context.Background()

	if list, err := client.Artifacts(ctx, "proj-aaaaa"); err != nil || len(list) != 1 {
		t.Fatalf("Artifacts = %+v, %v", list, err)
	}
	if got.Method != http.MethodGet || got.Path != "/v1/artifacts" || got.Query != "project=proj-aaaaa" {
		t.Errorf("Artifacts call = %+v", got)
	}
	if _, err := client.Artifacts(ctx, ""); err != nil || got.Query != "" {
		t.Errorf("Artifacts(all) = %v, query %q", err, got.Query)
	}
	if artifact, err := client.Artifact(ctx, "artf-x7k2f"); err != nil || artifact.ID != "artf-x7k2f" || got.Path != "/v1/artifacts/artf-x7k2f" {
		t.Errorf("Artifact = %+v, %v; call %+v", artifact, err, got)
	}
	if _, err := client.UpdateArtifact(ctx, "artf-x7k2f", ArtifactUpdateParams{Title: Some("New"), ProjectID: Clear[string]()}); err != nil {
		t.Fatal(err)
	}
	if got.Method != http.MethodPatch || got.Body != `{"title":"New","projectId":null}` {
		t.Errorf("UpdateArtifact call = %+v", got)
	}
	if err := client.DeleteArtifact(ctx, "artf-x7k2f"); err != nil || got.Method != http.MethodDelete || got.Path != "/v1/artifacts/artf-x7k2f" {
		t.Errorf("DeleteArtifact = %v; call %+v", err, got)
	}
	if versions, err := client.ArtifactVersions(ctx, "artf-x7k2f"); err != nil || len(versions) != 1 || got.Path != "/v1/artifacts/artf-x7k2f/versions" {
		t.Errorf("ArtifactVersions = %+v, %v; call %+v", versions, err, got)
	}
	if version, err := client.ArtifactVersion(ctx, "artf-x7k2f", 3); err != nil || version.Number != 3 || got.Path != "/v1/artifacts/artf-x7k2f/versions/3" {
		t.Errorf("ArtifactVersion = %+v, %v; call %+v", version, err, got)
	}
	source, err := client.ArtifactSource(ctx, "artf-x7k2f", 3)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(source)
	_ = source.Close()
	if string(body) != "GZ" || got.Path != "/v1/artifacts/artf-x7k2f/versions/3/source" {
		t.Errorf("ArtifactSource = %q; call %+v", body, got)
	}
	if status, err := client.DocumentOrigin(ctx); err != nil || status.State != OriginReady {
		t.Errorf("DocumentOrigin = %+v, %v", status, err)
	}
}
