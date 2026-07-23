package session

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jeremytondo/atc/internal/store"
	"github.com/jeremytondo/atc/internal/zmx"
)

const testWorkspaceID = "wsp_test"

type fakeMux struct {
	startErr   error
	started    []string
	dir        string
	argv       []string
	live       map[string]bool
	listErr    error
	startHook  func()
	startCalls int

	sendName    string
	sendPayload []byte
	sendCalls   int

	attachName  string
	attachRows  uint16
	attachCols  uint16
	attachCalls int

	terminateName  string
	terminateCalls int
	terminateErr   error
}

func (f *fakeMux) Start(_ context.Context, name, dir string, argv []string) error {
	f.startCalls++
	f.started = append(f.started, name)
	f.dir = dir
	f.argv = append([]string(nil), argv...)
	if f.startErr != nil {
		return f.startErr
	}
	if f.live == nil {
		f.live = map[string]bool{}
	}
	f.live[name] = true
	if f.startHook != nil {
		f.startHook()
	}
	return nil
}

func (f *fakeMux) Send(_ context.Context, name string, payload []byte) error {
	f.sendCalls++
	f.sendName = name
	f.sendPayload = append([]byte(nil), payload...)
	return nil
}
func (f *fakeMux) Attach(_ context.Context, name string, rows, cols uint16) (zmx.PTY, error) {
	f.attachName, f.attachRows, f.attachCols = name, rows, cols
	f.attachCalls++
	return nil, nil
}
func (f *fakeMux) List(context.Context) ([]zmx.Session, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]zmx.Session, 0, len(f.live))
	for name := range f.live {
		out = append(out, zmx.Session{Name: name})
	}
	return out, nil
}
func (f *fakeMux) Terminate(_ context.Context, name string) error {
	f.terminateName = name
	f.terminateCalls++
	if f.terminateErr != nil {
		return f.terminateErr
	}
	delete(f.live, name)
	return nil
}

type fakeResolver struct {
	dir    string
	err    error
	calls  int
	lastID string
}

func (f *fakeResolver) ResolveForStart(_ context.Context, id string) (string, error) {
	f.calls++
	f.lastID = id
	return f.dir, f.err
}

func newTestService(t *testing.T, mux *fakeMux) (*Service, *store.Store) {
	t.Helper()
	return newTestServiceAtPath(t, mux, filepath.Join(t.TempDir(), "atc.db"))
}

func newTestServiceAtPath(t *testing.T, mux *fakeMux, dbPath string) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if _, err := st.CreateProject(ctx, store.CreateProjectInput{ID: "prj_test", Name: "Test", WorkingDir: t.TempDir()}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := st.CreateWorkspace(ctx, store.CreateWorkspaceInput{ID: testWorkspaceID, ProjectID: "prj_test", Name: "Test"}); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	return NewService(st, mux, &fakeResolver{dir: "/work"}, nil), st
}

