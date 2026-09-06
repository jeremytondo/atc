package threads

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
)

var approvalIDPattern = regexp.MustCompile(`^aprv-[23456789bcdfghjkmnpqrstvwxyz]{10}$`)

var approveOrDeny = []api.ApprovalOption{{Decision: api.DecisionApprove, Label: "Approve"}, {Decision: api.DecisionDeny, Label: "Decline"}}

// Pending approval requests (ATC-307) ride the thread with stable,
// derived ids: a report adds and updates them and publishes
// thread.updated whether or not the status changed; a request no longer
// reported is resolved elsewhere and stays resolved whatever a later
// report says; an unmapped identity is dropped; and T3 dropping the
// thread clears them.
func TestObserveApprovals(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa")
	ctx := context.Background()
	id, err := f.service.ObserveExternal(ctx, ExternalObservation{
		IntegrationID: "t3code", ProviderID: "t1", InitialDirectory: f.dir("proj-aaaaa"), Status: api.ThreadWaitingForPermission, Title: "T",
	})
	if err != nil {
		t.Fatal(err)
	}
	f.drain()
	requested := time.Date(2026, 9, 1, 0, 0, 5, 0, time.UTC)
	first := ApprovalObservation{RequestID: "req-1", Kind: api.ApprovalCommand, Summary: "Command approval requested", Detail: "rm -rf build", Options: approveOrDeny, RequestedAt: requested}
	if err := f.service.ObserveApprovals(ctx, "t3code", "t1", []ApprovalObservation{first}); err != nil {
		t.Fatal(err)
	}
	thread, _ := f.service.Get(id)
	if len(thread.Approvals) != 1 || !approvalIDPattern.MatchString(thread.Approvals[0].ID) {
		t.Fatalf("approvals = %+v", thread.Approvals)
	}
	permID := thread.Approvals[0].ID
	want := api.ThreadApproval{ID: permID, ThreadID: id, Status: api.ApprovalPending, Kind: api.ApprovalCommand, Summary: "Command approval requested", Detail: "rm -rf build", Options: approveOrDeny, RequestedAt: requested}
	if diff := cmp.Diff(want, thread.Approvals[0]); diff != "" {
		t.Errorf("approval (-want +got):\n%s", diff)
	}
	if got := f.drain(); !slices.Equal(got, []string{"thread.updated " + id}) {
		t.Errorf("events on a new request = %v", got)
	}
	// The same report again changes nothing; a second request joins.
	if err := f.service.ObserveApprovals(ctx, "t3code", "t1", []ApprovalObservation{first}); err != nil {
		t.Fatal(err)
	}
	if got := f.drain(); len(got) != 0 {
		t.Errorf("events on an unchanged report = %v", got)
	}
	second := ApprovalObservation{RequestID: "req-2", Kind: api.ApprovalFileChange, Summary: "File-change approval requested", Detail: "main.go", Options: approveOrDeny, RequestedAt: requested.Add(time.Second)}
	if err := f.service.ObserveApprovals(ctx, "t3code", "t1", []ApprovalObservation{first, second}); err != nil {
		t.Fatal(err)
	}
	thread, _ = f.service.Get(id)
	if len(thread.Approvals) != 2 || thread.Approvals[0].ID != permID || thread.Approvals[1].Kind != api.ApprovalFileChange {
		t.Errorf("approvals = %+v", thread.Approvals)
	}
	if got := f.drain(); !slices.Equal(got, []string{"thread.updated " + id}) {
		t.Errorf("events on a second request = %v", got)
	}
	// The first is resolved elsewhere: gone from the thread, remembered
	// as resolved with no decision, and not revived by a stale report.
	if err := f.service.ObserveApprovals(ctx, "t3code", "t1", []ApprovalObservation{second}); err != nil {
		t.Fatal(err)
	}
	thread, _ = f.service.Get(id)
	if len(thread.Approvals) != 1 || thread.Approvals[0].Kind != api.ApprovalFileChange {
		t.Errorf("approvals after resolution elsewhere = %+v", thread.Approvals)
	}
	if _, err := f.service.BeginDecision(id, permID, api.DecisionApprove); !errors.Is(err, ErrApprovalResolved) || !strings.Contains(err.Error(), "elsewhere") {
		t.Errorf("decision on a request resolved elsewhere = %v", err)
	}
	if err := f.service.ObserveApprovals(ctx, "t3code", "t1", []ApprovalObservation{first, second}); err != nil {
		t.Fatal(err)
	}
	if thread, _ = f.service.Get(id); len(thread.Approvals) != 1 {
		t.Errorf("stale report revived a resolved request: %+v", thread.Approvals)
	}
	f.drain()
	// A pending request's presentation can change (options arrive late).
	richer := second
	richer.Options = append(slices.Clone(approveOrDeny), api.ApprovalOption{Decision: api.DecisionApproveAlways, Label: "Always", Warning: "careful"})
	if err := f.service.ObserveApprovals(ctx, "t3code", "t1", []ApprovalObservation{richer}); err != nil {
		t.Fatal(err)
	}
	if thread, _ = f.service.Get(id); len(thread.Approvals[0].Options) != 3 || thread.Approvals[0].Options[2].Warning != "careful" {
		t.Errorf("updated presentation = %+v", thread.Approvals[0])
	}
	if got := f.drain(); !slices.Equal(got, []string{"thread.updated " + id}) {
		t.Errorf("events on a presentation change = %v", got)
	}
	if _, err := f.service.BeginDecision(id, "aprv-nope", api.DecisionApprove); !errors.Is(err, ErrApprovalNotFound) {
		t.Errorf("unknown approval = %v", err)
	}
	if _, err := f.service.BeginDecision("thrd-nope", permID, api.DecisionApprove); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown thread = %v", err)
	}
	// Ids derive from the identity: the same request on another thread
	// has another id, and an unmapped identity reports nothing.
	if err := f.service.ObserveApprovals(ctx, "t3code", "t-unknown", []ApprovalObservation{first}); err != nil {
		t.Errorf("unmapped identity = %v", err)
	}
	if got := f.drain(); len(got) != 0 {
		t.Errorf("events for an unmapped identity = %v", got)
	}

	// T3 drops the thread: nothing pends on it any more.
	if err := f.service.ArchiveExternalThread(ctx, "t3code", "t1"); err != nil {
		t.Fatal(err)
	}
	if thread, _ = f.service.Get(id); len(thread.Approvals) != 0 || !thread.Archived {
		t.Errorf("after T3 dropped the thread = %+v", thread)
	}
}

