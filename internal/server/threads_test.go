package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/events"
	"github.com/jeremytondo/atc/internal/integrations/t3code/t3codetest"
	"github.com/jeremytondo/atc/internal/threads"
)

// observeThread plants a conversation observed inside a terminal in the
// fixture's threads service — the seam the provider observers use in
// production; POST /v1/threads creates only in T3 Code. It originates in
// the fixture project's directory, so it classifies into it.
func (f *fixture) observeThread(t *testing.T, terminalID, providerID string, status api.ThreadStatus) string {
	t.Helper()
	id, err := f.threads.ObserveSession(context.Background(), threads.SessionObservation{
		IntegrationID:    "claude",
		AppID:            "claude/tui",
		AgentID:          "claude",
		ProviderID:       providerID,
		TerminalID:       terminalID,
		InitialDirectory: f.projectDir,
		Status:           status,
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
	if thread.ID != id || thread.AgentID != "claude" || thread.AppID != "claude/tui" || thread.Status != api.ThreadWorking || thread.TerminalID != terminal.ID {
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

// Deleting a terminal clears thread linkage; deleting the project leaves
// the thread alive and unassigned — the wire-level referential lifecycle.
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
	// thread survives, unassigned, with its origin intact.
	if thread.ProjectID != f.projectID || thread.InitialDirectory != f.projectDir {
		t.Fatalf("before project delete = %+v; want classified into the fixture project", thread)
	}
	if rec := f.request(t, http.MethodDelete, "/v1/projects/"+f.projectID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete project: got %d; body %s", rec.Code, rec.Body)
	}
	rec = f.request(t, http.MethodGet, "/v1/threads/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("thread after project delete: got %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, `"projectId"`) || !strings.Contains(body, `"initialDirectory":"`+f.projectDir+`"`) {
		t.Errorf("thread after project delete = %s; want no projectId and the origin kept", body)
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

// Thread mode over the wire (ATC-297): POST /v1/terminals with threadId
// answers 200 with the running terminal holding the thread, unchanged;
// once the thread is dormant it answers 201 with a new terminal running
// the exact resume, linked to it and placed as the request asks — never
// in the thread's own directory; an unknown thread is refused and
// creates nothing; an archived thread comes back unarchived. The
// response terminal carries the projection like every other terminal
// response.
func TestThreadResumeThroughTerminalCreate(t *testing.T) {
	f := newFixture(t)
	launched := decodeTerminal(t, f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, api.TerminalCreateParams{AppID: "claude/tui"})))
	id := f.observeThread(t, launched.ID, "sess-1", api.ThreadIdle)
	elsewhere := canonicalDir(t, t.TempDir())

	// Reuse: 200, the same terminal, placement ignored.
	rec := f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, api.TerminalCreateParams{ThreadID: id, Directory: elsewhere, Name: "ignored"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("resume active: got %d; body %s", rec.Code, rec.Body)
	}
	reused := decodeTerminal(t, rec)
	if reused.ID != launched.ID || reused.ActiveThreadID != id || reused.Name != launched.Name || reused.Directory != launched.Directory {
		t.Errorf("resume active = %+v; want %s untouched", reused, launched.ID)
	}

	// The terminal goes away: the thread is dormant, and archived.
	if rec := f.request(t, http.MethodDelete, "/v1/terminals/"+launched.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete terminal: got %d", rec.Code)
	}
	if rec := f.request(t, http.MethodPatch, "/v1/threads/"+id, `{"archived":true}`); rec.Code != http.StatusOK {
		t.Fatalf("archive: got %d; body %s", rec.Code, rec.Body)
	}

	// Resume: 201, a new terminal in the requested directory running the
	// exact resume, composed privately.
	rec = f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, api.TerminalCreateParams{ThreadID: id, Directory: elsewhere, Name: "again"}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("resume dormant: got %d; body %s", rec.Code, rec.Body)
	}
	resumed := decodeTerminal(t, rec)
	if resumed.ID == launched.ID || resumed.AppID != "claude/tui" || resumed.Status != api.TerminalRunning || resumed.ActiveThreadID != "" {
		t.Errorf("resume dormant = %+v", resumed)
	}
	if command := f.driverCommand(resumed.ID); !strings.HasPrefix(command, "claude --settings '") || !strings.HasSuffix(command, " --resume 'sess-1'") {
		t.Errorf("resume command = %q", command)
	}
	if resumed.Command != "" || strings.Contains(rec.Body.String(), "sess-1") {
		t.Errorf("private data leaked onto the wire: %s", rec.Body)
	}
	if resumed.Directory != elsewhere || resumed.Name != "again" || resumed.SpaceID != f.defaultSpace(t).ID {
		t.Errorf("resume placement = %+v; want %q named again in the Default space", resumed, elsewhere)
	}
	thread := decodeThread(t, f.request(t, http.MethodGet, "/v1/threads/"+id, ""))
	if thread.TerminalID != resumed.ID || thread.Archived || thread.InitialDirectory != f.projectDir {
		t.Errorf("thread after resume = %+v; want linked to %s, unarchived, origin kept", thread, resumed.ID)
	}

	// Resuming again reuses the resumed terminal — no evidence has
	// arrived, but the linkage the decision recorded counts.
	rec = f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, api.TerminalCreateParams{ThreadID: id}))
	if rec.Code != http.StatusOK || decodeTerminal(t, rec).ID != resumed.ID {
		t.Errorf("second resume: got %d; body %s; want 200 with %s", rec.Code, rec.Body, resumed.ID)
	}

	before := len(decodeTerminalList(t, f.request(t, http.MethodGet, "/v1/terminals", "")))
	rec = f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, api.TerminalCreateParams{ThreadID: "thrd-zzzzz"}))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"code":"thread_not_found"`) {
		t.Errorf("resume unknown: got %d; body %s", rec.Code, rec.Body)
	}
	if after := len(decodeTerminalList(t, f.request(t, http.MethodGet, "/v1/terminals", ""))); after != before {
		t.Errorf("resume unknown created a terminal: %d → %d", before, after)
	}
	// The selectors are exclusive.
	for name, body := range map[string]api.TerminalCreateParams{
		"thread+app":     {ThreadID: id, AppID: "claude/tui"},
		"thread+command": {ThreadID: id, Command: "hx"},
		"all three":      {ThreadID: id, AppID: "claude/tui", Command: "hx"},
	} {
		rec := f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, body))
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"code":"launch_mode_conflict"`) {
			t.Errorf("%s: got %d; body %s", name, rec.Code, rec.Body)
		}
	}
	// The old action route is gone.
	if rec := f.request(t, http.MethodPost, "/v1/threads/"+id+"/open", ""); rec.Code != http.StatusNotFound {
		t.Errorf("POST /v1/threads/{id}/open: got %d, want 404", rec.Code)
	}

	// A thread whose recorded App the catalog no longer has cannot be
	// resumed: unavailable origin, distinct from missing provenance.
	stale, err := f.threads.ObserveSession(context.Background(), threads.SessionObservation{
		IntegrationID: "claude", AppID: "claude/desktop", AgentID: "claude", ProviderID: "sess-stale", TerminalID: resumed.ID,
		InitialDirectory: f.projectDir, Status: api.ThreadIdle,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.threads.Deactivate(context.Background(), resumed.ID)
	if rec := f.request(t, http.MethodDelete, "/v1/terminals/"+resumed.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete terminal: got %d", rec.Code)
	}
	rec = f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, api.TerminalCreateParams{ThreadID: stale}))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"code":"thread_app_unavailable"`) {
		t.Errorf("resume with a vanished app: got %d; body %s", rec.Code, rec.Body)
	}
}

// A dormant thread whose App executable is missing is refused like a
// launch, with the install hint, and nothing is created or linked.
func TestThreadResumeUnavailableApp(t *testing.T) {
	f := newFixture(t)
	launched := decodeTerminal(t, f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, api.TerminalCreateParams{AppID: "claude/tui"})))
	id := f.observeThread(t, launched.ID, "sess-1", api.ThreadIdle)
	if rec := f.request(t, http.MethodDelete, "/v1/terminals/"+launched.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete terminal: got %d", rec.Code)
	}
	f.binaries["claude"] = false

	rec := f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, api.TerminalCreateParams{ThreadID: id}))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "npm install -g @anthropic-ai/claude-code") ||
		!strings.Contains(rec.Body.String(), `"code":"app_unavailable"`) {
		t.Errorf("resume unavailable: got %d; body %s", rec.Code, rec.Body)
	}
	if thread := decodeThread(t, f.request(t, http.MethodGet, "/v1/threads/"+id, "")); thread.TerminalID != "" {
		t.Errorf("refused resume linked %q", thread.TerminalID)
	}
	if terminals := decodeTerminalList(t, f.request(t, http.MethodGet, "/v1/terminals", "")); len(terminals) != 0 {
		t.Errorf("refused resume created %+v", terminals)
	}
}

func decodeTerminalList(t *testing.T, rec *httptest.ResponseRecorder) []api.Terminal {
	t.Helper()
	var list api.TerminalList
	decodeInto(t, rec, &list)
	return list.Terminals
}

// A thread a provider owns outside ATC terminals (ATC-285): integration and links on the
// wire, open refused with a clear error, archive and delete refused
// while the program still reports it and allowed once it does not, a
// title change always allowed.
func TestExternalThreadVerbsOverTheWire(t *testing.T) {
	f := newFixture(t)
	f.threads.SetLinker("t3code", func(providerID string) *api.ThreadLinks {
		return &api.ThreadLinks{Web: "http://127.0.0.1:3773/env/" + providerID, App: "t3code://threads/env/" + providerID}
	})
	id, err := f.threads.ObserveExternal(context.Background(), threads.ExternalObservation{
		IntegrationID: "t3code", ProviderID: "t1", InitialDirectory: f.projectDir, Status: api.ThreadWorking, AgentID: "codex", Title: "T3 thread",
	})
	if err != nil {
		t.Fatal(err)
	}

	thread := decodeThread(t, f.request(t, http.MethodGet, "/v1/threads/"+id, ""))
	if thread.IntegrationID != "t3code" || thread.AppID != "" || thread.AgentID != "codex" || thread.TerminalID != "" || thread.Links == nil ||
		thread.Links.Web != "http://127.0.0.1:3773/env/t1" || thread.Links.App != "t3code://threads/env/t1" {
		t.Errorf("thread = %+v", thread)
	}
	if body := f.request(t, http.MethodGet, "/v1/threads/"+id, "").Body.String(); !strings.Contains(body, `"integrationId":"t3code"`) || strings.Contains(body, `"appId"`) || !strings.Contains(body, `"links":{"web":`) {
		t.Errorf("wire body = %s", body)
	}

	rec := f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, api.TerminalCreateParams{ThreadID: id}))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"code":"thread_not_terminal_resumable"`) ||
		!strings.Contains(rec.Body.String(), "not started in an ATC terminal") {
		t.Errorf("resume: got %d; body %s", rec.Code, rec.Body)
	}
	if terminals := decodeTerminalList(t, f.request(t, http.MethodGet, "/v1/terminals", "")); len(terminals) != 0 {
		t.Errorf("refused open created %+v", terminals)
	}
	rec = f.request(t, http.MethodPatch, "/v1/threads/"+id, `{"archived":true}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "t3code") {
		t.Errorf("archive while reported: got %d; body %s", rec.Code, rec.Body)
	}
	if rec := f.request(t, http.MethodDelete, "/v1/threads/"+id, ""); rec.Code != http.StatusConflict {
		t.Errorf("delete while reported: got %d", rec.Code)
	}
	rec = f.request(t, http.MethodPatch, "/v1/threads/"+id, `{"title":"my title"}`)
	if rec.Code != http.StatusOK || decodeThread(t, rec).Title != "my title" {
		t.Errorf("title while reported: got %d; body %s", rec.Code, rec.Body)
	}

	// T3 stops reporting it: ATC archived it, and the user may now
	// unarchive, re-archive, or delete.
	if err := f.threads.ArchiveExternalThread(context.Background(), "t3code", "t1"); err != nil {
		t.Fatal(err)
	}
	if thread := decodeThread(t, f.request(t, http.MethodGet, "/v1/threads/"+id, "")); !thread.Archived || thread.Status != api.ThreadUnknown {
		t.Errorf("after removal = %+v", thread)
	}
	if rec := f.request(t, http.MethodPatch, "/v1/threads/"+id, `{"archived":false}`); rec.Code != http.StatusOK {
		t.Errorf("unarchive after removal: got %d; body %s", rec.Code, rec.Body)
	}
	if rec := f.request(t, http.MethodDelete, "/v1/threads/"+id, ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete after removal: got %d; body %s", rec.Code, rec.Body)
	}

	// Every thread carries its integration; terminal threads carry no links.
	terminal := f.createRunningTerminal(t)
	local := decodeThread(t, f.request(t, http.MethodGet, "/v1/threads/"+f.observeThread(t, terminal.ID, "sess-1", api.ThreadIdle), ""))
	if local.IntegrationID != "claude" || local.Links != nil {
		t.Errorf("terminal thread = %+v", local)
	}
}

// The Integration connection change rides the same feed with its own name.
func TestIntegrationEventOnTheFeed(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewServer(f.handler)
	t.Cleanup(srv.Close)
	client := dialSSE(t, srv.URL, "")
	if opening := client.next(t); opening.Comment != "connected" {
		t.Fatalf("opening message = %+v", opening)
	}
	f.hub.Publish(api.EventIntegrationUpdated, "integration", "t3code")
	event := client.next(t)
	if event.Event != api.EventIntegrationUpdated || resourceID(t, event.Data) != "t3code" {
		t.Errorf("feed event = %+v", event)
	}
}

// Project association over the wire (ATC-295): a thread observed before
// any project contains it is unassigned and omits projectId; creating a
// containing project backfills it (archived threads too); the merge
// patch assigns any project, null clears, omitted leaves it alone; the
// ?project filter follows.
func TestThreadProjectAssociationOverTheWire(t *testing.T) {
	f := newFixture(t)
	terminal := f.createRunningTerminal(t)
	origin := canonicalDir(t, t.TempDir())
	id, err := f.threads.ObserveSession(context.Background(), threads.SessionObservation{
		IntegrationID: "claude", AppID: "claude/tui", AgentID: "claude", ProviderID: "sess-9", TerminalID: terminal.ID,
		InitialDirectory: origin, Status: api.ThreadIdle,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.threads.Deactivate(context.Background(), terminal.ID)
	if rec := f.request(t, http.MethodPatch, "/v1/threads/"+id, `{"archived":true}`); rec.Code != http.StatusOK {
		t.Fatalf("archive: got %d; body %s", rec.Code, rec.Body)
	}
	if body := f.request(t, http.MethodGet, "/v1/threads/"+id, "").Body.String(); strings.Contains(body, `"projectId"`) || !strings.Contains(body, `"initialDirectory":"`+origin+`"`) {
		t.Errorf("unassigned thread = %s", body)
	}

	// Creating the containing project backfills the archived thread.
	project := f.createProject(t, origin)
	if got := decodeThread(t, f.request(t, http.MethodGet, "/v1/threads/"+id, "")); got.ProjectID != project.ID {
		t.Errorf("after project create = %+v; want backfilled into %s", got, project.ID)
	}
	if list := decodeThreadList(t, f.request(t, http.MethodGet, "/v1/threads?project="+project.ID+"&includeArchived=true", "")); len(list) != 1 || list[0].ID != id {
		t.Errorf("?project filter = %+v", list)
	}

	// Merge patch: assign elsewhere, leave alone, clear, refuse unknown.
	rec := f.request(t, http.MethodPatch, "/v1/threads/"+id, jsonBody(t, map[string]any{"projectId": f.projectID}))
	if rec.Code != http.StatusOK || decodeThread(t, rec).ProjectID != f.projectID {
		t.Errorf("assign: got %d; body %s", rec.Code, rec.Body)
	}
	rec = f.request(t, http.MethodPatch, "/v1/threads/"+id, `{"title":"kept"}`)
	if rec.Code != http.StatusOK || decodeThread(t, rec).ProjectID != f.projectID {
		t.Errorf("omitted projectId changed it: got %d; body %s", rec.Code, rec.Body)
	}
	rec = f.request(t, http.MethodPatch, "/v1/threads/"+id, `{"projectId":null}`)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"projectId"`) {
		t.Errorf("clear: got %d; body %s", rec.Code, rec.Body)
	}
	rec = f.request(t, http.MethodPatch, "/v1/threads/"+id, `{"projectId":"proj-nope"}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"code":"project_not_found"`) {
		t.Errorf("assign unknown: got %d; body %s", rec.Code, rec.Body)
	}
	for name, body := range map[string]string{"null title": `{"title":null}`, "null archived": `{"archived":null}`, "empty project": `{"projectId":""}`} {
		if rec := f.request(t, http.MethodPatch, "/v1/threads/"+id, body); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: got %d, want 422; body %s", name, rec.Code, rec.Body)
		}
	}

	// Moving a project onto the origin backfills the cleared thread.
	rec = f.request(t, http.MethodPatch, "/v1/projects/"+project.ID, jsonBody(t, map[string]any{"directory": t.TempDir()}))
	if rec.Code != http.StatusOK {
		t.Fatalf("move away: got %d; body %s", rec.Code, rec.Body)
	}
	if got := decodeThread(t, f.request(t, http.MethodGet, "/v1/threads/"+id, "")); got.ProjectID != "" {
		t.Fatalf("a move away assigned: %+v", got)
	}
	rec = f.request(t, http.MethodPatch, "/v1/projects/"+project.ID, jsonBody(t, map[string]any{"directory": origin}))
	if rec.Code != http.StatusOK {
		t.Fatalf("move back: got %d; body %s", rec.Code, rec.Body)
	}
	if got := decodeThread(t, f.request(t, http.MethodGet, "/v1/threads/"+id, "")); got.ProjectID != project.ID {
		t.Errorf("after move back = %+v; want backfilled into %s", got, project.ID)
	}
}

