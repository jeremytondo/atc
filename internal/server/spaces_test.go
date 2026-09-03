package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
)

func decodeSpace(t *testing.T, rec *httptest.ResponseRecorder) api.Space {
	t.Helper()
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("got %d; body %s", rec.Code, rec.Body)
	}
	var space api.Space
	if err := json.Unmarshal(rec.Body.Bytes(), &space); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body, err)
	}
	return space
}

// Spaces over the wire (ATC-296): the Default space exists and refuses
// change; a regular space's CRUD, canonical directory, name default,
// merge patch, and the typed refusals.
func TestSpaceCRUDOverTheWire(t *testing.T) {
	f := newFixture(t)
	def := f.defaultSpace(t)

	var list api.SpaceList
	decodeInto(t, f.request(t, http.MethodGet, "/v1/spaces", ""), &list)
	if len(list.Spaces) != 1 || list.Spaces[0].ID != def.ID || !list.Spaces[0].IsDefault || list.Spaces[0].Directory != f.projectDir {
		t.Fatalf("spaces = %+v; want the one Default space at %s", list.Spaces, f.projectDir)
	}
	for name, req := range map[string]struct{ method, body string }{
		"update": {http.MethodPatch, `{"name":"x"}`},
		"delete": {http.MethodDelete, ""},
	} {
		rec := f.request(t, req.method, "/v1/spaces/"+def.ID, req.body)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"code":"space_default"`) {
			t.Errorf("%s Default: got %d; body %s", name, rec.Code, rec.Body)
		}
	}

	dir := canonicalDir(t, t.TempDir())
	rec := f.request(t, http.MethodPost, "/v1/spaces", jsonBody(t, map[string]any{"directory": dir}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d; body %s", rec.Code, rec.Body)
	}
	created := decodeSpace(t, rec)
	if created.Directory != dir || created.Name != filepath.Base(dir) || created.IsDefault || !strings.HasPrefix(created.ID, "spce-") {
		t.Errorf("created = %+v", created)
	}
	if diff := cmp.Diff(created, decodeSpace(t, f.request(t, http.MethodGet, "/v1/spaces/"+created.ID, ""))); diff != "" {
		t.Errorf("get (-created +got):\n%s", diff)
	}
	// Home by default; a missing directory is refused.
	if got := decodeSpace(t, f.request(t, http.MethodPost, "/v1/spaces", `{"name":"home"}`)); got.Directory != f.projectDir {
		t.Errorf("create without directory = %+v; want the server user's home", got)
	}
	rec = f.request(t, http.MethodPost, "/v1/spaces", jsonBody(t, map[string]any{"directory": filepath.Join(dir, "nope")}))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"code":"space_directory_invalid"`) {
		t.Errorf("create missing directory: got %d; body %s", rec.Code, rec.Body)
	}

	moved := canonicalDir(t, t.TempDir())
	rec = f.request(t, http.MethodPatch, "/v1/spaces/"+created.ID, jsonBody(t, map[string]any{"name": "moved", "directory": moved}))
	if rec.Code != http.StatusOK {
		t.Fatalf("update: got %d; body %s", rec.Code, rec.Body)
	}
	if got := decodeSpace(t, rec); got.Name != "moved" || got.Directory != moved {
		t.Errorf("updated = %+v", got)
	}
	for name, body := range map[string]string{"null name": `{"name":null}`, "unknown field": `{"frobnicate":true}`, "empty name": `{"name":" "}`} {
		if rec := f.request(t, http.MethodPatch, "/v1/spaces/"+created.ID, body); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: got %d, want 422; body %s", name, rec.Code, rec.Body)
		}
	}
	if rec := f.request(t, http.MethodGet, "/v1/spaces/spce-zzzzz", ""); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"code":"space_not_found"`) {
		t.Errorf("get unknown: got %d; body %s", rec.Code, rec.Body)
	}
	if rec := f.request(t, http.MethodDelete, "/v1/spaces/"+created.ID, ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete: got %d; body %s", rec.Code, rec.Body)
	}
	if rec := f.request(t, http.MethodDelete, "/v1/spaces/"+created.ID, ""); rec.Code != http.StatusNotFound {
		t.Errorf("second delete: got %d, want 404", rec.Code)
	}
}

// Terminals belong to spaces: unnamed placement is the Default space and
// its directory; a named space supplies its directory; an explicit
// directory wins; a move changes only spaceId and keeps the App, the
// active thread, and the session; the list filters by space.
func TestTerminalsInSpacesOverTheWire(t *testing.T) {
	f := newFixture(t)
	dir := canonicalDir(t, t.TempDir())
	space := decodeSpace(t, f.request(t, http.MethodPost, "/v1/spaces", jsonBody(t, map[string]any{"directory": dir, "name": "work"})))

	inDefault := decodeTerminal(t, f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, api.TerminalCreateParams{})))
	inSpace := decodeTerminal(t, f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, api.TerminalCreateParams{SpaceID: space.ID})))
	explicit := decodeTerminal(t, f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, api.TerminalCreateParams{SpaceID: space.ID, Directory: f.projectDir, Name: "named"})))
	if inDefault.SpaceID != f.defaultSpace(t).ID || inDefault.Directory != f.projectDir {
		t.Errorf("default placement = %+v", inDefault)
	}
	if inSpace.SpaceID != space.ID || inSpace.Directory != dir || inSpace.Name != filepath.Base(dir) {
		t.Errorf("space placement = %+v", inSpace)
	}
	if explicit.SpaceID != space.ID || explicit.Directory != f.projectDir || explicit.Name != "named" {
		t.Errorf("explicit placement = %+v", explicit)
	}
	for name, body := range map[string]string{
		"unknown space":     `{"spaceId":"spce-zzzzz"}`,
		"missing directory": jsonBody(t, map[string]any{"directory": filepath.Join(dir, "nope")}),
	} {
		if rec := f.request(t, http.MethodPost, "/v1/terminals", body); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: got %d, want 422; body %s", name, rec.Code, rec.Body)
		}
	}
	var list api.TerminalList
	decodeInto(t, f.request(t, http.MethodGet, "/v1/terminals?space="+space.ID, ""), &list)
	if len(list.Terminals) != 2 {
		t.Errorf("filtered list = %+v, want the two in the space", list.Terminals)
	}
	decodeInto(t, f.request(t, http.MethodGet, "/v1/terminals", ""), &list)
	if len(list.Terminals) != 3 {
		t.Errorf("unfiltered list = %+v, want all three", list.Terminals)
	}

	// An App-launched terminal with an active thread moves intact.
	launched := decodeTerminal(t, f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, api.TerminalCreateParams{AppID: "claude/tui"})))
	threadID := f.observeThread(t, launched.ID, "sess-1", api.ThreadWorking)
	rec := f.request(t, http.MethodPatch, "/v1/terminals/"+launched.ID, jsonBody(t, map[string]any{"spaceId": space.ID}))
	if rec.Code != http.StatusOK {
		t.Fatalf("move: got %d; body %s", rec.Code, rec.Body)
	}
	moved := decodeTerminal(t, rec)
	want := launched
	want.SpaceID, want.UpdatedAt, want.ActiveThreadID = space.ID, moved.UpdatedAt, threadID
	if diff := cmp.Diff(want, moved); diff != "" {
		t.Errorf("moved (-want +got):\n%s", diff)
	}
	if got := decodeThread(t, f.request(t, http.MethodGet, "/v1/threads/"+threadID, "")); got.TerminalID != launched.ID || got.Status != api.ThreadWorking {
		t.Errorf("thread after move = %+v; want untouched", got)
	}
	if rec := f.request(t, http.MethodPatch, "/v1/terminals/"+launched.ID, `{"spaceId":null}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("null spaceId: got %d, want 422; body %s", rec.Code, rec.Body)
	}
}

