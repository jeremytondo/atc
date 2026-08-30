package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/threads"
)

// observeThread plants an observed conversation in the fixture's threads
// service — the seam the provider observers use in production (there is
// deliberately no POST /v1/threads to do it over the wire).
func (f *fixture) observeThread(t *testing.T, terminalID, providerID string, status api.ThreadStatus) string {
	t.Helper()
	id, err := f.threads.ObserveSession(context.Background(), threads.SessionObservation{
		Agent:      "claude",
		ProviderID: providerID,
		TerminalID: terminalID,
		ProjectID:  f.projectID,
		Status:     status,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// createRunningTerminal creates a terminal over the wire for threads to
// belong to.
func (f *fixture) createRunningTerminal(t *testing.T) api.Terminal {
	t.Helper()
	rec := f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, api.TerminalCreateParams{Command: "claude"}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create terminal: got %d; body %s", rec.Code, rec.Body)
	}
	return decodeTerminal(t, rec)
}

func decodeThread(t *testing.T, rec *httptest.ResponseRecorder) api.Thread {
	t.Helper()
	var thread api.Thread
	if err := json.Unmarshal(rec.Body.Bytes(), &thread); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body, err)
	}
	return thread
}

func TestThreadReadsOverTheWire(t *testing.T) {
	f := newFixture(t)
	terminal := f.createRunningTerminal(t)

	rec := f.request(t, http.MethodGet, "/v1/threads", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("empty list: got %d", rec.Code)
	}
	var list api.ThreadList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Threads == nil || len(list.Threads) != 0 {
		t.Errorf("empty list body = %s; want an empty array, not null", rec.Body)
	}

	id := f.observeThread(t, terminal.ID, "sess-1", api.ThreadWorking)
	rec = f.request(t, http.MethodGet, "/v1/threads/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: got %d; body %s", rec.Code, rec.Body)
	}
	thread := decodeThread(t, rec)
	if thread.ID != id || thread.Agent != "claude" || thread.Status != api.ThreadWorking || thread.TerminalID != terminal.ID {
		t.Errorf("thread = %+v", thread)
	}

	// Filters ride the query string.
	for path, want := range map[string]int{
		"/v1/threads":                         1,
		"/v1/threads?project=" + f.projectID:  1,
		"/v1/threads?project=proj-nope":       0,
		"/v1/threads?terminal=" + terminal.ID: 1,
		"/v1/threads?terminal=term-nope":      0,
	} {
		rec := f.request(t, http.MethodGet, path, "")
		if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		if len(list.Threads) != want {
			t.Errorf("%s = %d threads; want %d", path, len(list.Threads), want)
		}
	}

	if rec := f.request(t, http.MethodGet, "/v1/threads/thrd-zzzzz", ""); rec.Code != http.StatusNotFound {
		t.Errorf("get unknown: got %d, want 404", rec.Code)
	}

	// The terminal wire shape carries the activeThreadId projection.
	rec = f.request(t, http.MethodGet, "/v1/terminals/"+terminal.ID, "")
	if got := decodeTerminal(t, rec); got.ActiveThreadID != id {
		t.Errorf("terminal.activeThreadId = %q; want %q", got.ActiveThreadID, id)
	}
}