func decodeThreadList(t *testing.T, rec *httptest.ResponseRecorder) []api.Thread {
	t.Helper()
	var list api.ThreadList
	decodeInto(t, rec, &list)
	return list.Threads
}

// The turn model at the boundary (ATC-301): latestTurn is absent before
// any turn, carries an ATC-minted id and never the provider's, and
// statusDetail rides an error status only; lastError is gone from the
// schema; a turn change publishes thread.updated.
func TestThreadTurnOverTheWire(t *testing.T) {
	f := newFixture(t)
	terminal := f.createRunningTerminal(t)
	id := f.observeThread(t, terminal.ID, "sess-1", api.ThreadIdle)
	sub := f.hub.Subscribe(0, false)
	t.Cleanup(sub.Close)

	body := f.request(t, http.MethodGet, "/v1/threads/"+id, "").Body.String()
	for _, absent := range []string{`"latestTurn"`, `"lastError"`, `"statusDetail"`} {
		if strings.Contains(body, absent) {
			t.Errorf("thread before any turn carries %s: %s", absent, body)
		}
	}
	if rec := get(f.handler, "/openapi.json", true); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "lastError") || !strings.Contains(rec.Body.String(), "latestTurn") {
		t.Errorf("schema: got %d; lastError present %v, latestTurn present %v", rec.Code, strings.Contains(rec.Body.String(), "lastError"), strings.Contains(rec.Body.String(), "latestTurn"))
	}

	observe := func(status api.ThreadStatus, detail string, turn *threads.TurnObservation) {
		t.Helper()
		if err := f.threads.ObserveStatus(context.Background(), threads.StatusObservation{
			IntegrationID: "claude", ProviderID: "sess-1", Status: status, StatusDetail: detail, Turn: turn,
		}); err != nil {
			t.Fatal(err)
		}
	}
	observe(api.ThreadWorking, "", &threads.TurnObservation{ProviderID: "provider-turn-1", State: api.TurnRunning})
	select {
	case change := <-sub.C:
		if change.Type != api.EventThreadUpdated || change.ID != id {
			t.Errorf("event after a turn change = %+v", change)
		}
	case <-time.After(time.Second):
		t.Error("no thread.updated after a turn change")
	}
	rec := f.request(t, http.MethodGet, "/v1/threads/"+id, "")
	thread := decodeThread(t, rec)
	if thread.LatestTurn == nil || !strings.HasPrefix(thread.LatestTurn.ID, "turn-") || len(thread.LatestTurn.ID) != len("turn-")+10 ||
		thread.LatestTurn.State != api.TurnRunning || thread.LatestTurn.CompletedAt != nil {
		t.Errorf("latest turn = %+v", thread.LatestTurn)
	}
	if body := rec.Body.String(); strings.Contains(body, "provider-turn-1") || strings.Contains(body, `"completedAt"`) || strings.Contains(body, `"statusDetail"`) || strings.Contains(body, `"response"`) {
		t.Errorf("wire body = %s", body)
	}

	// The turn ends with its response (ATC-303): on the Thread, one
	// thread.updated; the same text recovered again publishes nothing, a
	// changed text publishes once; a new turn clears it.
	observe(api.ThreadIdle, "", &threads.TurnObservation{ProviderID: "provider-turn-1", State: api.TurnCompleted, Response: "Fixed the **build**.\n\n- one\n- two"})
	thread = decodeThread(t, f.request(t, http.MethodGet, "/v1/threads/"+id, ""))
	if thread.LatestTurn.State != api.TurnCompleted || thread.LatestTurn.Response != "Fixed the **build**.\n\n- one\n- two" {
		t.Errorf("completed turn = %+v", thread.LatestTurn)
	}
	if got := changes(sub); !slices.Equal(got, []string{"thread.updated " + id}) {
		t.Errorf("events on completion = %v", got)
	}
	if err := f.threads.ObserveTurnResponse(context.Background(), id, "provider-turn-1", "Fixed the **build**.\n\n- one\n- two"); err != nil {
		t.Fatal(err)
	}
	if got := changes(sub); len(got) != 0 {
		t.Errorf("events on an identical recovery = %v", got)
	}
	if err := f.threads.ObserveTurnResponse(context.Background(), id, "provider-turn-1", "Fixed the build."); err != nil {
		t.Fatal(err)
	}
	if got := changes(sub); !slices.Equal(got, []string{"thread.updated " + id}) {
		t.Errorf("events on a changed recovery = %v", got)
	}
	if list := decodeThreadList(t, f.request(t, http.MethodGet, "/v1/threads", "")); len(list) != 1 || list[0].LatestTurn == nil || list[0].LatestTurn.Response != "Fixed the build." {
		t.Errorf("list = %+v", list)
	}
	observe(api.ThreadWorking, "", &threads.TurnObservation{ProviderID: "provider-turn-2", State: api.TurnRunning})
	rec = f.request(t, http.MethodGet, "/v1/threads/"+id, "")
	if thread = decodeThread(t, rec); thread.LatestTurn.State != api.TurnRunning || strings.Contains(rec.Body.String(), `"response"`) {
		t.Errorf("new turn = %+v; body %s", thread.LatestTurn, rec.Body)
	}
	changes(sub)

	observe(api.ThreadError, "session broke", nil)
	thread = decodeThread(t, f.request(t, http.MethodGet, "/v1/threads/"+id, ""))
	if thread.Status != api.ThreadError || thread.StatusDetail != "session broke" || thread.LatestTurn.State != api.TurnFailed || thread.LatestTurn.Error != "session broke" {
		t.Errorf("faulted = %+v turn %+v", thread, thread.LatestTurn)
	}
	observe(api.ThreadIdle, "", nil)
	if body := f.request(t, http.MethodGet, "/v1/threads/"+id, "").Body.String(); strings.Contains(body, `"statusDetail"`) || !strings.Contains(body, `"state":"failed"`) {
		t.Errorf("recovered body = %s", body)
	}
	threads := decodeThreadList(t, f.request(t, http.MethodGet, "/v1/threads", ""))
	if len(threads) != 1 || threads[0].LatestTurn == nil {
		t.Errorf("list = %+v", threads)
	}
}

