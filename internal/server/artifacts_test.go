package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
)

// tarGz builds a gzip tar of regular files from name → content.
func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

// multipartRequest drives a publication through the handler directly,
// for status-code assertions the client does not expose.
func (f *fixture) multipartRequest(t *testing.T, path string, params api.ArtifactPublishParams, build, source []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	encoded, _ := json.Marshal(params)
	part, _ := form.CreatePart(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="params"`}, "Content-Type": {"application/json"}})
	_, _ = part.Write(encoded)
	for name, archive := range map[string][]byte{"build": build, "source": source} {
		if archive == nil {
			continue
		}
		part, _ := form.CreatePart(textproto.MIMEHeader{
			"Content-Disposition": {`form-data; name="` + name + `"; filename="x.tar.gz"`}, "Content-Type": {"application/gzip"},
		})
		_, _ = part.Write(archive)
	}
	_ = form.Close()
	req := httptest.NewRequest(http.MethodPost, path, &body)
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", form.FormDataContentType())
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func (f *fixture) client(t *testing.T) *api.Client {
	t.Helper()
	srv := httptest.NewServer(f.handler)
	t.Cleanup(srv.Close)
	return api.NewClient(srv.URL, testToken, testVersion, nil, nil)
}

func TestArtifactPublicationOverTheWire(t *testing.T) {
	f := newFixture(t)
	client := f.client(t)
	ctx := context.Background()
	build1 := tarGz(t, map[string]string{"index.html": `<html><head></head><body><script src="./assets/a.js"></script></body></html>`, "assets/a.js": "1"})
	source1 := tarGz(t, map[string]string{"src/document/index.tsx": "one", "package.json": "{}"})

	first, err := client.PublishArtifact(ctx, api.ArtifactPublishParams{
		Title: "Design", PublicationID: "pub-1", Platform: "atc test",
		Source: api.ArtifactSource{Thread: "thrd-aaaaa", Revision: "abc", Links: []string{"https://linear.app/x"}},
	}, bytes.NewReader(build1), bytes.NewReader(source1))
	if err != nil {
		t.Fatal(err)
	}
	id := first.Artifact.ID
	if first.Artifact.CurrentVersion != 1 || first.Version.Number != 1 || first.Artifact.URL != "http://127.0.0.1:7332/a/"+id+"/" || first.Version.URL != "http://127.0.0.1:7332/a/"+id+"/v/1/" {
		t.Errorf("first = %+v", first)
	}
	if first.Version.Source.Thread != "thrd-aaaaa" || first.Version.Platform != "atc test" {
		t.Errorf("provenance = %+v", first.Version)
	}
	if got, err := os.ReadFile(filepath.Join(f.artifactRoot, id, "1", "build", "assets", "a.js")); err != nil || string(got) != "1" {
		t.Errorf("stored asset = %q, %v", got, err)
	}

	// The same publication again is a 200 with the same result; a new
	// one on the current base is a 201.
	rec := f.multipartRequest(t, "/v1/artifacts", api.ArtifactPublishParams{Title: "Design", PublicationID: "pub-1"}, build1, source1)
	if rec.Code != http.StatusOK {
		t.Fatalf("replay: %d %s", rec.Code, rec.Body)
	}
	var replay api.ArtifactPublication
	if err := json.Unmarshal(rec.Body.Bytes(), &replay); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(first, replay); diff != "" {
		t.Errorf("replay mismatch (-want +got):\n%s", diff)
	}
	build2 := tarGz(t, map[string]string{"index.html": "<p>two</p>"})
	rec = f.multipartRequest(t, "/v1/artifacts/"+id+"/versions", api.ArtifactPublishParams{Title: "Design v2", PublicationID: "pub-2", BaseVersion: 1}, build2, source1)
	if rec.Code != http.StatusCreated {
		t.Fatalf("second: %d %s", rec.Code, rec.Body)
	}

	// A stale base is a 409 naming the current version; a bad package a
	// 422; an unknown artifact a 404.
	rec = f.multipartRequest(t, "/v1/artifacts/"+id+"/versions", api.ArtifactPublishParams{Title: "late", PublicationID: "pub-3", BaseVersion: 1}, build2, source1)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"code":"artifact_base_stale"`) || !strings.Contains(rec.Body.String(), `"value":"2"`) {
		t.Errorf("stale: %d %s", rec.Code, rec.Body)
	}
	rec = f.multipartRequest(t, "/v1/artifacts", api.ArtifactPublishParams{Title: "bad", PublicationID: "pub-4"}, tarGz(t, map[string]string{"main.html": "x"}), source1)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"code":"artifact_package_invalid"`) {
		t.Errorf("bad package: %d %s", rec.Code, rec.Body)
	}
	rec = f.multipartRequest(t, "/v1/artifacts", api.ArtifactPublishParams{Title: "bad", PublicationID: "pub-5"}, build1, nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("missing source: %d %s", rec.Code, rec.Body)
	}
	rec = f.multipartRequest(t, "/v1/artifacts", api.ArtifactPublishParams{PublicationID: "pub-6"}, build1, source1)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("missing title: %d %s", rec.Code, rec.Body)
	}
	rec = f.multipartRequest(t, "/v1/artifacts/artf-nope1/versions", api.ArtifactPublishParams{Title: "x", PublicationID: "pub-7"}, build2, source1)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"code":"artifact_not_found"`) {
		t.Errorf("unknown artifact: %d %s", rec.Code, rec.Body)
	}
	if rec := f.request(t, http.MethodPost, "/v1/artifacts", `{"title":"json"}`); rec.Code < http.StatusBadRequest || rec.Code >= http.StatusInternalServerError {
		t.Errorf("JSON publish: %d %s", rec.Code, rec.Body)
	}

	// Restore version 1 as version 3 via the client, then read history,
	// one version, and the source download.
	restored, err := client.PublishArtifactVersion(ctx, id, api.ArtifactPublishParams{Title: "Design", PublicationID: "pub-8", BaseVersion: 2, RestoreFrom: 1}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Version.Number != 3 || restored.Version.RestoredFrom != 1 || restored.Version.Platform != "atc test" {
		t.Errorf("restored = %+v", restored.Version)
	}
	versions, err := client.ArtifactVersions(ctx, id)
	if err != nil || len(versions) != 3 || versions[0].Title != "Design" || versions[1].Title != "Design v2" {
		t.Errorf("versions = %+v, %v", versions, err)
	}
	if version, err := client.ArtifactVersion(ctx, id, 2); err != nil || version.Title != "Design v2" || version.URL != "http://127.0.0.1:7332/a/"+id+"/v/2/" {
		t.Errorf("version 2 = %+v, %v", version, err)
	}
	source, err := client.ArtifactSource(ctx, id, 3)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(source)
	_ = source.Close()
	if !bytes.Equal(got, source1) {
		t.Error("downloaded source differs from the upload restored")
	}
	if _, err := client.ArtifactSource(ctx, id, 9); !isProblem(err, http.StatusNotFound, api.CodeArtifactVersionNotFound) {
		t.Errorf("source of unknown version = %v", err)
	}
	if _, err := client.ArtifactVersion(ctx, id, 0); !isProblem(err, http.StatusUnprocessableEntity, "") {
		t.Errorf("version 0 = %v", err)
	}

	// Metadata: rename and Project assignment, both refused when invalid.
	project := f.createProject(t, canonicalDir(t, t.TempDir()))
	updated, err := client.UpdateArtifact(ctx, id, api.ArtifactUpdateParams{Title: api.Some("Renamed"), ProjectID: api.Some(project.ID)})
	if err != nil || updated.Title != "Renamed" || updated.ProjectID != project.ID || updated.CurrentVersion != 3 {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	if list, err := client.Artifacts(ctx, project.ID); err != nil || len(list) != 1 || list[0].ID != id {
		t.Errorf("list by project = %+v, %v", list, err)
	}
	if _, err := client.UpdateArtifact(ctx, id, api.ArtifactUpdateParams{ProjectID: api.Some("proj-nope1")}); !isProblem(err, http.StatusUnprocessableEntity, api.CodeProjectNotFound) {
		t.Errorf("unknown project = %v", err)
	}
	if _, err := client.UpdateArtifact(ctx, id, api.ArtifactUpdateParams{}); !isProblem(err, http.StatusUnprocessableEntity, api.CodeValidationFailed) {
		t.Errorf("empty patch = %v", err)
	}
	if rec := f.request(t, http.MethodPatch, "/v1/artifacts/"+id, `{"title":""}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("empty title: %d %s", rec.Code, rec.Body)
	}
	cleared, err := client.UpdateArtifact(ctx, id, api.ArtifactUpdateParams{ProjectID: api.Clear[string]()})
	if err != nil || cleared.ProjectID != "" {
		t.Errorf("clear project = %+v, %v", cleared, err)
	}
	if _, err := client.Artifact(ctx, "artf-nope1"); !isProblem(err, http.StatusNotFound, api.CodeArtifactNotFound) {
		t.Errorf("unknown get = %v", err)
	}

	// Project deletion preserves the artifact unassigned; artifact
	// deletion is total.
	if _, err := client.UpdateArtifact(ctx, id, api.ArtifactUpdateParams{ProjectID: api.Some(project.ID)}); err != nil {
		t.Fatal(err)
	}
	if rec := f.request(t, http.MethodDelete, "/v1/projects/"+project.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete project: %d %s", rec.Code, rec.Body)
	}
	if after, err := client.Artifact(ctx, id); err != nil || after.ProjectID != "" || after.CurrentVersion != 3 {
		t.Errorf("after project delete = %+v, %v", after, err)
	}
	if err := client.DeleteArtifact(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteArtifact(ctx, id); !isProblem(err, http.StatusNotFound, api.CodeArtifactNotFound) {
		t.Errorf("second delete = %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.artifactRoot, id)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("content after delete: %v", err)
	}
	if list, err := client.Artifacts(ctx, ""); err != nil || len(list) != 0 {
		t.Errorf("list after delete = %+v, %v", list, err)
	}
}

func TestArtifactLinksFollowTailnetExposure(t *testing.T) {
	f := newFixture(t)
	client := f.client(t)
	f.documents.status.Tailnet = api.DocumentsTailnet{State: api.TailnetReady, URL: "https://node.ts.net:7332"}
	published, err := client.PublishArtifact(context.Background(), api.ArtifactPublishParams{Title: "t", PublicationID: "pub-1"},
		bytes.NewReader(tarGz(t, map[string]string{"index.html": "x"})), bytes.NewReader(tarGz(t, map[string]string{"a": "b"})))
	if err != nil {
		t.Fatal(err)
	}
	id := published.Artifact.ID
	if published.Artifact.TailnetURL != "https://node.ts.net:7332/a/"+id+"/" || published.Version.TailnetURL != "https://node.ts.net:7332/a/"+id+"/v/1/" {
		t.Errorf("tailnet links = %q, %q", published.Artifact.TailnetURL, published.Version.TailnetURL)
	}
	status, err := client.Documents(context.Background())
	if err != nil || status.State != api.DocumentsReady || status.Tailnet.URL != "https://node.ts.net:7332" {
		t.Errorf("documents status = %+v, %v", status, err)
	}
}

func TestArtifactsInOpenAPI(t *testing.T) {
	f := newFixture(t)
	rec := f.request(t, http.MethodGet, "/openapi.json", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("openapi: %d", rec.Code)
	}
	var doc struct {
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	for path, method := range map[string]string{
		"/v1/artifacts": "post", "/v1/artifacts/{id}": "patch", "/v1/artifacts/{id}/versions": "post",
		"/v1/artifacts/{id}/versions/{number}": "get", "/v1/artifacts/{id}/versions/{number}/source": "get", "/v1/documents": "get",
	} {
		if _, ok := doc.Paths[path][method]; !ok {
			t.Errorf("openapi lacks %s %s", method, path)
		}
	}
	if raw := string(doc.Paths["/v1/artifacts"]["post"]); !strings.Contains(raw, "multipart/form-data") {
		t.Errorf("create-artifact request body: %s", raw)
	}
	for _, schema := range []string{"ArtifactPublication", "ArtifactVersionList", "Documents"} {
		if _, ok := doc.Components.Schemas[schema]; !ok {
			t.Errorf("openapi lacks schema %s", schema)
		}
	}
}

func isProblem(err error, status int, code string) bool {
	problem, ok := errors.AsType[*api.Problem](err)
	return ok && problem.Status == status && (code == "" || problem.Code == code)
}