func TestThreadUpdateAndArchiveOverTheWire(t *testing.T) {
	f := newFixture(t)
	terminal := f.createRunningTerminal(t)
	id := f.observeThread(t, terminal.ID, "sess-1", api.ThreadIdle)

	// Title is mutable while active.
	rec := f.request(t, http.MethodPatch, "/v1/threads/"+id, `{"title":"my title"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch title: got %d; body %s", rec.Code, rec.Body)
	}
	if thread := decodeThread(t, rec); thread.Title != "my title" {
		t.Errorf("title = %q", thread.Title)
	}
	// An empty title fails validation.
	if rec := f.request(t, http.MethodPatch, "/v1/threads/"+id, `{"title":""}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("empty title: got %d, want 422", rec.Code)
	}

	// Archiving the active thread is refused, naming the holding terminal.
	rec = f.request(t, http.MethodPatch, "/v1/threads/"+id, `{"archived":true}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("archive active: got %d; body %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), terminal.ID) {
		t.Errorf("conflict body %s does not name the terminal", rec.Body)
	}

	// Once the conversation is switched away, archive works and the thread
	// disappears from the default list, returning with the opt-in flag.
	f.observeThread(t, terminal.ID, "sess-2", api.ThreadIdle)
	rec = f.request(t, http.MethodPatch, "/v1/threads/"+id, `{"archived":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("archive inactive: got %d; body %s", rec.Code, rec.Body)
	}
	if thread := decodeThread(t, rec); !thread.Archived || thread.ArchivedAt == nil {
		t.Errorf("archived thread = %+v", thread)
	}
	var list api.ThreadList
	rec = f.request(t, http.MethodGet, "/v1/threads", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Threads) != 1 {
		t.Errorf("default list = %d threads; want 1 (archived hidden)", len(list.Threads))
	}
	rec = f.request(t, http.MethodGet, "/v1/threads?includeArchived=true", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Threads) != 2 {
		t.Errorf("includeArchived list = %d threads; want 2", len(list.Threads))
	}

	// Unarchive is the same PATCH.
	rec = f.request(t, http.MethodPatch, "/v1/threads/"+id, `{"archived":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("unarchive: got %d", rec.Code)
	}
	if thread := decodeThread(t, rec); thread.Archived || thread.ArchivedAt != nil {
		t.Errorf("unarchived thread = %+v", thread)
	}

	if rec := f.request(t, http.MethodPatch, "/v1/threads/thrd-zzzzz", `{"title":"x"}`); rec.Code != http.StatusNotFound {
		t.Errorf("patch unknown: got %d, want 404", rec.Code)
	}
}

func TestThreadDeleteOverTheWire(t *testing.T) {
	f := newFixture(t)
	terminal := f.createRunningTerminal(t)
	id := f.observeThread(t, terminal.ID, "sess-1", api.ThreadIdle)

	rec := f.request(t, http.MethodDelete, "/v1/threads/"+id, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete active: got %d; body %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), terminal.ID) {
		t.Errorf("conflict body %s does not name the terminal", rec.Body)
	}

	f.observeThread(t, terminal.ID, "sess-2", api.ThreadIdle)
	if rec := f.request(t, http.MethodDelete, "/v1/threads/"+id, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete inactive: got %d; body %s", rec.Code, rec.Body)
	}
	if rec := f.request(t, http.MethodGet, "/v1/threads/"+id, ""); rec.Code != http.StatusNotFound {
		t.Errorf("get after delete: got %d, want 404", rec.Code)
	}
	if rec := f.request(t, http.MethodDelete, "/v1/threads/"+id, ""); rec.Code != http.StatusNotFound {
		t.Errorf("second delete: got %d, want 404", rec.Code)
	}
}

// Deleting a terminal clears thread linkage; deleting the project then
// removes the records — the wire-level referential lifecycle.
func TestThreadReferentialLifecycleOverTheWire(t *testing.T) {
	f := newFixture(t)
	terminal := f.createRunningTerminal(t)
	id := f.observeThread(t, terminal.ID, "sess-1", api.ThreadWorking)

	if rec := f.request(t, http.MethodDelete, "/v1/terminals/"+terminal.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete terminal: got %d", rec.Code)
	}
	rec := f.request(t, http.MethodGet, "/v1/threads/"+id, "")
	thread := decodeThread(t, rec)
	if thread.TerminalID != "" {
		t.Errorf("terminalId = %q; want cleared after terminal delete", thread.TerminalID)
	}
	if thread.Status != api.ThreadUnknown {
		t.Errorf("status = %s; want unknown after its terminal left", thread.Status)
	}

	// The other fixture terminals are gone too, so the project can go; its
	// threads go with it (unlike terminals, which block).
	if rec := f.request(t, http.MethodDelete, "/v1/projects/"+f.projectID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete project: got %d; body %s", rec.Code, rec.Body)
	}
	if rec := f.request(t, http.MethodGet, "/v1/threads/"+id, ""); rec.Code != http.StatusNotFound {
		t.Errorf("thread survived project delete: got %d, want 404", rec.Code)
	}
}

// A client that cancels its delete request after the commit must not
// leave threads linked to the deleted terminal: the cleanup runs
// detached, like the delete itself.
func TestTerminalDeleteConvergesThreadsDespiteCancel(t *testing.T) {
	f := newFixture(t)
	terminal := f.createRunningTerminal(t)
	id := f.observeThread(t, terminal.ID, "sess-1", api.ThreadWorking)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodDelete, "/v1/terminals/"+terminal.ID, nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)

	thread := decodeThread(t, f.request(t, http.MethodGet, "/v1/threads/"+id, ""))
	if thread.TerminalID != "" || thread.Status != api.ThreadUnknown {
		t.Errorf("thread after cancelled delete = %+v; want cleared linkage and unknown", thread)
	}
}

// The thread change events ride the same SSE feed with their own names.
func TestThreadEventsOnTheFeed(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewServer(f.handler)
	t.Cleanup(srv.Close)

	client := dialSSE(t, srv.URL, "")
	if opening := client.next(t); opening.Comment != "connected" {
		t.Fatalf("opening message = %+v", opening)
	}

	terminal := f.createRunningTerminal(t)
	id := f.observeThread(t, terminal.ID, "sess-1", api.ThreadIdle)

	var seen []string
	for range 4 {
		event := client.next(t)
		seen = append(seen, event.Event+" "+resourceID(t, event.Data))
	}
	// The middle terminal.updated is the create settling to running; the
	// last is the activeThreadId projection moving.
	want := []string{
		"terminal.created " + terminal.ID,
		"terminal.updated " + terminal.ID,
		"thread.created " + id,
		"terminal.updated " + terminal.ID,
	}
	if diff := cmp.Diff(want, seen); diff != "" {
		t.Errorf("feed (-want +got):\n%s", diff)
	}
}

func resourceID(t *testing.T, data string) string {
	t.Helper()
	var change api.ChangeEvent
	if err := json.Unmarshal([]byte(data), &change); err != nil {
		t.Fatalf("decoding %q: %v", data, err)
	}
	return change.ID
}