// connectT3 brings the fixture's T3 Code Integration up against the fake
// environment, which knows one project rooted at root, and keeps it
// running until the test ends.
func (f *fixture) connectT3(t *testing.T, root string) {
	t.Helper()
	t3codetest.Connect(t, f.t3Server, f.t3Home, root, f.t3.Run, f.t3.Connection)
}

func waitUntil(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// changes drains a hub subscription as "type id" strings.
func changes(sub *events.Subscription) []string {
	var got []string
	for {
		select {
		case change := <-sub.C:
			got = append(got, change.Type+" "+change.ID)
		default:
			return got
		}
	}
}

func decodeProblem(t *testing.T, rec *httptest.ResponseRecorder) api.Problem {
	t.Helper()
	var problem api.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body, err)
	}
	return problem
}

var (
	threadIDPattern = regexp.MustCompile(`^thrd-[a-z2-9]{5}$`)
	turnIDPattern   = regexp.MustCompile(`^turn-[a-z2-9]{10}$`)
)

const createThreadBody = `{"integrationId":"t3code","agent":"codex","projectId":"proj-fixtr","prompt":"  Fix the   build ","model":"gpt-5.6-sol","options":[{"id":"reasoningEffort","value":"high"}]}`

// The acceptance path: T3 connected and knowing the project's directory,
// a create returns 201 with the thread already working on a provisional
// turn, T3 received exactly one command, and T3's later report of the
// thread updates the same record, binding its turn.
func TestThreadCreateOverTheWire(t *testing.T) {
	f := newFixture(t)
	f.connectT3(t, f.projectDir)
	sub := f.hub.Subscribe(0, false)
	t.Cleanup(sub.Close)

	rec := f.request(t, http.MethodPost, "/v1/threads", createThreadBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d; body %s", rec.Code, rec.Body)
	}
	thread := decodeThread(t, rec)
	commands := f.t3Server.Commands()
	if len(commands) != 1 {
		t.Fatalf("T3 received %d commands; want 1", len(commands))
	}
	t3ID, _ := commands[0]["threadId"].(string)
	if !threadIDPattern.MatchString(thread.ID) || thread.LatestTurn == nil || !turnIDPattern.MatchString(thread.LatestTurn.ID) || t3ID == "" {
		t.Fatalf("thread = %+v; command %v", thread, commands[0])
	}
	want := api.Thread{
		ID: thread.ID, IntegrationID: "t3code", AgentID: "codex", ProjectID: f.projectID, InitialDirectory: f.projectDir,
		Title: "Fix the build", Model: "gpt-5.6-sol", Cwd: f.projectDir, Status: api.ThreadWorking,
		LatestTurn:     &api.ThreadTurn{ID: thread.LatestTurn.ID, State: api.TurnRunning, StartedAt: thread.LatestTurn.StartedAt},
		LastEvidenceAt: thread.LastEvidenceAt,
		Links:          &api.ThreadLinks{Web: f.t3Server.Origin() + "/env-1/" + t3ID, App: "t3code://threads/env-1/" + t3ID},
		CreatedAt:      thread.CreatedAt, UpdatedAt: thread.UpdatedAt,
	}
	if diff := cmp.Diff(want, thread); diff != "" {
		t.Errorf("thread (-want +got):\n%s", diff)
	}
	// The request's opaque values reached T3 untouched, in the project T3
	// knows the directory as; the full payload is pinned in the
	// Integration's own tests.
	create := commands[0]["bootstrap"].(map[string]any)["createThread"].(map[string]any)
	selection := map[string]any{"instanceId": "codex", "model": "gpt-5.6-sol", "options": []any{map[string]any{"id": "reasoningEffort", "value": "high"}}}
	if diff := cmp.Diff(map[string]any{"projectId": "p1", "modelSelection": selection},
		map[string]any{"projectId": create["projectId"], "modelSelection": create["modelSelection"]}); diff != "" {
		t.Errorf("bootstrap (-want +got):\n%s", diff)
	}
	if got := changes(sub); !slices.Equal(got, []string{"thread.created " + thread.ID, "thread.updated " + thread.ID}) {
		t.Errorf("events = %v; want created (pre-creation) then updated (the submitted turn)", got)
	}

	// T3 reports the thread: the same ATC record, T3's first turn bound to
	// the provisional id with T3's timestamps.
	f.t3Server.Push(t3codetest.Upserted(2, t3codetest.ThreadItem(t3ID, "p1", "Fix the build",
		t3codetest.Model("codex", "gpt-5.6-sol"), t3codetest.WithSession("running", "codex"),
		t3codetest.LatestTurn("pt-1", "running", "2026-09-01T00:00:02Z", nil))))
	started := time.Date(2026, 9, 1, 0, 0, 2, 0, time.UTC)
	var reported api.Thread
	waitUntil(t, "T3's turn to bind", func() bool {
		reported, _ = f.threads.Get(thread.ID)
		return reported.LatestTurn != nil && reported.LatestTurn.StartedAt.Equal(started)
	})
	if reported.LatestTurn.ID != thread.LatestTurn.ID || reported.LatestTurn.State != api.TurnRunning || reported.Status != api.ThreadWorking {
		t.Errorf("after T3's report = %+v; want the provisional turn %s bound and running", reported.LatestTurn, thread.LatestTurn.ID)
	}
	rec = f.request(t, http.MethodGet, "/v1/threads", "")
	var list api.ThreadList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Threads) != 1 {
		t.Errorf("threads after T3's report = %d; want the one record", len(list.Threads))
	}
	if got := changes(sub); slices.Contains(got, "thread.created "+thread.ID) || !slices.Contains(got, "thread.updated "+thread.ID) {
		t.Errorf("events after T3's report = %v; want an update and no second creation", got)
	}
}