func TestStartCopiesActionIdentityAndUsesLiteralArgv(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	mux := &fakeMux{}
	svc, st := newTestService(t, mux)
	ctx := context.Background()
	action, err := st.CreateAction(ctx, store.Action{
		Name: "Agent", Description: "before", Enabled: true, Command: "agent",
		Args: []string{"$HOME", "{{prompt}}", "two words"}, IsAgent: true,
	})
	if err != nil {
		t.Fatalf("CreateAction: %v", err)
	}

	started, err := svc.Start(ctx, StartInput{WorkspaceID: testWorkspaceID, ActionID: action.ID, Name: "Review"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if started.ActionID != action.ID || started.ActionName != "Agent" || !started.IsAgent {
		t.Fatalf("started identity = %+v", started)
	}
	wantArgv := []string{"/bin/zsh", "-l", "-i", "-c", `agent '$HOME' '{{prompt}}' 'two words'`}
	if !reflect.DeepEqual(mux.argv, wantArgv) {
		t.Fatalf("argv = %#v, want %#v", mux.argv, wantArgv)
	}

	renamed := "Editor"
	isAgent := false
	enabled := false
	if _, err := st.UpdateAction(ctx, action.ID, store.UpdateActionInput{Name: &renamed, IsAgent: &isAgent, Enabled: &enabled}); err != nil {
		t.Fatalf("UpdateAction: %v", err)
	}
	if err := st.DeleteAction(ctx, action.ID); err != nil {
		t.Fatalf("DeleteAction: %v", err)
	}
	got, err := svc.Read(ctx, started.ID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.ActionID != action.ID || got.ActionName != "Agent" || !got.IsAgent {
		t.Fatalf("session identity changed after Action mutation: %+v", got)
	}
}

func TestStartInteractiveShell(t *testing.T) {
	t.Setenv("SHELL", "/bin/fish")
	mux := &fakeMux{}
	svc, _ := newTestService(t, mux)

	started, err := svc.Start(context.Background(), StartInput{WorkspaceID: testWorkspaceID})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if started.ActionID != "" || started.ActionName != "" || started.IsAgent {
		t.Fatalf("interactive identity = %+v", started)
	}
	if want := []string{"/bin/fish", "-l", "-i"}; !reflect.DeepEqual(mux.argv, want) {
		t.Fatalf("argv = %#v, want %#v", mux.argv, want)
	}
}

func TestStartRejectsMissingAndDisabledActions(t *testing.T) {
	mux := &fakeMux{}
	svc, st := newTestService(t, mux)
	ctx := context.Background()

	if _, err := svc.Start(ctx, StartInput{WorkspaceID: testWorkspaceID, ActionID: "act_missing"}); !errors.Is(err, ErrActionNotFound) {
		t.Fatalf("missing err = %v, want ErrActionNotFound", err)
	}
	action, err := st.CreateAction(ctx, store.Action{Name: "Off", Command: "off"})
	if err != nil {
		t.Fatalf("CreateAction: %v", err)
	}
	if _, err := svc.Start(ctx, StartInput{WorkspaceID: testWorkspaceID, ActionID: action.ID}); !errors.Is(err, ErrActionDisabled) {
		t.Fatalf("disabled err = %v, want ErrActionDisabled", err)
	}
}

func TestLaunchFailureDeletesProvisionalSession(t *testing.T) {
	mux := &fakeMux{startErr: errors.New("executable not found")}
	svc, st := newTestService(t, mux)
	ctx := context.Background()
	action, err := st.CreateAction(ctx, store.Action{Name: "Missing", Enabled: true, Command: "not-installed"})
	if err != nil {
		t.Fatalf("CreateAction: %v", err)
	}

	_, err = svc.Start(ctx, StartInput{WorkspaceID: testWorkspaceID, ActionID: action.ID})
	var launchErr *LaunchError
	if !errors.As(err, &launchErr) || launchErr.Code != CodeLaunchFailed || launchErr.SessionID == "" {
		t.Fatalf("Start err = %#v, want launch_failed", err)
	}
	records, listErr := st.ListAll(ctx, store.ListFilter{})
	if listErr != nil {
		t.Fatalf("ListAll: %v", listErr)
	}
	if len(records) != 0 {
		t.Fatalf("persistent sessions after failure = %+v", records)
	}
}

func TestNewIDUsesSessionPrefix(t *testing.T) {
	id, err := newID()
	if err != nil {
		t.Fatalf("newID: %v", err)
	}
	if len(id) != len("ses_")+26 || id[:4] != "ses_" {
		t.Fatalf("id %q, want ses_-prefixed public id", id)
	}
}

func TestStartSuccessCreatesOneLivePublicSession(t *testing.T) {
	t.Setenv("SHELL", "/usr/bin/zsh")
	mux := &fakeMux{}
	svc, st := newTestService(t, mux)
	action := createTestAction(t, st)
	started, err := svc.Start(context.Background(), StartInput{
		ActionID: action.ID, WorkspaceID: testWorkspaceID,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if started.Status != StatusLive || started.ID == "" {
		t.Fatalf("started = %+v", started)
	}
	if len(mux.started) != 1 || mux.started[0] != zmx.NameForID(started.ID) || mux.startCalls != 1 {
		t.Fatalf("start call = %v/%d", mux.started, mux.startCalls)
	}
	records, err := st.List(context.Background(), store.ListFilter{})
	if err != nil || len(records) != 1 || records[0].Status != store.StatusLive {
		t.Fatalf("public records = %+v err=%v", records, err)
	}
}

func TestStartNormalizesName(t *testing.T) {
	mux := &fakeMux{}
	svc, _ := newTestService(t, mux)
	started, err := svc.Start(context.Background(), StartInput{
		Name: "  Review  ", WorkspaceID: testWorkspaceID,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if started.Name != "Review" {
		t.Fatalf("name = %q, want trimmed", started.Name)
	}

	blank, err := svc.Start(context.Background(), StartInput{
		Name: " \t ", WorkspaceID: testWorkspaceID,
	})
	if err != nil {
		t.Fatalf("Start blank name: %v", err)
	}
	if blank.Name != "" {
		t.Fatalf("blank name = %q, want unnamed", blank.Name)
	}
}

func TestRenameLiveAndEndedSessions(t *testing.T) {
	mux := &fakeMux{}
	svc, st := newTestService(t, mux)
	ctx := context.Background()
	live, err := svc.Start(ctx, StartInput{Name: "Live", WorkspaceID: testWorkspaceID})
	if err != nil {
		t.Fatalf("Start live: %v", err)
	}
	ended, err := svc.Start(ctx, StartInput{Name: "Ended", WorkspaceID: testWorkspaceID})
	if err != nil {
		t.Fatalf("Start ended: %v", err)
	}
	if _, err := st.MarkEnded(ctx, ended.ID); err != nil {
		t.Fatalf("MarkEnded: %v", err)
	}

	renamedLive, err := svc.Rename(ctx, live.ID, "  Shared  ")
	if err != nil {
		t.Fatalf("Rename live: %v", err)
	}
	renamedEnded, err := svc.Rename(ctx, ended.ID, "Shared")
	if err != nil {
		t.Fatalf("Rename ended duplicate: %v", err)
	}
	if renamedLive.Name != "Shared" || renamedEnded.Name != "Shared" {
		t.Fatalf("names = %q, %q", renamedLive.Name, renamedEnded.Name)
	}
	if renamedLive.Workspace == nil || renamedLive.Project == nil {
		t.Fatalf("renamed live missing refs: %+v", renamedLive)
	}
	if _, err := svc.Rename(ctx, live.ID, "   "); !errors.Is(err, ErrInvalidSessionName) {
		t.Fatalf("blank err = %v, want ErrInvalidSessionName", err)
	}
	if _, err := svc.Rename(ctx, "ses_missing", "Name"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("missing err = %v, want ErrSessionNotFound", err)
	}
}

func TestStartPromotionLostRaceTerminatesLaunchedProcess(t *testing.T) {
	mux := &fakeMux{}
	svc, st := newTestService(t, mux)
	mux.startHook = func() {
		records, err := st.ListAll(context.Background(), store.ListFilter{})
		if err != nil || len(records) != 1 {
			t.Fatalf("records during start = %+v err=%v", records, err)
		}
		if err := st.DeleteStarting(context.Background(), records[0].ID); err != nil {
			t.Fatalf("DeleteStarting: %v", err)
		}
	}
	_, err := svc.Start(context.Background(), StartInput{WorkspaceID: testWorkspaceID})
	if !errors.Is(err, ErrSessionNotFound) || mux.terminateCalls != 1 {
		t.Fatalf("err=%v terminateCalls=%d", err, mux.terminateCalls)
	}
}

func TestStartConcurrentPromotionKeepsLiveSession(t *testing.T) {
	mux := &fakeMux{}
	svc, st := newTestService(t, mux)
	mux.startHook = func() {
		records, err := st.ListAll(context.Background(), store.ListFilter{})
		if err != nil || len(records) != 1 {
			t.Fatalf("records during start = %+v err=%v", records, err)
		}
		if _, err := st.PromoteToLive(context.Background(), records[0].ID); err != nil {
			t.Fatalf("PromoteToLive: %v", err)
		}
	}
	started, err := svc.Start(context.Background(), StartInput{WorkspaceID: testWorkspaceID})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if started.Status != StatusLive || mux.terminateCalls != 0 {
		t.Fatalf("started=%+v terminateCalls=%d", started, mux.terminateCalls)
	}
	assertStoredStatus(t, st, started.ID, store.StatusLive)
}

func TestStartPromotionPersistenceFailureTerminatesLaunchedProcess(t *testing.T) {
	mux := &fakeMux{}
	svc, st := newTestService(t, mux)
	mux.startHook = func() {
		if err := st.Close(); err != nil {
			t.Fatalf("Close store: %v", err)
		}
	}
	_, err := svc.Start(context.Background(), StartInput{WorkspaceID: testWorkspaceID})
	if err == nil || errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("err = %v, want a persistence failure", err)
	}
	if mux.terminateCalls != 1 {
		t.Fatalf("terminateCalls = %d, want the launched process terminated", mux.terminateCalls)
	}
}

func TestListAndReadExcludeProvisionalAndReconcileMissingLive(t *testing.T) {
	mux := &fakeMux{}
	svc, st := newTestService(t, mux)
	seedStarting(t, st, "ses_provisional")
	seedLive(t, st, "ses_live")
	mux.live = map[string]bool{zmx.NameForID("ses_live"): true}

	listed, err := svc.List(context.Background(), "", ListScope{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	assertSessionIDs(t, listed, []string{"ses_live"})
	if _, err := svc.Read(context.Background(), "ses_provisional"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Read provisional err = %v", err)
	}

	mux.live = nil
	read, err := svc.Read(context.Background(), "ses_live")
	if err != nil || read.Status != StatusEnded {
		t.Fatalf("Read stale = %+v err=%v", read, err)
	}
	mux.live = map[string]bool{zmx.NameForID("ses_live"): true}
	read, err = svc.Read(context.Background(), "ses_live")
	if err != nil || read.Status != StatusEnded {
		t.Fatalf("Read ended with reappeared zmx = %+v err=%v", read, err)
	}
}

func TestReadListAvailabilityFailureLeavesStoredState(t *testing.T) {
	mux := &fakeMux{listErr: errors.New("zmx unavailable")}
	svc, st := newTestService(t, mux)
	seedLive(t, st, "ses_live")
	read, err := svc.Read(context.Background(), "ses_live")
	if err != nil || read.Status != StatusLive {
		t.Fatalf("Read = %+v err=%v", read, err)
	}
	listed, err := svc.List(context.Background(), StatusLive, ListScope{})
	if err != nil || len(listed) != 1 {
		t.Fatalf("List = %+v err=%v", listed, err)
	}
	assertStoredStatus(t, st, "ses_live", store.StatusLive)
}

func TestReconcilePromotesDeletesAndEnds(t *testing.T) {
	mux := &fakeMux{live: map[string]bool{
		zmx.NameForID("ses_start_live"): true,
		zmx.NameForID("ses_live"):       true,
	}}
	svc, st := newTestService(t, mux)
	seedStarting(t, st, "ses_start_live")
	seedStarting(t, st, "ses_start_dead")
	seedLive(t, st, "ses_live")
	seedLive(t, st, "ses_dead")
	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	assertStoredStatus(t, st, "ses_start_live", store.StatusLive)
	if _, err := st.Get(context.Background(), "ses_start_dead"); !errors.Is(err, store.ErrSessionNotFound) {
		t.Fatalf("stale provisional err=%v", err)
	}
	assertStoredStatus(t, st, "ses_live", store.StatusLive)
	assertStoredStatus(t, st, "ses_dead", store.StatusEnded)
}

func TestReconcileListFailureChangesNothing(t *testing.T) {
	mux := &fakeMux{listErr: errors.New("zmx unavailable")}
	svc, st := newTestService(t, mux)
	seedStarting(t, st, "ses_start")
	seedLive(t, st, "ses_live")
	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	assertStoredStatus(t, st, "ses_start", store.StatusStarting)
	assertStoredStatus(t, st, "ses_live", store.StatusLive)
}

func TestStaleInteractionsPersistEndedAndReturnSessionEnded(t *testing.T) {
	operations := map[string]func(*Service) error{
		"send text": func(s *Service) error { return s.SendText(context.Background(), "ses_live", "hi") },
		"send key":  func(s *Service) error { return s.SendKey(context.Background(), "ses_live", "enter") },
		"attach": func(s *Service) error {
			_, err := s.Attach(context.Background(), "ses_live", 24, 80)
			return err
		},
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			svc, st := newTestService(t, &fakeMux{})
			seedLive(t, st, "ses_live")
			err := operation(svc)
			var endedErr *EndedError
			if !errors.As(err, &endedErr) || endedErr.SessionID != "ses_live" {
				t.Fatalf("err = %#v", err)
			}
			assertStoredStatus(t, st, "ses_live", store.StatusEnded)
		})
	}
}

func TestInteractionAvailabilityFailureDoesNotEnd(t *testing.T) {
	svc, st := newTestService(t, &fakeMux{listErr: errors.New("zmx unavailable")})
	seedLive(t, st, "ses_live")
	if err := svc.SendText(context.Background(), "ses_live", "hi"); err == nil || errors.Is(err, ErrSessionEnded) {
		t.Fatalf("SendText err = %v", err)
	}
	assertStoredStatus(t, st, "ses_live", store.StatusLive)
}

func TestDeleteLiveAndEnded(t *testing.T) {
	t.Run("live", func(t *testing.T) {
		mux := &fakeMux{live: map[string]bool{zmx.NameForID("ses_live"): true}}
		svc, st := newTestService(t, mux)
		seedLive(t, st, "ses_live")
		if err := svc.Delete(context.Background(), "ses_live"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if mux.terminateCalls != 1 {
			t.Fatalf("terminateCalls = %d", mux.terminateCalls)
		}
		assertMissing(t, st, "ses_live")
	})
	t.Run("ended", func(t *testing.T) {
		mux := &fakeMux{}
		svc, st := newTestService(t, mux)
		seedEnded(t, st, "ses_ended")
		if err := svc.Delete(context.Background(), "ses_ended"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if mux.terminateCalls != 0 {
			t.Fatalf("terminateCalls = %d", mux.terminateCalls)
		}
		assertMissing(t, st, "ses_ended")
	})
}

func TestDeleteTerminateFailureLeavesLiveRecord(t *testing.T) {
	mux := &fakeMux{
		live:         map[string]bool{zmx.NameForID("ses_live"): true},
		terminateErr: errors.New("zmx kill failed"),
	}
	svc, st := newTestService(t, mux)
	seedLive(t, st, "ses_live")
	if err := svc.Delete(context.Background(), "ses_live"); err == nil {
		t.Fatal("Delete succeeded")
	}
	assertStoredStatus(t, st, "ses_live", store.StatusLive)
}

func TestDeleteRecordFailureLeavesEndedAndCanBeRetried(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "atc.db")
	mux := &fakeMux{live: map[string]bool{zmx.NameForID("ses_live"): true}}
	svc, st := newTestServiceAtPath(t, mux, dbPath)
	seedLive(t, st, "ses_live")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		CREATE TRIGGER fail_ended_session_delete
		BEFORE DELETE ON sessions
		WHEN OLD.status = 'ended'
		BEGIN
			SELECT RAISE(FAIL, 'forced session delete failure');
		END`); err != nil {
		t.Fatalf("create delete failure trigger: %v", err)
	}

	if err := svc.Delete(context.Background(), "ses_live"); err == nil {
		t.Fatal("Delete succeeded")
	}
	assertStoredStatus(t, st, "ses_live", store.StatusEnded)
	if mux.terminateCalls != 1 || len(mux.live) != 0 {
		t.Fatalf("terminateCalls=%d sessions=%+v", mux.terminateCalls, mux.live)
	}

	if _, err := db.Exec(`DROP TRIGGER fail_ended_session_delete`); err != nil {
		t.Fatalf("drop delete failure trigger: %v", err)
	}
	if err := svc.Delete(context.Background(), "ses_live"); err != nil {
		t.Fatalf("retry Delete: %v", err)
	}
	if mux.terminateCalls != 1 {
		t.Fatalf("retry terminateCalls = %d, want 1", mux.terminateCalls)
	}
	assertMissing(t, st, "ses_live")
}

func TestDeleteProvisionalIsNotFound(t *testing.T) {
	svc, st := newTestService(t, &fakeMux{})
	seedStarting(t, st, "ses_start")
	if err := svc.Delete(context.Background(), "ses_start"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Delete err = %v", err)
	}
	assertStoredStatus(t, st, "ses_start", store.StatusStarting)
}

func TestInternalEndRemovesProvisionalAndEndsLive(t *testing.T) {
	mux := &fakeMux{live: map[string]bool{zmx.NameForID("ses_live"): true}}
	svc, st := newTestService(t, mux)
	seedStarting(t, st, "ses_start")
	seedLive(t, st, "ses_live")
	if err := svc.End(context.Background(), "ses_start"); err != nil {
		t.Fatalf("End provisional: %v", err)
	}
	assertMissing(t, st, "ses_start")
	if err := svc.End(context.Background(), "ses_live"); err != nil {
		t.Fatalf("End live: %v", err)
	}
	assertStoredStatus(t, st, "ses_live", store.StatusEnded)
}

func TestInternalEndTreatsMissingRecordAsEnded(t *testing.T) {
	svc, _ := newTestService(t, &fakeMux{})
	if err := svc.End(context.Background(), "ses_gone"); err != nil {
		t.Fatalf("End missing session: %v", err)
	}
}

func TestStatusVocabulary(t *testing.T) {
	if !validStatus(StatusLive) || !validStatus(StatusEnded) || validStatus(Status("starting")) || validStatus(Status("running")) {
		t.Fatal("unexpected public status vocabulary")
	}
}

func TestKeyRegistry(t *testing.T) {
	want := map[string][]byte{"enter": {0x0D}, "ctrl-c": {0x03}, "escape": {0x1B}}
	for name, bytes := range want {
		got, ok := keyBytes(name)
		if !ok || !reflect.DeepEqual(got, bytes) {
			t.Fatalf("key %q = %v ok=%v", name, got, ok)
		}
	}
}

func createTestAction(t *testing.T, st *store.Store) store.Action {
	t.Helper()
	action, err := st.CreateAction(context.Background(), store.Action{
		Name: "Codex", Enabled: true, Command: "codex", IsAgent: true,
	})
	if err != nil {
		t.Fatalf("CreateAction: %v", err)
	}
	return action
}

func seedStarting(t *testing.T, st *store.Store, id string) {
	t.Helper()
	if _, err := st.CreateStarting(context.Background(), store.CreateSessionInput{
		ID: id, ActionID: "act_test", ActionName: "Codex", IsAgent: true,
		WorkingDir: "/work", WorkspaceID: testWorkspaceID,
	}); err != nil {
		t.Fatalf("CreateStarting(%s): %v", id, err)
	}
}

func seedLive(t *testing.T, st *store.Store, id string) {
	t.Helper()
	seedStarting(t, st, id)
	if _, err := st.PromoteToLive(context.Background(), id); err != nil {
		t.Fatalf("PromoteToLive(%s): %v", id, err)
	}
}

func seedEnded(t *testing.T, st *store.Store, id string) {
	t.Helper()
	seedLive(t, st, id)
	if _, err := st.MarkEnded(context.Background(), id); err != nil {
		t.Fatalf("MarkEnded(%s): %v", id, err)
	}
}

func assertStoredStatus(t *testing.T, st *store.Store, id string, want store.RecordStatus) {
	t.Helper()
	got, err := st.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get(%s): %v", id, err)
	}
	if got.Status != want {
		t.Fatalf("status for %s = %s, want %s", id, got.Status, want)
	}
}

func assertMissing(t *testing.T, st *store.Store, id string) {
	t.Helper()
	if _, err := st.Get(context.Background(), id); !errors.Is(err, store.ErrSessionNotFound) {
		t.Fatalf("Get(%s) err = %v, want not found", id, err)
	}
}

func assertSessionIDs(t *testing.T, sessions []Session, want []string) {
	t.Helper()
	got := make([]string, len(sessions))
	for i, session := range sessions {
		got[i] = session.ID
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("session ids = %v, want %v", got, want)
	}
}
