package store

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func errorsIsForeignKey(err error) bool { return errors.Is(err, ErrForeignKeyViolation) }

func TestThreadsRoundTrip(t *testing.T) {
	s, _ := openStore(t)
	ctx := context.Background()
	threads := s.Threads()
	insertProject(t, s, "proj-aaaaa", "/home/x")
	insertSpace(t, s, "spce-aaaaa", "/home/x")
	if ok, err := s.Terminals().Insert(ctx, TerminalRecord{
		ID: "term-aaaaa", SpaceID: "spce-aaaaa", Name: "Claude Code", Directory: "/home/x",
		AppID: "claude/tui", CreatedAt: at(0), UpdatedAt: at(0),
	}); err != nil || !ok {
		t.Fatalf("planting terminal = %v, %v", ok, err)
	}

	evidence := at(3)
	records := []ThreadRecord{
		{
			ID: "thrd-aaaaa", IntegrationID: "claude", AgentID: "claude", ProjectID: "proj-aaaaa", TerminalID: new("term-aaaaa"),
			Title: "fix the build", Model: "claude-opus-5", Effort: "high",
			Cwd: "/home/x", PermissionMode: "default",
			Status: "idle", LastEvidenceAt: &evidence,
			CreatedAt: at(1), UpdatedAt: at(3),
			Turn: &TurnRecord{ID: "turn-aaaaaaaaaa", State: "running", StartedAt: at(2)},
		},
		{
			ID: "thrd-bbbbb", IntegrationID: "codex", AgentID: "codex", ProjectID: "proj-aaaaa",
			Status: "unknown", CreatedAt: at(2), UpdatedAt: at(2),
		},
	}
	for _, record := range records {
		if ok, err := threads.InsertObserved(ctx, record, ThreadIdentity{
			IntegrationID: record.IntegrationID, ProviderConversationID: "sess-" + record.ID, ThreadID: record.ID,
		}); err != nil || !ok {
			t.Fatalf("InsertObserved = %v, %v; want true", ok, err)
		}
	}

	got, err := threads.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(records, got); diff != "" {
		t.Errorf("List() mismatch (-want +got):\n%s", diff)
	}

	// Insertion is the ID collision check.
	if ok, err := threads.InsertObserved(ctx, ThreadRecord{
		ID: "thrd-aaaaa", IntegrationID: "claude", AgentID: "claude", ProjectID: "proj-aaaaa", Status: "unknown",
		CreatedAt: at(9), UpdatedAt: at(9),
	}, ThreadIdentity{IntegrationID: "claude", ProviderConversationID: "sess-x", ThreadID: "thrd-aaaaa"}); err != nil || ok {
		t.Fatalf("InsertObserved(collision) = %v, %v; want false", ok, err)
	}

	// A thread referencing a missing project or terminal is refused by the
	// schema, surfaced as the typed foreign-key error.
	if _, err := threads.InsertObserved(ctx, ThreadRecord{
		ID: "thrd-ccccc", IntegrationID: "claude", AgentID: "claude", ProjectID: "proj-nope", Status: "unknown",
		CreatedAt: at(4), UpdatedAt: at(4),
	}, ThreadIdentity{IntegrationID: "claude", ProviderConversationID: "sess-c", ThreadID: "thrd-ccccc"}); !errorsIsForeignKey(err) {
		t.Errorf("InsertObserved(missing project) error = %v; want ErrForeignKeyViolation", err)
	}
	if _, err := threads.InsertObserved(ctx, ThreadRecord{
		ID: "thrd-ddddd", IntegrationID: "claude", AgentID: "claude", ProjectID: "proj-aaaaa", TerminalID: new("term-nope"),
		Status: "unknown", CreatedAt: at(4), UpdatedAt: at(4),
	}, ThreadIdentity{IntegrationID: "claude", ProviderConversationID: "sess-d", ThreadID: "thrd-ddddd"}); !errorsIsForeignKey(err) {
		t.Errorf("InsertObserved(missing terminal) error = %v; want ErrForeignKeyViolation", err)
	}

	// Update writes every mutable column.
	archived := at(5)
	updated := records[0]
	updated.TerminalID = nil
	updated.Title = "renamed"
	updated.TitleUserSet = true
	updated.Status = "error"
	updated.StatusDetail = "session faulted"
	completed := at(5)
	updated.Turn = &TurnRecord{ID: "turn-bbbbbbbbbb", ProviderID: "t3-turn-1", State: "failed", StartedAt: at(4), CompletedAt: &completed, Error: "boom"}
	updated.Archived = true
	updated.ArchivedAt = &archived
	updated.UpdatedAt = at(6)
	if ok, err := threads.Update(ctx, updated); err != nil || !ok {
		t.Fatalf("Update = %v, %v; want true", ok, err)
	}
	if ok, err := threads.Update(ctx, ThreadRecord{ID: "thrd-zzzzz", Status: "idle"}); err != nil || ok {
		t.Fatalf("Update(absent) = %v, %v; want false", ok, err)
	}
	got, err = threads.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]ThreadRecord{updated, records[1]}, got); diff != "" {
		t.Errorf("after update (-want +got):\n%s", diff)
	}

	if ok, err := threads.Delete(ctx, "thrd-bbbbb"); err != nil || !ok {
		t.Fatalf("Delete = %v, %v; want true", ok, err)
	}
	if ok, err := threads.Delete(ctx, "thrd-bbbbb"); err != nil || ok {
		t.Fatalf("second Delete = %v, %v; want false", ok, err)
	}
}