// T3's report can arrive before the dispatch reply does: the result is
// the same record, and the response already reflects it.
func TestThreadCreateReportedBeforeReply(t *testing.T) {
	f := newFixture(t)
	f.connectT3(t, f.projectDir)
	sub := f.hub.Subscribe(0, false)
	t.Cleanup(sub.Close)
	// The fake's goroutine cannot fail the test itself: it polls with a
	// bound, records whether T3's report was applied before it answered,
	// and the assertions follow the request.
	var reportedFirst atomic.Bool
	f.t3Server.SetDispatch(func(command map[string]any) t3codetest.DispatchReply {
		t3ID := command["threadId"].(string)
		f.t3Server.Push(t3codetest.Upserted(2, t3codetest.ThreadItem(t3ID, "p1", "Fix the build",
			t3codetest.WithSession("running", "codex"), t3codetest.LatestTurn("pt-1", "running", "2026-09-01T00:00:02Z", nil))))
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			threads := f.threads.List("", "", false)
			if len(threads) == 1 && threads[0].LatestTurn != nil && threads[0].LatestTurn.StartedAt.Equal(time.Date(2026, 9, 1, 0, 0, 2, 0, time.UTC)) {
				reportedFirst.Store(true)
				break
			}
		}
		return t3codetest.DispatchReply{}
	})

	rec := f.request(t, http.MethodPost, "/v1/threads", createThreadBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d; body %s", rec.Code, rec.Body)
	}
	if !reportedFirst.Load() {
		t.Fatal("T3's report was not applied before the dispatch answered; the test proves nothing about that order")
	}
	thread := decodeThread(t, rec)
	if thread.Status != api.ThreadWorking || thread.LatestTurn == nil || thread.LatestTurn.State != api.TurnRunning ||
		!thread.LatestTurn.StartedAt.Equal(time.Date(2026, 9, 1, 0, 0, 2, 0, time.UTC)) {
		t.Errorf("thread = %+v; want working with T3's turn already bound", thread)
	}
	if got := changes(sub); count(got, "thread.created "+thread.ID) != 1 {
		t.Errorf("events = %v; want exactly one creation", got)
	}
	if list := f.threads.List("", "", false); len(list) != 1 {
		t.Errorf("threads = %d; want one", len(list))
	}
}

