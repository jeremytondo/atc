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

var permissionIDPattern = regexp.MustCompile(`^perm-[23456789bcdfghjkmnpqrstvwxyz]{10}$`)

var approveOrDeny = []api.PermissionOption{{Decision: api.PermissionApprove, Label: "Approve"}, {Decision: api.PermissionDeny, Label: "Decline"}}

// Pending permission requests (ATC-307) ride the thread with stable,
// derived ids: a report adds and updates them and publishes
// thread.updated whether or not the status changed; a request no longer
// reported is resolved elsewhere and stays resolved whatever a later
// report says; an unmapped identity is dropped; and T3 dropping the
// thread clears them.
func TestObservePermissions(t *testing.T) {
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
	first := PermissionObservation{RequestID: "req-1", Kind: api.PermissionCommand, Summary: "Command approval requested", Detail: "rm -rf build", Options: approveOrDeny, RequestedAt: requested}
	if err := f.service.ObservePermissions(ctx, "t3code", "t1", []PermissionObservation{first}); err != nil {
		t.Fatal(err)
	}
	thread, _ := f.service.Get(id)
	if len(thread.Permissions) != 1 || !permissionIDPattern.MatchString(thread.Permissions[0].ID) {
		t.Fatalf("permissions = %+v", thread.Permissions)
	}
	permID := thread.Permissions[0].ID
	want := api.ThreadPermission{ID: permID, ThreadID: id, Status: api.PermissionPending, Kind: api.PermissionCommand, Summary: "Command approval requested", Detail: "rm -rf build", Options: approveOrDeny, RequestedAt: requested}
	if diff := cmp.Diff(want, thread.Permissions[0]); diff != "" {
		t.Errorf("permission (-want +got):\n%s", diff)
	}
	if got := f.drain(); !slices.Equal(got, []string{"thread.updated " + id}) {
		t.Errorf("events on a new request = %v", got)
	}
	// The same report again changes nothing; a second request joins.
	if err := f.service.ObservePermissions(ctx, "t3code", "t1", []PermissionObservation{first}); err != nil {
		t.Fatal(err)
	}
	if got := f.drain(); len(got) != 0 {
		t.Errorf("events on an unchanged report = %v", got)
	}
	second := PermissionObservation{RequestID: "req-2", Kind: api.PermissionFileChange, Summary: "File-change approval requested", Detail: "main.go", Options: approveOrDeny, RequestedAt: requested.Add(time.Second)}
	if err := f.service.ObservePermissions(ctx, "t3code", "t1", []PermissionObservation{first, second}); err != nil {
		t.Fatal(err)
	}
	thread, _ = f.service.Get(id)
	if len(thread.Permissions) != 2 || thread.Permissions[0].ID != permID || thread.Permissions[1].Kind != api.PermissionFileChange {
		t.Errorf("permissions = %+v", thread.Permissions)
	}
	if got := f.drain(); !slices.Equal(got, []string{"thread.updated " + id}) {
		t.Errorf("events on a second request = %v", got)
	}
	// The first is resolved elsewhere: gone from the thread, remembered
	// as resolved with no decision, and not revived by a stale report.
	if err := f.service.ObservePermissions(ctx, "t3code", "t1", []PermissionObservation{second}); err != nil {
		t.Fatal(err)
	}
	thread, _ = f.service.Get(id)
	if len(thread.Permissions) != 1 || thread.Permissions[0].Kind != api.PermissionFileChange {
		t.Errorf("permissions after resolution elsewhere = %+v", thread.Permissions)
	}
	resolved, err := f.service.Permission(id, permID)
	if err != nil || resolved.Status != api.PermissionResolved || resolved.Decision != "" || resolved.ResolvedAt == nil {
		t.Errorf("resolved elsewhere = %+v, %v", resolved, err)
	}
	if err := f.service.ObservePermissions(ctx, "t3code", "t1", []PermissionObservation{first, second}); err != nil {
		t.Fatal(err)
	}
	if thread, _ = f.service.Get(id); len(thread.Permissions) != 1 {
		t.Errorf("stale report revived a resolved request: %+v", thread.Permissions)
	}
	f.drain()
	// A pending request's presentation can change (options arrive late).
	richer := second
	richer.Options = append(slices.Clone(approveOrDeny), api.PermissionOption{Decision: api.PermissionApproveAlways, Label: "Always", Warning: "careful"})
	if err := f.service.ObservePermissions(ctx, "t3code", "t1", []PermissionObservation{richer}); err != nil {
		t.Fatal(err)
	}
	if thread, _ = f.service.Get(id); len(thread.Permissions[0].Options) != 3 || thread.Permissions[0].Options[2].Warning != "careful" {
		t.Errorf("updated presentation = %+v", thread.Permissions[0])
	}
	if got := f.drain(); !slices.Equal(got, []string{"thread.updated " + id}) {
		t.Errorf("events on a presentation change = %v", got)
	}
	if _, err := f.service.Permission(id, "perm-nope"); !errors.Is(err, ErrPermissionNotFound) {
		t.Errorf("unknown permission = %v", err)
	}
	if _, err := f.service.Permission("thrd-nope", permID); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown thread = %v", err)
	}
	// Ids derive from the identity: the same request on another thread
	// has another id, and an unmapped identity reports nothing.
	if err := f.service.ObservePermissions(ctx, "t3code", "t-unknown", []PermissionObservation{first}); err != nil {
		t.Errorf("unmapped identity = %v", err)
	}
	if got := f.drain(); len(got) != 0 {
		t.Errorf("events for an unmapped identity = %v", got)
	}

	// T3 drops the thread: nothing pends on it any more.
	if err := f.service.ArchiveExternalThread(ctx, "t3code", "t1"); err != nil {
		t.Fatal(err)
	}
	if thread, _ = f.service.Get(id); len(thread.Permissions) != 0 || !thread.Archived {
		t.Errorf("after T3 dropped the thread = %+v", thread)
	}
}