// Deciding a request (ATC-307): the decision must target a pending
// request and an offered decision; a decision the provider committed
// resolves the request with it; a refused one leaves the request open
// to another; an unanswered one blocks a different decision but takes
// the same one again with the same key.
func TestDecideApproval(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa")
	ctx := context.Background()
	id, err := f.service.ObserveExternal(ctx, ExternalObservation{
		IntegrationID: "t3code", ProviderID: "t1", InitialDirectory: f.dir("proj-aaaaa"), Status: api.ThreadWaitingForPermission, Title: "T",
	})
	if err != nil {
		t.Fatal(err)
	}
	requests := []ApprovalObservation{
		{RequestID: "req-1", Kind: api.ApprovalCommand, Summary: "Command", Options: approveOrDeny},
		{RequestID: "req-2", Kind: api.ApprovalCommand, Summary: "Command", Options: approveOrDeny},
	}
	if err := f.service.ObserveApprovals(ctx, "t3code", "t1", requests); err != nil {
		t.Fatal(err)
	}
	thread, _ := f.service.Get(id)
	one, two := thread.Approvals[0].ID, thread.Approvals[1].ID
	f.drain()

	if _, err := f.service.BeginDecision(id, "aprv-nope", api.DecisionApprove); !errors.Is(err, ErrApprovalNotFound) {
		t.Errorf("unknown request = %v", err)
	}
	if _, err := f.service.BeginDecision(id, one, api.DecisionApproveAlways); !errors.Is(err, ErrDecisionNotOffered) {
		t.Errorf("decision not offered = %v", err)
	}
	req, err := f.service.BeginDecision(id, one, api.DecisionApprove)
	if err != nil {
		t.Fatal(err)
	}
	want := DecisionRequest{ProviderID: "t1", RequestID: "req-1", Decision: api.DecisionApprove, Key: one + "/approve"}
	if req != want {
		t.Errorf("BeginDecision = %+v; want %+v", req, want)
	}
	// Unanswered: a different decision is refused, the same one begun
	// again presents the same key.
	if _, err := f.service.BeginDecision(id, one, api.DecisionDeny); !errors.Is(err, ErrDecisionPending) {
		t.Errorf("different decision while unanswered = %v; want ErrDecisionPending", err)
	}
	if again, err := f.service.BeginDecision(id, one, api.DecisionApprove); err != nil || again != want {
		t.Errorf("same decision again = %+v, %v", again, err)
	}
	// A retry of the sent decision is taken even after the offered options
	// changed underneath it.
	if err := f.service.ObserveApprovals(ctx, "t3code", "t1", []ApprovalObservation{
		{RequestID: "req-1", Kind: api.ApprovalCommand, Summary: "Command", Options: approveOrDeny[1:]}, requests[1],
	}); err != nil {
		t.Fatal(err)
	}
	if again, err := f.service.BeginDecision(id, one, api.DecisionApprove); err != nil || again != want {
		t.Errorf("retry after the options changed = %+v, %v", again, err)
	}
	f.drain()
	// Refused: open again to any decision.
	f.service.AbandonDecision(id, one)
	if _, err := f.service.BeginDecision(id, one, api.DecisionDeny); err != nil {
		t.Errorf("decision after an abandoned one = %v", err)
	}
	// Committed: resolved with the decision, published, and final.
	approval, err := f.service.ResolveDecision(id, one)
	if err != nil || approval.Status != api.ApprovalResolved || approval.Decision != api.DecisionDeny || approval.ResolvedAt == nil {
		t.Fatalf("ResolveDecision = %+v, %v", approval, err)
	}
	if got := f.drain(); !slices.Equal(got, []string{"thread.updated " + id}) {
		t.Errorf("events on resolution = %v", got)
	}
	if thread, _ = f.service.Get(id); len(thread.Approvals) != 1 || thread.Approvals[0].ID != two {
		t.Errorf("pending after a decision = %+v", thread.Approvals)
	}
	_, err = f.service.BeginDecision(id, one, api.DecisionApprove)
	if !errors.Is(err, ErrApprovalResolved) || !strings.Contains(err.Error(), "deny") {
		t.Errorf("decision on a resolved request = %v; want ErrApprovalResolved naming the decision", err)
	}
	if again, err := f.service.ResolveDecision(id, one); err != nil || again.Decision != api.DecisionDeny {
		t.Errorf("resolving twice = %+v, %v", again, err)
	}
	if got := f.drain(); len(got) != 0 {
		t.Errorf("events on a repeated resolution = %v", got)
	}
	// A later report without the resolved request changes nothing; the
	// second, resolved elsewhere, refuses a decision as resolved too.
	if err := f.service.ObserveApprovals(ctx, "t3code", "t1", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.BeginDecision(id, two, api.DecisionApprove); !errors.Is(err, ErrApprovalResolved) || !strings.Contains(err.Error(), "elsewhere") {
		t.Errorf("decision on a request resolved elsewhere = %v", err)
	}
	if err := f.service.Delete(ctx, id); !errors.Is(err, ErrActive) {
		t.Fatalf("delete held = %v", err)
	}
	f.service.ReleaseIntegration(ctx, "t3code")
	if err := f.service.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.BeginDecision(id, one, api.DecisionApprove); !errors.Is(err, ErrNotFound) {
		t.Errorf("approval of a deleted thread = %v", err)
	}
}