// T3 rejecting the command: 502 with T3's message (and the rollback when
// T3 reports one), the pre-created record gone, thread.deleted published.
func TestThreadCreateRejected(t *testing.T) {
	f := newFixture(t)
	f.connectT3(t, f.projectDir)
	sub := f.hub.Subscribe(0, false)
	t.Cleanup(sub.Close)
	f.t3Server.SetDispatch(func(map[string]any) t3codetest.DispatchReply {
		return t3codetest.DispatchReply{Reject: "provider instance codex is not configured", RolledBack: true}
	})

	rec := f.request(t, http.MethodPost, "/v1/threads", createThreadBody)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("create: got %d; body %s", rec.Code, rec.Body)
	}
	problem := decodeProblem(t, rec)
	if problem.Code != api.CodeThreadCreationFailed || !strings.Contains(problem.Detail, "provider instance codex is not configured") ||
		!strings.Contains(problem.Detail, "rolled back the thread") {
		t.Errorf("problem = %+v", problem)
	}
	if list := f.threads.List("", "", true); len(list) != 0 {
		t.Errorf("threads after a rejection = %+v; want none", list)
	}
	got := changes(sub)
	if len(got) != 3 || !strings.HasPrefix(got[0], "thread.created ") || !strings.HasPrefix(got[1], "thread.updated ") || !strings.HasPrefix(got[2], "thread.deleted ") {
		t.Errorf("events = %v; want created, updated, deleted for the discarded record", got)
	}
}