// Deciding a request (ATC-307): the decision must target a pending
// request and an offered decision; a decision the provider committed
// resolves the request with it; a refused one leaves the request open
// to another; an unanswered one blocks a different decision but takes
// the same one again with the same key.
func TestDecidePermission(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa")
	ctx := context.Background()
	id, err := f.service.ObserveExternal(ctx, ExternalObservation{
		IntegrationID: "t3code", ProviderID: "t1", InitialDirectory: f.dir("proj-aaaaa"), Status: api.ThreadWaitingForPermission, Title: "T",
	})
	if err != nil {
		t.Fatal(err)
	}
	requests := []PermissionObservation{
		{RequestID: "req-1", Kind: api.PermissionCommand, Summary: "Command", Options: approveOrDeny},
		{RequestID: "req-2", Kind: api.PermissionCommand, Summary: "Command", Options: approveOrDeny},
	}
	if err := f.service.ObservePermissions(ctx, "t3code", "t1", requests); err != nil {
		t.Fatal(err)
	}
	thread, _ := f.service.Get(id)
	one, two := thread.Permissions[0].ID, thread.Permissions[1].ID
	f.drain()

	if _, err := f.service.BeginDecision(id, "perm-nope", api.PermissionApprove); !errors.Is(err, ErrPermissionNotFound) {
		t.Errorf("unknown request = %v", err)
	}
	if _, err := f.service.BeginDecision(id, one, api.PermissionApproveAlways); !errors.Is(err, ErrDecisionNotOffered) {
		t.Errorf("decision not offered = %v", err)
	}
	req, err := f.service.BeginDecision(id, one, api.PermissionApprove)
	if err != nil {
		t.Fatal(err)
	}
	want := DecisionRequest{IntegrationID: "t3code", ProviderID: "t1", RequestID: "req-1", Decision: api.PermissionApprove, Key: one + "/approve"}
	if req != want {
		t.Errorf("BeginDecision = %+v; want %+v", req, want)
	}
	// Unanswered: a different decision is refused, the same one begun
	// again presents the same key.
	if _, err := f.service.BeginDecision(id, one, api.PermissionDeny); !errors.Is(err, ErrDecisionPending) {
		t.Errorf("different decision while unanswered = %v; want ErrDecisionPending", err)
	}
	if again, err := f.service.BeginDecision(id, one, api.PermissionApprove); err != nil || again != want {
		t.Errorf("same decision again = %+v, %v", again, err)
	}
	// Refused: open again to any decision.
	f.service.AbandonDecision(id, one)
	if _, err := f.service.BeginDecision(id, one, api.PermissionDeny); err != nil {
		t.Errorf("decision after an abandoned one = %v", err)
	}
	// Committed: resolved with the decision, published, and final.
	permission, err := f.service.ResolveDecision(id, one)
	if err != nil || permission.Status != api.PermissionResolved || permission.Decision != api.PermissionDeny || permission.ResolvedAt == nil {
		t.Fatalf("ResolveDecision = %+v, %v", permission, err)
	}
	if got := f.drain(); !slices.Equal(got, []string{"thread.updated " + id}) {
		t.Errorf("events on resolution = %v", got)
	}
	if thread, _ = f.service.Get(id); len(thread.Permissions) != 1 || thread.Permissions[0].ID != two {
		t.Errorf("pending after a decision = %+v", thread.Permissions)
	}
	_, err = f.service.BeginDecision(id, one, api.PermissionApprove)
	if !errors.Is(err, ErrPermissionResolved) || !strings.Contains(err.Error(), "deny") {
		t.Errorf("decision on a resolved request = %v; want ErrPermissionResolved naming the decision", err)
	}
	if again, err := f.service.ResolveDecision(id, one); err != nil || again.Decision != api.PermissionDeny {
		t.Errorf("resolving twice = %+v, %v", again, err)
	}
	if got := f.drain(); len(got) != 0 {
		t.Errorf("events on a repeated resolution = %v", got)
	}
	// A later report without the resolved request changes nothing; the
	// second, resolved elsewhere, refuses a decision as resolved too.
	if err := f.service.ObservePermissions(ctx, "t3code", "t1", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.BeginDecision(id, two, api.PermissionApprove); !errors.Is(err, ErrPermissionResolved) || !strings.Contains(err.Error(), "elsewhere") {
		t.Errorf("decision on a request resolved elsewhere = %v", err)
	}
	if err := f.service.Delete(ctx, id); !errors.Is(err, ErrActive) {
		t.Fatalf("delete held = %v", err)
	}
	f.service.ReleaseIntegration(ctx, "t3code")
	if err := f.service.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Permission(id, one); !errors.Is(err, ErrNotFound) {
		t.Errorf("permission of a deleted thread = %v", err)
	}
}
