package store

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestArtifactsRoundTrip(t *testing.T) {
	s, _ := openStore(t)
	ctx := context.Background()
	insertProject(t, s, "proj-aaaaa", "/a")
	artifacts := s.Artifacts()

	first := ArtifactVersionRecord{
		ArtifactID: "artf-aaaaa", Number: 1, Title: "Design", Platform: "atc v1", PublishedAt: at(1),
		PublicationID: "pub-1", Thread: "thrd-aaaaa", Revision: "abc123", Links: []string{"https://linear.app/x"},
	}
	if ok, err := artifacts.Create(ctx, ArtifactRecord{
		ID: "artf-aaaaa", Title: "Design", ProjectID: "proj-aaaaa", CreatedAt: at(1), UpdatedAt: at(1),
	}, first); err != nil || !ok {
		t.Fatalf("Create = %v, %v; want true", ok, err)
	}
	if ok, err := artifacts.Create(ctx, ArtifactRecord{
		ID: "artf-aaaaa", Title: "dup", CreatedAt: at(2), UpdatedAt: at(2),
	}, ArtifactVersionRecord{ArtifactID: "artf-aaaaa", Number: 1, PublicationID: "pub-x", PublishedAt: at(2)}); err != nil || ok {
		t.Fatalf("Create(collision) = %v, %v; want false", ok, err)
	}
	if _, err := artifacts.Create(ctx, ArtifactRecord{
		ID: "artf-bbbbb", Title: "stray", ProjectID: "proj-zzzzz", CreatedAt: at(2), UpdatedAt: at(2),
	}, ArtifactVersionRecord{ArtifactID: "artf-bbbbb", Number: 1, PublicationID: "pub-y", PublishedAt: at(2)}); !errors.Is(err, ErrForeignKeyViolation) {
		t.Fatalf("Create(unknown project) = %v, want ErrForeignKeyViolation", err)
	}

	want := ArtifactRecord{ID: "artf-aaaaa", Title: "Design", ProjectID: "proj-aaaaa", CurrentVersion: 1, CreatedAt: at(1), UpdatedAt: at(1)}
	got, ok, err := artifacts.Get(ctx, "artf-aaaaa")
	if err != nil || !ok {
		t.Fatalf("Get = %v, %v", ok, err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Get mismatch (-want +got):\n%s", diff)
	}
	if _, ok, err := artifacts.Get(ctx, "artf-zzzzz"); err != nil || ok {
		t.Errorf("Get(unknown) = %v, %v; want false", ok, err)
	}

	second := ArtifactVersionRecord{ArtifactID: "artf-aaaaa", Number: 2, Title: "Design v2", PublishedAt: at(3), PublicationID: "pub-2"}
	if err := artifacts.AppendVersion(ctx, second, 1); err != nil {
		t.Fatalf("AppendVersion = %v", err)
	}
	stale := ArtifactVersionRecord{ArtifactID: "artf-aaaaa", Number: 2, Title: "late", PublishedAt: at(4), PublicationID: "pub-3"}
	var staleErr *StaleBaseError
	if err := artifacts.AppendVersion(ctx, stale, 1); !errors.As(err, &staleErr) || staleErr.Current != 2 {
		t.Fatalf("AppendVersion(stale) = %v, want StaleBaseError{2}", err)
	}
	if err := artifacts.AppendVersion(ctx, ArtifactVersionRecord{ArtifactID: "artf-zzzzz", Number: 1, PublicationID: "pub-4", PublishedAt: at(4)}, 0); !errors.Is(err, ErrArtifactMissing) {
		t.Fatalf("AppendVersion(missing) = %v, want ErrArtifactMissing", err)
	}
	if err := artifacts.AppendVersion(ctx, ArtifactVersionRecord{ArtifactID: "artf-aaaaa", Number: 5, PublicationID: "pub-5", PublishedAt: at(4)}, 2); err == nil {
		t.Fatal("AppendVersion(number skips) = nil, want error")
	}

	versions, err := artifacts.Versions(ctx, "artf-aaaaa")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]ArtifactVersionRecord{first, second}, versions); diff != "" {
		t.Errorf("Versions mismatch (-want +got):\n%s", diff)
	}
	version, ok, err := artifacts.Version(ctx, "artf-aaaaa", 2)
	if err != nil || !ok {
		t.Fatalf("Version = %v, %v", ok, err)
	}
	if diff := cmp.Diff(second, version); diff != "" {
		t.Errorf("Version mismatch (-want +got):\n%s", diff)
	}
	if _, ok, err := artifacts.Version(ctx, "artf-aaaaa", 3); err != nil || ok {
		t.Errorf("Version(3) = %v, %v; want false", ok, err)
	}
	byPublication, found, deleted, err := artifacts.Publication(ctx, "pub-1")
	if err != nil || !found || deleted || byPublication.Number != 1 {
		t.Errorf("Publication = %+v, %v, %v, %v; want version 1", byPublication, found, deleted, err)
	}
	if _, found, deleted, err := artifacts.Publication(ctx, "pub-never"); err != nil || found || deleted {
		t.Errorf("Publication(unknown) = %v, %v, %v; want neither", found, deleted, err)
	}

	// The append set the current title and updated_at; the current
	// version is derived.
	want.Title, want.UpdatedAt, want.CurrentVersion = "Design v2", at(3), 2
	if got, _, err := artifacts.Get(ctx, "artf-aaaaa"); err != nil {
		t.Fatal(err)
	} else if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Get after append mismatch (-want +got):\n%s", diff)
	}

	updated, ok, err := artifacts.Update(ctx, "artf-aaaaa", "Renamed", "", at(5))
	if err != nil || !ok {
		t.Fatalf("Update = %v, %v", ok, err)
	}
	want.Title, want.ProjectID, want.UpdatedAt = "Renamed", "", at(5)
	if diff := cmp.Diff(want, updated); diff != "" {
		t.Errorf("Update mismatch (-want +got):\n%s", diff)
	}
	if _, ok, err := artifacts.Update(ctx, "artf-zzzzz", "x", "", at(5)); err != nil || ok {
		t.Errorf("Update(unknown) = %v, %v; want false", ok, err)
	}
	if _, _, err := artifacts.Update(ctx, "artf-aaaaa", "x", "proj-zzzzz", at(5)); !errors.Is(err, ErrForeignKeyViolation) {
		t.Errorf("Update(unknown project) = %v, want ErrForeignKeyViolation", err)
	}

	keys, err := artifacts.VersionKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(map[string][]int{"artf-aaaaa": {1, 2}}, keys); diff != "" {
		t.Errorf("VersionKeys mismatch (-want +got):\n%s", diff)
	}

	if ok, err := artifacts.Delete(ctx, "artf-aaaaa"); err != nil || !ok {
		t.Fatalf("Delete = %v, %v", ok, err)
	}
	if ok, err := artifacts.Delete(ctx, "artf-aaaaa"); err != nil || ok {
		t.Fatalf("second Delete = %v, %v; want false", ok, err)
	}
	if versions, err := artifacts.Versions(ctx, "artf-aaaaa"); err != nil || len(versions) != 0 {
		t.Errorf("Versions after delete = %v, %v; want none (cascade)", versions, err)
	}
	// The publication ids outlive the artifact.
	if _, found, deleted, err := artifacts.Publication(ctx, "pub-1"); err != nil || found || !deleted {
		t.Errorf("Publication after delete = %v, %v, %v; want deleted", found, deleted, err)
	}
}