// Every refusal before dispatch, each with its status and code, and none
// of them reaching T3.
func TestThreadCreateRefusals(t *testing.T) {
	body := func(integration, agent, project, prompt, model, options string) string {
		return fmt.Sprintf(`{"integrationId":%q,"agent":%q,"projectId":%q,"prompt":%q,"model":%q,"options":%s}`, integration, agent, project, prompt, model, options)
	}
	cases := []struct {
		name   string
		body   string
		status int
		code   string
		detail string
	}{
		{"empty prompt", body("t3code", "codex", "proj-fixtr", " \n", "m", "[]"), http.StatusBadRequest, api.CodeValidationFailed, "prompt is empty"},
		{"empty model", body("t3code", "codex", "proj-fixtr", "hi", "", "[]"), http.StatusBadRequest, api.CodeValidationFailed, "model is empty"},
		{"unknown integration", body("nope", "codex", "proj-fixtr", "hi", "m", "[]"), http.StatusBadRequest, api.CodeIntegrationNotFound, `"nope"`},
		{"integration without creation", body("claude", "claude", "proj-fixtr", "hi", "m", "[]"), http.StatusBadRequest, api.CodeThreadCreationUnsupported, "does not support thread creation"},
		{"unlisted agent", body("t3code", "gpt", "proj-fixtr", "hi", "m", "[]"), http.StatusBadRequest, api.CodeAgentNotFound, `no agent "gpt"`},
		{"option without id", body("t3code", "codex", "proj-fixtr", "hi", "m", `[{"id":"","value":"high"}]`), http.StatusBadRequest, api.CodeValidationFailed, "option has no id"},
		{"unknown project", body("t3code", "codex", "proj-nope", "hi", "m", "[]"), http.StatusNotFound, api.CodeProjectNotFound, "project not found"},
		{"not connected", body("t3code", "codex", "proj-fixtr", "hi", "m", "[]"), http.StatusServiceUnavailable, api.CodeIntegrationNotConnected, "T3 Code is unavailable: "},
	}
	f := newFixture(t)
	for _, tc := range cases {
		rec := f.request(t, http.MethodPost, "/v1/threads", tc.body)
		problem := decodeProblem(t, rec)
		if rec.Code != tc.status || problem.Code != tc.code || !strings.Contains(problem.Detail, tc.detail) {
			t.Errorf("%s: got %d %+v; want %d %s containing %q", tc.name, rec.Code, problem, tc.status, tc.code, tc.detail)
		}
	}
	if list := f.threads.List("", "", true); len(list) != 0 {
		t.Errorf("threads after refusals = %+v", list)
	}
	if commands := f.t3Server.Commands(); len(commands) != 0 {
		t.Errorf("T3 received %d commands", len(commands))
	}

	// A project T3 does not know is a conflict, not a creation.
	f = newFixture(t)
	f.connectT3(t, t.TempDir())
	rec := f.request(t, http.MethodPost, "/v1/threads", createThreadBody)
	problem := decodeProblem(t, rec)
	if rec.Code != http.StatusConflict || problem.Code != api.CodeProjectNotRegistered || !strings.Contains(problem.Detail, "not registered in T3 Code") {
		t.Errorf("unregistered project: got %d %+v", rec.Code, problem)
	}
	if list := f.threads.List("", "", true); len(list) != 0 {
		t.Errorf("threads after the conflict = %+v", list)
	}
	if commands := f.t3Server.Commands(); len(commands) != 0 {
		t.Errorf("T3 received %d commands", len(commands))
	}
}

func count(items []string, want string) int {
	n := 0
	for _, item := range items {
		if item == want {
			n++
		}
	}
	return n
}
