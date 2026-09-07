package store

import (
	"context"
	"errors"
	"fmt"
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
	updated.Turn = &TurnRecord{ID: "turn-bbbbbbbbbb", ProviderID: "t3-turn-1", State: "failed", StartedAt: at(4), CompletedAt: &completed, Error: "boom", Response: "I could not finish: **boom**."}
	updated.Pending = &PendingTurnRecord{ID: "turn-cccccccccc", Prior: "", SubmittedAt: at(5)}
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

// Messages (ATC-307): the submission transaction, keyed lookup, delivery
// updates, the per-thread prune that spares uncertain rows, and the key
// uniqueness the domain's race guard relies on. Unkeyed messages never
// collide, and a thread's rows go with it.
func TestThreadMessages(t *testing.T) {
	s, _ := openStore(t)
	ctx := context.Background()
	threads := s.Threads()
	insertProject(t, s, "proj-aaaaa", "/")
	records := map[string]ThreadRecord{}
	for _, id := range []string{"thrd-aaaaa", "thrd-bbbbb"} {
		records[id] = ThreadRecord{ID: id, IntegrationID: "t3code", ProjectID: "proj-aaaaa", Status: "idle", CreatedAt: at(0), UpdatedAt: at(0)}
		if ok, err := threads.InsertObserved(ctx, records[id], ThreadIdentity{IntegrationID: "t3code", ProviderConversationID: "t3-" + id, ThreadID: id}); err != nil || !ok {
			t.Fatalf("planting %s = %v, %v", id, ok, err)
		}
	}
	submit := func(record ThreadRecord, message ThreadMessageRecord, keep int) (bool, error) {
		t.Helper()
		return threads.SubmitMessage(ctx, record, message, keep)
	}
	pending := records["thrd-aaaaa"]
	pending.Pending = &PendingTurnRecord{ID: "turn-aaaaaaaaaa", SubmittedAt: at(1)}
	pending.Status, pending.UpdatedAt = "working", at(1)
	keyed := ThreadMessageRecord{ID: "msg-aaaaaaaaaa", ThreadID: "thrd-aaaaa", Key: "k1", Text: "continue", TurnID: "turn-aaaaaaaaaa", Delivery: "uncertain", CreatedAt: at(1), UpdatedAt: at(1)}
	if ok, err := submit(pending, keyed, 32); err != nil || !ok {
		t.Fatalf("SubmitMessage = %v, %v", ok, err)
	}
	// The thread record went with the message.
	if got, err := threads.List(ctx); err != nil || len(got) != 2 {
		t.Fatal(err)
	} else if diff := cmp.Diff(pending, got[0]); diff != "" {
		t.Errorf("thread after submit (-want +got):\n%s", diff)
	}
	if ok, err := submit(pending, keyed, 32); err != nil || ok {
		t.Fatalf("SubmitMessage(id collision) = %v, %v; want false", ok, err)
	}
	if _, err := submit(pending, ThreadMessageRecord{ID: "msg-bbbbbbbbbb", ThreadID: "thrd-aaaaa", Key: "k1", Text: "again", TurnID: "turn-x", Delivery: "accepted", CreatedAt: at(2), UpdatedAt: at(2)}, 32); !errors.Is(err, ErrMessageKeyTaken) {
		t.Errorf("SubmitMessage(same key) = %v; want ErrMessageKeyTaken", err)
	}
	// A vanished thread inserts nothing.
	if ok, err := submit(ThreadRecord{ID: "thrd-zzzzz", Status: "idle"}, ThreadMessageRecord{ID: "msg-zzzzzzzzzz", ThreadID: "thrd-zzzzz", Text: "x", TurnID: "t", Delivery: "accepted", CreatedAt: at(2), UpdatedAt: at(2)}, 32); err != nil || ok {
		t.Errorf("SubmitMessage(absent thread) = %v, %v; want false", ok, err)
	}
	// The same key on another thread is another message; unkeyed rows
	// never collide.
	for i, record := range []ThreadMessageRecord{
		{ID: "msg-bbbbbbbbbb", ThreadID: "thrd-bbbbb", Key: "k1", Text: "other", TurnID: "turn-b", Delivery: "accepted", CreatedAt: at(2), UpdatedAt: at(2)},
		{ID: "msg-cccccccccc", ThreadID: "thrd-aaaaa", Text: "unkeyed", TurnID: "turn-c", Delivery: "accepted", CreatedAt: at(3), UpdatedAt: at(3)},
		{ID: "msg-dddddddddd", ThreadID: "thrd-aaaaa", Text: "unkeyed too", TurnID: "turn-d", Delivery: "rejected", Detail: "no", CreatedAt: at(4), UpdatedAt: at(4)},
	} {
		if ok, err := submit(records[record.ThreadID], record, 32); err != nil || !ok {
			t.Fatalf("SubmitMessage(%d) = %v, %v", i, ok, err)
		}
	}

	got, err := threads.MessageByKey(ctx, "thrd-aaaaa", "k1")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(keyed, got); diff != "" {
		t.Errorf("MessageByKey (-want +got):\n%s", diff)
	}
	if _, err := threads.MessageByKey(ctx, "thrd-aaaaa", "k2"); !errors.Is(err, ErrMessageNotFound) {
		t.Errorf("MessageByKey(unknown) = %v", err)
	}
	if _, err := threads.MessageByKey(ctx, "thrd-aaaaa", ""); !errors.Is(err, ErrMessageNotFound) {
		t.Errorf("MessageByKey(empty key) = %v; want not found, never an unkeyed row", err)
	}
	if ok, err := threads.SetMessageDelivery(ctx, "msg-zzzzzzzzzz", "accepted", "", at(5)); err != nil || ok {
		t.Fatalf("SetMessageDelivery(absent) = %v, %v; want false", ok, err)
	}

	// The prune keeps the newest rows of the named thread, and every row
	// whose delivery is still uncertain — the receipt a retry needs.
	if ok, err := submit(records["thrd-aaaaa"], ThreadMessageRecord{ID: "msg-eeeeeeeeee", ThreadID: "thrd-aaaaa", Text: "newest", TurnID: "turn-e", Delivery: "accepted", CreatedAt: at(6), UpdatedAt: at(6)}, 2); err != nil || !ok {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{keyed.ID: true, "msg-cccccccccc": false, "msg-dddddddddd": true, "msg-eeeeeeeeee": true, "msg-bbbbbbbbbb": true} {
		_, err := threads.Message(ctx, id)
		if kept := err == nil; kept != want {
			t.Errorf("message %s after prune: kept %t, want %t (%v)", id, kept, want, err)
		}
	}
	if ok, err := threads.SetMessageDelivery(ctx, keyed.ID, "rejected", "T3 said no", at(7)); err != nil || !ok {
		t.Fatalf("SetMessageDelivery = %v, %v", ok, err)
	}
	keyed.Delivery, keyed.Detail, keyed.UpdatedAt = "rejected", "T3 said no", at(7)
	if got, err := threads.Message(ctx, keyed.ID); err != nil {
		t.Fatal(err)
	} else if diff := cmp.Diff(keyed, got); diff != "" {
		t.Errorf("Message (-want +got):\n%s", diff)
	}
	if ok, err := submit(records["thrd-aaaaa"], ThreadMessageRecord{ID: "msg-ffffffffff", ThreadID: "thrd-aaaaa", Text: "newer", TurnID: "turn-f", Delivery: "accepted", CreatedAt: at(8), UpdatedAt: at(8)}, 2); err != nil || !ok {
		t.Fatal(err)
	}
	if _, err := threads.Message(ctx, keyed.ID); !errors.Is(err, ErrMessageNotFound) {
		t.Errorf("rejected message past the bound = %v; want gone", err)
	}
	// Deleting the thread deletes its messages.
	if ok, err := threads.Delete(ctx, "thrd-aaaaa"); err != nil || !ok {
		t.Fatal(err)
	}
	if _, err := threads.Message(ctx, "msg-eeeeeeeeee"); !errors.Is(err, ErrMessageNotFound) {
		t.Errorf("message after thread delete = %v; want gone", err)
	}
}

// Answers and stops (ATC-308): the submission transactions, the state
// updates, the boot-time lists, the prunes that spare unresolved rows,
// and a stop's resolution committing the record, the stop, and the
// withdrawal of the covered turn's uncertain messages together.
func TestThreadAnswersAndStops(t *testing.T) {
	s, _ := openStore(t)
	ctx := context.Background()
	threads := s.Threads()
	insertProject(t, s, "proj-aaaaa", "/")
	record := ThreadRecord{ID: "thrd-aaaaa", IntegrationID: "t3code", ProjectID: "proj-aaaaa", Status: "working", CreatedAt: at(0), UpdatedAt: at(0),
		Pending: &PendingTurnRecord{ID: "turn-pending00", SubmittedAt: at(1)}}
	if ok, err := threads.InsertObserved(ctx, record, ThreadIdentity{IntegrationID: "t3code", ProviderConversationID: "t1", ThreadID: record.ID}); err != nil || !ok {
		t.Fatalf("planting = %v, %v", ok, err)
	}
	answer := ThreadAnswerRecord{ID: "ans-aaaaaaaaaa", ThreadID: record.ID, RequestID: "inpt-aaaaaaaaaa", ProviderRequestID: "req-1",
		Questions: `[{"id":"q1"}]`, Answers: `[{"questionId":"q1","choices":["a"]}]`, ProviderAnswers: `[{"questionId":"Q","values":["a"]}]`,
		Delivery: "uncertain", State: "sent", CreatedAt: at(2), UpdatedAt: at(2)}
	if ok, err := threads.SubmitAnswer(ctx, answer, 16); err != nil || !ok {
		t.Fatalf("SubmitAnswer = %v, %v", ok, err)
	}
	if ok, err := threads.SubmitAnswer(ctx, answer, 16); err != nil || ok {
		t.Errorf("SubmitAnswer(id collision) = %v, %v; want false", ok, err)
	}
	if _, err := threads.SubmitAnswer(ctx, ThreadAnswerRecord{ID: "ans-zzzzzzzzzz", ThreadID: "thrd-zzzzz", RequestID: "r", ProviderRequestID: "r", Questions: "[]", Answers: "[]", ProviderAnswers: "[]", Delivery: "uncertain", State: "sent", CreatedAt: at(2), UpdatedAt: at(2)}, 16); !errorsIsForeignKey(err) {
		t.Errorf("SubmitAnswer(absent thread) = %v; want a foreign-key violation", err)
	}
	// Settled answers past keep are pruned, unresolved ones never.
	for i, state := range []string{"resolved", "failed", "sent"} {
		if ok, err := threads.SubmitAnswer(ctx, ThreadAnswerRecord{ID: fmt.Sprintf("ans-%010d", i), ThreadID: record.ID, RequestID: "inpt-b", ProviderRequestID: "req-2",
			Questions: "[]", Answers: "[]", ProviderAnswers: "[]", Delivery: "accepted", State: state, CreatedAt: at(3 + i), UpdatedAt: at(3 + i)}, 2); err != nil || !ok {
			t.Fatalf("SubmitAnswer(%d) = %v, %v", i, ok, err)
		}
	}
	answers, err := threads.ListAnswers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, a := range answers {
		ids = append(ids, a.ID)
	}
	if diff := cmp.Diff([]string{answer.ID, "ans-0000000001", "ans-0000000002"}, ids); diff != "" {
		t.Errorf("answers after prune (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(answer, answers[0]); diff != "" {
		t.Errorf("answer (-want +got):\n%s", diff)
	}
	answer.Delivery, answer.State, answer.Detail, answer.UpdatedAt = "accepted", "failed", "no session", at(9)
	if ok, err := threads.UpdateAnswer(ctx, answer); err != nil || !ok {
		t.Fatalf("UpdateAnswer = %v, %v", ok, err)
	}
	if ok, err := threads.UpdateAnswer(ctx, ThreadAnswerRecord{ID: "ans-zzzzzzzzzz"}); err != nil || ok {
		t.Errorf("UpdateAnswer(absent) = %v, %v; want false", ok, err)
	}
	if answers, _ = threads.ListAnswers(ctx); answers[0].State != "failed" || answers[0].Detail != "no session" || !answers[0].UpdatedAt.Equal(at(9)) {
		t.Errorf("updated answer = %+v", answers[0])
	}

	stop := ThreadStopRecord{ID: "stop-aaaaaaaaaa", ThreadID: record.ID, State: "stopping", Delivery: "uncertain", ScopeTurn: "turn-running000", ScopePending: "turn-pending00", ScopeStatus: "working", CreatedAt: at(10), UpdatedAt: at(10)}
	if ok, err := threads.SubmitStop(ctx, stop, 8); err != nil || !ok {
		t.Fatalf("SubmitStop = %v, %v", ok, err)
	}
	if ok, err := threads.SubmitStop(ctx, stop, 8); err != nil || ok {
		t.Errorf("SubmitStop(id collision) = %v, %v; want false", ok, err)
	}
	stop.Delivery, stop.UpdatedAt = "accepted", at(11)
	if ok, err := threads.UpdateStop(ctx, stop); err != nil || !ok {
		t.Fatalf("UpdateStop = %v, %v", ok, err)
	}
	stops, err := threads.ListStops(ctx)
	if err != nil || len(stops) != 1 {
		t.Fatalf("ListStops = %+v, %v", stops, err)
	}
	if diff := cmp.Diff(stop, stops[0]); diff != "" {
		t.Errorf("stop (-want +got):\n%s", diff)
	}
	// The resolution: the record's pending turn becomes its latest, the
	// stop resolves, the uncertain messages on the covered turns are
	// withdrawn and the others untouched.
	for _, message := range []ThreadMessageRecord{
		{ID: "msg-aaaaaaaaaa", ThreadID: record.ID, Key: "k1", Text: "pending", TurnID: "turn-pending00", Delivery: "uncertain", CreatedAt: at(1), UpdatedAt: at(1)},
		{ID: "msg-bbbbbbbbbb", ThreadID: record.ID, Key: "k2", Text: "steer", TurnID: "turn-running000", Delivery: "accepted", CreatedAt: at(1), UpdatedAt: at(1)},
		{ID: "msg-cccccccccc", ThreadID: record.ID, Key: "k3", Text: "other", TurnID: "turn-other00000", Delivery: "uncertain", CreatedAt: at(1), UpdatedAt: at(1)},
	} {
		if ok, err := threads.SubmitMessage(ctx, record, message, 32); err != nil || !ok {
			t.Fatalf("SubmitMessage(%s) = %v, %v", message.ID, ok, err)
		}
	}
	completed := at(12)
	record.Pending, record.Status, record.UpdatedAt = nil, "idle", at(12)
	record.Turn = &TurnRecord{ID: "turn-pending00", State: "interrupted", StartedAt: at(1), CompletedAt: &completed}
	stop.State, stop.Detail, stop.UpdatedAt, stop.ResolvedAt = "stopped", "closed", at(12), &completed
	if ok, err := threads.ResolveStop(ctx, record, stop, []string{"turn-running000", "turn-pending00"}, "withdrawn by the stop", "stopped"); err != nil || !ok {
		t.Fatalf("ResolveStop = %v, %v", ok, err)
	}
	// The answer still sent is superseded with the resolution; the
	// settled ones are untouched.
	if answers, _ = threads.ListAnswers(ctx); answers[2].State != "superseded" || answers[2].Detail != "stopped" || answers[0].State != "failed" {
		t.Errorf("answers after resolution = %+v", answers)
	}
	if got, _ := threads.List(ctx); len(got) != 1 {
		t.Fatal("thread gone")
	} else if diff := cmp.Diff(record, got[0]); diff != "" {
		t.Errorf("thread after resolution (-want +got):\n%s", diff)
	}
	if stops, _ = threads.ListStops(ctx); stops[0].State != "stopped" || stops[0].ResolvedAt == nil {
		t.Errorf("stop after resolution = %+v", stops[0])
	}
	for id, want := range map[string]string{"msg-aaaaaaaaaa": "withdrawn", "msg-bbbbbbbbbb": "accepted", "msg-cccccccccc": "uncertain"} {
		message, err := threads.Message(ctx, id)
		if err != nil || message.Delivery != want {
			t.Errorf("message %s after resolution = %s (%v); want %s", id, message.Delivery, err, want)
		}
		if want == "withdrawn" && (message.Detail != "withdrawn by the stop" || !message.UpdatedAt.Equal(at(12))) {
			t.Errorf("withdrawn message = %+v", message)
		}
	}
	if ok, err := threads.ResolveStop(ctx, record, ThreadStopRecord{ID: "stop-zzzzzzzzzz", ThreadID: record.ID, State: "failed", Delivery: "accepted", ScopeStatus: "idle", UpdatedAt: at(13)}, nil, "", ""); err != nil || ok {
		t.Errorf("ResolveStop(absent stop) = %v, %v; want false", ok, err)
	}
	// Resolved stops past keep are pruned; a stopping one is spared.
	for i, state := range []string{"stopping", "failed", "finished"} {
		if ok, err := threads.SubmitStop(ctx, ThreadStopRecord{ID: fmt.Sprintf("stop-%010d", i), ThreadID: record.ID, State: state, Delivery: "accepted", ScopeStatus: "idle", CreatedAt: at(20 + i), UpdatedAt: at(20 + i)}, 2); err != nil || !ok {
			t.Fatalf("SubmitStop(%d) = %v, %v", i, ok, err)
		}
	}
	stops, _ = threads.ListStops(ctx)
	ids = ids[:0]
	for _, s := range stops {
		ids = append(ids, s.ID)
	}
	if diff := cmp.Diff([]string{"stop-0000000000", "stop-0000000001", "stop-0000000002"}, ids); diff != "" {
		t.Errorf("stops after prune (-want +got):\n%s", diff)
	}
	// A key is unique per thread; unkeyed stops never collide.
	keyed := ThreadStopRecord{ID: "stop-kkkkkkkkkk", ThreadID: record.ID, Key: "k1", State: "finished", Delivery: "accepted", ScopeStatus: "idle", CreatedAt: at(30), UpdatedAt: at(30)}
	if ok, err := threads.SubmitStop(ctx, keyed, 8); err != nil || !ok {
		t.Fatalf("SubmitStop(keyed) = %v, %v", ok, err)
	}
	keyed.ID = "stop-kkkkkkkkk2"
	if _, err := threads.SubmitStop(ctx, keyed, 8); !errors.Is(err, ErrStopKeyTaken) {
		t.Errorf("SubmitStop(same key) = %v; want ErrStopKeyTaken", err)
	}
	if ok, err := threads.SubmitStop(ctx, ThreadStopRecord{ID: "stop-uuuuuuuuuu", ThreadID: record.ID, State: "finished", Delivery: "accepted", ScopeStatus: "idle", CreatedAt: at(30), UpdatedAt: at(30)}, 8); err != nil || !ok {
		t.Errorf("SubmitStop(unkeyed again) = %v, %v", ok, err)
	}
	// A thread's rows go with it.
	if ok, err := threads.Delete(ctx, record.ID); err != nil || !ok {
		t.Fatal(err)
	}
	if answers, _ = threads.ListAnswers(ctx); len(answers) != 0 {
		t.Errorf("answers after delete = %+v", answers)
	}
	if stops, _ = threads.ListStops(ctx); len(stops) != 0 {
		t.Errorf("stops after delete = %+v", stops)
	}
}
