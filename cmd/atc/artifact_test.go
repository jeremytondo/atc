package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/cli"
)

func cliTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

// publishArtifactCLI publishes through the client the CLI uses, the way
// the authoring commands will.
func publishArtifactCLI(t *testing.T, title string) (api.ArtifactPublication, []byte) {
	t.Helper()
	client, _, err := cli.NewClient(io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	source := cliTarGz(t, map[string]string{"src/document/index.tsx": title})
	result, err := client.PublishArtifact(context.Background(), api.ArtifactPublishParams{Title: title, PublicationID: "pub-" + title},
		bytes.NewReader(cliTarGz(t, map[string]string{"index.html": "<p>" + title + "</p>"})), bytes.NewReader(source))
	if err != nil {
		t.Fatal(err)
	}
	return result, source
}

func TestArtifactCLILifecycle(t *testing.T) {
	startTestServer(t)
	t.Chdir(t.TempDir())

	stdout, _, err := runCLI(t, "artifact", "list")
	if err != nil || !strings.Contains(stdout, "no artifacts") {
		t.Fatalf("empty list = %q, %v", stdout, err)
	}
	published, source := publishArtifactCLI(t, "Design")
	id := published.Artifact.ID
	projectID := createProjectCLI(t, t.TempDir())

	stdout, _, err = runCLI(t, "artifact", "list")
	if err != nil || !strings.Contains(stdout, id) || !strings.Contains(stdout, "Design") || !strings.Contains(stdout, "http://127.0.0.1:7332/a/"+id+"/") {
		t.Fatalf("list output:\n%s\n%v", stdout, err)
	}
	stdout, _, err = runCLI(t, "artifact", "get", id)
	if err != nil || !strings.Contains(stdout, "Design") || !strings.Contains(stdout, "version") {
		t.Fatalf("get output:\n%s\n%v", stdout, err)
	}

	stdout, _, err = runCLI(t, "artifact", "update", id, "--title", "Renamed", "--project", projectID)
	if err != nil || !strings.Contains(stdout, "Renamed") || !strings.Contains(stdout, projectID) {
		t.Fatalf("update output:\n%s\n%v", stdout, err)
	}
	stdout, _, err = runCLI(t, "artifact", "list", "--project", projectID)
	if err != nil || !strings.Contains(stdout, id) {
		t.Fatalf("list by project:\n%s\n%v", stdout, err)
	}
	if _, _, err := runCLI(t, "artifact", "update", id, "--project", "proj-nope1"); err == nil || !strings.Contains(err.Error(), "422") {
		t.Errorf("unknown project = %v, want a 422", err)
	}
	stdout, _, err = runCLI(t, "artifact", "update", id, "--no-project")
	if err != nil || strings.Contains(stdout, projectID) {
		t.Fatalf("unassign output:\n%s\n%v", stdout, err)
	}

	// Restore republishes version 1 as version 2 with its own title by
	// default; the history shows both.
	stdout, _, err = runCLI(t, "artifact", "restore", id, "1")
	if err != nil || !strings.Contains(stdout, "published "+id+" version 2") || !strings.Contains(stdout, "Design") || !strings.Contains(stdout, "/a/"+id+"/v/2/") {
		t.Fatalf("restore output:\n%s\n%v", stdout, err)
	}
	stdout, _, err = runCLI(t, "artifact", "versions", id)
	if err != nil || !strings.Contains(stdout, "from 1") || strings.Count(stdout, "\n") != 3 {
		t.Fatalf("versions output:\n%s\n%v", stdout, err)
	}
	if _, _, err := runCLI(t, "artifact", "restore", id, "1", "--base", "1"); err == nil || !strings.Contains(err.Error(), "409") {
		t.Errorf("stale restore = %v, want a 409", err)
	}
	if _, _, err := runCLI(t, "artifact", "restore", id, "v9"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("restore unknown version = %v, want a 404", err)
	}

	// Source downloads the current version by default, a named one on
	// request, and never overwrites.
	stdout, _, err = runCLI(t, "artifact", "source", id)
	if err != nil || !strings.Contains(stdout, "wrote "+id+"-v2-source.tar.gz") {
		t.Fatalf("source output:\n%s\n%v", stdout, err)
	}
	if got, err := os.ReadFile(id + "-v2-source.tar.gz"); err != nil || !bytes.Equal(got, source) {
		t.Errorf("downloaded source = %d bytes, %v; want the upload", len(got), err)
	}
	if _, _, err := runCLI(t, "artifact", "source", id); err == nil {
		t.Error("second download overwrote the file")
	}
	out := filepath.Join(t.TempDir(), "v1.tar.gz")
	if _, _, err := runCLI(t, "artifact", "source", id, "v1", "-o", out); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(out); err != nil || !bytes.Equal(got, source) {
		t.Errorf("version 1 source = %d bytes, %v", len(got), err)
	}
	stdout, _, err = runCLI(t, "artifact", "source", id, "1", "-o", "-")
	if err != nil || stdout != string(source) {
		t.Errorf("source to stdout = %d bytes, %v", len(stdout), err)
	}

	stdout, _, err = runCLI(t, "artifact", "delete", id)
	if err != nil || !strings.Contains(stdout, "deleted "+id) {
		t.Fatalf("delete output:\n%s\n%v", stdout, err)
	}
	if _, _, err := runCLI(t, "artifact", "get", id); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("get after delete = %v, want a 404", err)
	}
	if _, _, err := runCLI(t, "artifact", "delete", id); err == nil {
		t.Error("second delete succeeded")
	}
}