// Projects own no artifacts: deleting one leaves its artifacts and every
// version in place, unassigned. Listing filters by project.
func TestProjectDeleteUnassignsArtifacts(t *testing.T) {
	s, _ := openStore(t)
	ctx := context.Background()
	insertProject(t, s, "proj-aaaaa", "/a")
	insertProject(t, s, "proj-bbbbb", "/b")
	artifacts := s.Artifacts()
	for i, id := range []string{"artf-aaaaa", "artf-bbbbb"} {
		if ok, err := artifacts.Create(ctx, ArtifactRecord{
			ID: id, Title: id, ProjectID: "proj-aaaaa", CreatedAt: at(i), UpdatedAt: at(i),
		}, ArtifactVersionRecord{ArtifactID: id, Number: 1, Title: id, PublishedAt: at(i), PublicationID: "pub-" + id}); err != nil || !ok {
			t.Fatalf("Create(%s) = %v, %v", id, ok, err)
		}
	}
	if _, ok, err := artifacts.Update(ctx, "artf-bbbbb", "artf-bbbbb", "proj-bbbbb", at(2)); err != nil || !ok {
		t.Fatalf("Update = %v, %v", ok, err)
	}
	inA, err := artifacts.List(ctx, "proj-aaaaa")
	if err != nil || len(inA) != 1 || inA[0].ID != "artf-aaaaa" {
		t.Fatalf("List(proj-aaaaa) = %+v, %v; want artf-aaaaa only", inA, err)
	}
	if ok, err := s.Projects().Delete(ctx, "proj-aaaaa"); err != nil || !ok {
		t.Fatalf("Delete(project) = %v, %v", ok, err)
	}
	all, err := artifacts.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []ArtifactRecord{
		{ID: "artf-aaaaa", Title: "artf-aaaaa", CurrentVersion: 1, CreatedAt: at(0), UpdatedAt: at(0)},
		{ID: "artf-bbbbb", Title: "artf-bbbbb", ProjectID: "proj-bbbbb", CurrentVersion: 1, CreatedAt: at(1), UpdatedAt: at(2)},
	}
	if diff := cmp.Diff(want, all); diff != "" {
		t.Errorf("List after project delete mismatch (-want +got):\n%s", diff)
	}
	if versions, err := artifacts.Versions(ctx, "artf-aaaaa"); err != nil || len(versions) != 1 {
		t.Errorf("Versions after project delete = %v, %v; want the version kept", versions, err)
	}
	if none, err := artifacts.List(ctx, "proj-zzzzz"); err != nil || len(none) != 0 {
		t.Errorf("List(unknown project) = %v, %v; want empty", none, err)
	}
}