// Deleting a space deletes every terminal in it through the same
// workflow as an individual delete: hook secrets revoked, thread linkage
// cleared, threads surviving; the events follow the same order.
func TestSpaceDeleteRunsTheTerminalWorkflow(t *testing.T) {
	f := newFixture(t)
	space := decodeSpace(t, f.request(t, http.MethodPost, "/v1/spaces", jsonBody(t, map[string]any{"directory": f.projectDir, "name": "doomed"})))
	launched := decodeTerminal(t, f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, api.TerminalCreateParams{SpaceID: space.ID, AppID: "claude/tui"})))
	secret := hookSecret(t, f, launched)
	if rec := f.postHook(t, secret, `{"session_id":"s1","hook_event_name":"SessionStart","source":"startup"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("hook before delete: got %d", rec.Code)
	}
	threadID := f.observeThread(t, launched.ID, "s1", api.ThreadWorking)
	plain := decodeTerminal(t, f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, api.TerminalCreateParams{SpaceID: space.ID})))
	f.driver.mu.Lock()
	delete(f.driver.sessions, plain.ID) // vanished: missing
	f.driver.mu.Unlock()
	sub := f.hub.Subscribe(0, false)
	defer sub.Close()

	if rec := f.request(t, http.MethodDelete, "/v1/spaces/"+space.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete space: got %d; body %s", rec.Code, rec.Body)
	}
	for _, id := range []string{launched.ID, plain.ID} {
		if rec := f.request(t, http.MethodGet, "/v1/terminals/"+id, ""); rec.Code != http.StatusNotFound {
			t.Errorf("terminal %s after space delete: got %d, want 404", id, rec.Code)
		}
	}
	if rec := f.postHook(t, secret, `{"session_id":"s1","hook_event_name":"Stop"}`); rec.Code != http.StatusNotFound {
		t.Errorf("hook after space delete: got %d, want 404 (secret revoked)", rec.Code)
	}
	thread := decodeThread(t, f.request(t, http.MethodGet, "/v1/threads/"+threadID, ""))
	if thread.TerminalID != "" || thread.Status != api.ThreadUnknown {
		t.Errorf("thread after space delete = %+v; want unlinked, coerced, alive", thread)
	}
	if !f.driver.killed(launched.ID) {
		t.Error("the running session was not killed")
	}
	// Terminals delete in id order, each followed by its thread's
	// unlinking; status reconciles ride along as terminal.updated; the
	// space's own event comes last.
	var events []string
	for len(sub.C) > 0 {
		change := <-sub.C
		if change.Type != api.EventTerminalUpdated {
			events = append(events, change.Type+" "+change.ID)
		}
	}
	wantEvents := []string{"terminal.deleted " + launched.ID, "thread.updated " + threadID, "terminal.deleted " + plain.ID}
	if launched.ID > plain.ID {
		wantEvents = []string{"terminal.deleted " + plain.ID, "terminal.deleted " + launched.ID, "thread.updated " + threadID}
	}
	wantEvents = append(wantEvents, "space.deleted "+space.ID)
	if diff := cmp.Diff(wantEvents, events); diff != "" {
		t.Errorf("events (-want +got):\n%s", diff)
	}
}

// Space events ride the public feed with their own names, and the
// document lists them.
func TestSpaceEventsOnTheFeed(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewServer(f.handler)
	t.Cleanup(srv.Close)
	client := dialSSE(t, srv.URL, "")
	if opening := client.next(t); opening.Comment != "connected" {
		t.Fatalf("opening message = %+v", opening)
	}
	space := decodeSpace(t, f.request(t, http.MethodPost, "/v1/spaces", jsonBody(t, map[string]any{"directory": f.projectDir})))
	if rec := f.request(t, http.MethodPatch, "/v1/spaces/"+space.ID, `{"name":"renamed"}`); rec.Code != http.StatusOK {
		t.Fatalf("update: got %d; body %s", rec.Code, rec.Body)
	}
	if rec := f.request(t, http.MethodDelete, "/v1/spaces/"+space.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: got %d; body %s", rec.Code, rec.Body)
	}
	for _, want := range []string{api.EventSpaceCreated, api.EventSpaceUpdated, api.EventSpaceDeleted} {
		if event := client.next(t); event.Event != want || resourceID(t, event.Data) != space.ID {
			t.Errorf("feed event = %+v, want %s for %s", event, want, space.ID)
		}
	}
	doc := f.request(t, http.MethodGet, "/openapi.json", "").Body.String()
	for _, name := range []string{"SpaceCreatedEvent", "SpaceUpdatedEvent", "SpaceDeletedEvent"} {
		if !strings.Contains(doc, name) {
			t.Errorf("openapi document lacks %s", name)
		}
	}
}