// InsertObserved commits the record and its mapping together: a thread
// must never exist unmapped, or the next observation would duplicate it.
func TestInsertObservedIsAtomic(t *testing.T) {
	s, _ := openStore(t)
	ctx := context.Background()
	threads := s.Threads()
	insertProject(t, s, "proj-aaaaa", "/")

	record := ThreadRecord{
		ID: "thrd-aaaaa", IntegrationID: "claude", AgentID: "claude", ProjectID: "proj-aaaaa", Status: "idle",
		CreatedAt: at(0), UpdatedAt: at(0),
	}
	identity := ThreadIdentity{IntegrationID: "claude", ProviderConversationID: "sess-1", ThreadID: "thrd-aaaaa"}
	if ok, err := threads.InsertObserved(ctx, record, identity); err != nil || !ok {
		t.Fatalf("InsertObserved = %v, %v; want true", ok, err)
	}

	// An ID collision inserts neither half.
	collision := record
	collision.IntegrationID = "codex"
	if ok, err := threads.InsertObserved(ctx, collision, ThreadIdentity{
		IntegrationID: "codex", ProviderConversationID: "sess-2", ThreadID: "thrd-aaaaa",
	}); err != nil || ok {
		t.Fatalf("InsertObserved(collision) = %v, %v; want false", ok, err)
	}

	// A failing identity write rolls the record back too.
	fresh := record
	fresh.ID = "thrd-bbbbb"
	if ok, err := threads.InsertObserved(ctx, fresh, identity); err == nil || ok {
		t.Fatalf("InsertObserved(mapped identity) = %v, %v; want an error", ok, err)
	}
	records, err := threads.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	identities, err := threads.ListIdentities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || len(identities) != 1 {
		t.Errorf("after refused inserts: %d records, %d identities; want 1 and 1", len(records), len(identities))
	}
}

func TestThreadIdentities(t *testing.T) {
	s, _ := openStore(t)
	ctx := context.Background()
	threads := s.Threads()
	insertProject(t, s, "proj-aaaaa", "/")

	// The identity key is (integration, provider conversation id): the same
	// provider id under another integration is a distinct identity.
	identities := []ThreadIdentity{
		{IntegrationID: "claude", ProviderConversationID: "sess-1", ThreadID: "thrd-aaaaa"},
		{IntegrationID: "codex", ProviderConversationID: "sess-1", ThreadID: "thrd-bbbbb"},
	}
	for _, identity := range identities {
		if ok, err := threads.InsertObserved(ctx, ThreadRecord{
			ID: identity.ThreadID, IntegrationID: identity.IntegrationID, AgentID: identity.IntegrationID, ProjectID: "proj-aaaaa", Status: "idle",
			CreatedAt: at(0), UpdatedAt: at(0),
		}, identity); err != nil || !ok {
			t.Fatalf("InsertObserved = %v, %v", ok, err)
		}
	}

	got, err := threads.ListIdentities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(identities, got); diff != "" {
		t.Errorf("ListIdentities (-want +got):\n%s", diff)
	}

	// Deleting a thread cascades only its own mapping.
	if ok, err := threads.Delete(ctx, "thrd-aaaaa"); err != nil || !ok {
		t.Fatalf("Delete = %v, %v", ok, err)
	}
	got, err = threads.ListIdentities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(identities[1:], got); diff != "" {
		t.Errorf("identities after thread delete (-want +got):\n%s", diff)
	}
}

// The referential lifecycle lives in the schema: terminal deletion clears
// thread linkage, project deletion clears the association — the thread
// and its identity mapping survive unassigned.
func TestThreadReferentialLifecycle(t *testing.T) {
	s, _ := openStore(t)
	ctx := context.Background()
	threads := s.Threads()
	insertProject(t, s, "proj-aaaaa", "/")
	insertSpace(t, s, "spce-aaaaa", "/")
	if ok, err := s.Terminals().Insert(ctx, TerminalRecord{
		ID: "term-aaaaa", SpaceID: "spce-aaaaa", Name: "Claude Code", Directory: "/",
		CreatedAt: at(0), UpdatedAt: at(0),
	}); err != nil || !ok {
		t.Fatalf("planting terminal = %v, %v", ok, err)
	}
	if ok, err := threads.InsertObserved(ctx, ThreadRecord{
		ID: "thrd-aaaaa", IntegrationID: "claude", AgentID: "claude", ProjectID: "proj-aaaaa", TerminalID: new("term-aaaaa"),
		Status: "idle", CreatedAt: at(1), UpdatedAt: at(1),
	}, ThreadIdentity{
		IntegrationID: "claude", ProviderConversationID: "sess-1", ThreadID: "thrd-aaaaa",
	}); err != nil || !ok {
		t.Fatalf("InsertObserved = %v, %v", ok, err)
	}

	// ON DELETE SET NULL: the thread record survives its terminal.
	if ok, err := s.Terminals().Delete(ctx, "term-aaaaa"); err != nil || !ok {
		t.Fatalf("terminal Delete = %v, %v", ok, err)
	}
	records, err := threads.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].TerminalID != nil {
		t.Fatalf("after terminal delete: %+v; want one record with nil TerminalID", records)
	}

	// ON DELETE SET NULL: the project leaves its threads unassigned.
	if ok, err := s.Projects().Delete(ctx, "proj-aaaaa"); err != nil || !ok {
		t.Fatalf("project Delete = %v, %v", ok, err)
	}
	records, err = threads.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	identities, err := threads.ListIdentities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ProjectID != "" || len(identities) != 1 {
		t.Errorf("after project delete: %+v, %d identities; want one unassigned record and its mapping", records, len(identities))
	}
}
