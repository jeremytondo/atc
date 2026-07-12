package session

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jeremytondo/atc/internal/project"
	"github.com/jeremytondo/atc/internal/store"
	"github.com/jeremytondo/atc/internal/workspace"
	"github.com/jeremytondo/atc/internal/zmx"
)

// fakeMux is a faked Multiplexer that records calls and serves a fixed list.
type fakeMux struct {
	sessions []zmx.Session
	listErr  error

	startName, startDir string
	startArgv           []string
	startErr            error
	startCalls          int
	// startHook runs after a successful launch, before Start returns, so
	// tests can interleave a concurrent settle with an in-flight Start.
	startHook func()

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
	f.startName, f.startDir = name, dir
	f.startArgv = append([]string(nil), argv...)
	if f.startErr != nil {
		return f.startErr
	}
	f.sessions = append(f.sessions, zmx.Session{Name: name, StartDir: dir, Cmd: strings.Join(argv, " ")})
	if f.startHook != nil {
		f.startHook()
	}
	return nil
}

func (f *fakeMux) Send(_ context.Context, name string, payload []byte) error {
	f.sendCalls++
	f.sendName = name
	f.sendPayload = payload
	return nil
}

func (f *fakeMux) Attach(_ context.Context, name string, rows, cols uint16) (zmx.PTY, error) {
	f.attachName = name
	f.attachRows = rows
	f.attachCols = cols
	f.attachCalls++
	return nil, nil
}

func (f *fakeMux) List(context.Context) ([]zmx.Session, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.sessions, nil
}

func (f *fakeMux) Terminate(_ context.Context, name string) error {
	f.terminateName = name
	f.terminateCalls++
	if f.terminateErr != nil {
		return f.terminateErr
	}
	for i, s := range f.sessions {
		if s.Name == name {
			f.sessions = append(f.sessions[:i], f.sessions[i+1:]...)
			break
		}
	}
	return nil
}

// fakeResolver is a faked WorkspaceResolver recording resolve calls.
type fakeResolver struct {
	dir    string
	err    error
	calls  int
	lastID string
}

func (f *fakeResolver) ResolveForStart(_ context.Context, id string) (string, error) {
	f.calls++
	f.lastID = id
	if f.err != nil {
		return "", f.err
	}
	return f.dir, nil
}

// testActions is the registry used across service tests.
func testActions() ActionRegistry {
	return ActionRegistry{
		"claude": {Command: "claude"},
		"codex": {
			Command: "codex",
			Prompt:  &PromptSpec{Flag: "--prompt"},
			Params: map[string]ParamSpec{
				"model":     {Type: "enum", Values: []string{"gpt-5", "gpt-5-codex"}, Default: "gpt-5-codex", Flag: "--model"},
				"full-auto": {Type: "bool", Flag: "--full-auto"},
			},
		},
	}
}

// testWorkspaceID is the workspace every test session hangs off; newService
// seeds its project/workspace rows so the sessions FK holds.
const testWorkspaceID = "wsp_test"

func newService(t *testing.T, mux Multiplexer) (*Service, *store.Store) {
	t.Helper()
	svc, st, _ := newServiceWithResolver(t, mux, &fakeResolver{dir: t.TempDir()})
	return svc, st
}

func newServiceWithResolver(t *testing.T, mux Multiplexer, resolver *fakeResolver) (*Service, *store.Store, *fakeResolver) {
	t.Helper()
	svc, st := newServiceWithActions(t, mux, testActions(), resolver)
	return svc, st, resolver
}

func newServiceWithActions(t *testing.T, mux Multiplexer, actions ActionLoader, resolver *fakeResolver) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/atc.db")
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if _, err := st.CreateProject(ctx, store.CreateProjectInput{ID: "prj_test", Name: "Test", WorkingDir: "/work"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := st.CreateWorkspace(ctx, store.CreateWorkspaceInput{ID: testWorkspaceID, ProjectID: "prj_test", Name: "Test workspace"}); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	return NewService(st, mux, actions, testEnvironments(), resolver, nil), st
}

func testEnvironments() EnvironmentRegistry {
	return EnvironmentRegistry{
		"host-login-shell": {Kind: EnvironmentKindHostLoginShell},
	}
}

func TestNewIDUsesSessionPrefix(t *testing.T) {
	id, err := newID()
	if err != nil {
		t.Fatalf("newID: %v", err)
	}
	// Encoding shape is covered by internal/publicid tests.
	if len(id) != len("ses_")+26 || id[:4] != "ses_" {
		t.Fatalf("id %q, want ses_-prefixed public id", id)
	}
}

func TestKeyRegistry(t *testing.T) {
	want := map[string][]byte{
		"enter":  {0x0D},
		"ctrl-c": {0x03},
		"escape": {0x1B},
	}
	for name, bytes := range want {
		got, ok := keyBytes(name)
		if !ok || !reflect.DeepEqual(got, bytes) {
			t.Fatalf("key %q = %v ok=%v, want %v", name, got, ok, bytes)
		}
	}
	if _, ok := keyBytes("tab"); ok {
		t.Fatal("unexpected key 'tab' in registry")
	}
}

func TestStartCreatesRecordLaunchesDerivedNameAndReturnsRunning(t *testing.T) {
	t.Setenv("SHELL", "/usr/bin/zsh")
	mux := &fakeMux{}
	workDir := t.TempDir()
	svc, st, resolver := newServiceWithResolver(t, mux, &fakeResolver{dir: workDir})

	got, err := svc.Start(context.Background(), StartInput{
		Action:      "codex",
		Params:      map[string]any{"full-auto": true},
		WorkspaceID: testWorkspaceID,
		Prompt:      "review this change",
		Name:        "Review",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if resolver.calls != 1 || resolver.lastID != testWorkspaceID {
		t.Fatalf("resolver calls = %d lastID = %q", resolver.calls, resolver.lastID)
	}
	if got.ID == "" || got.Status != StatusRunning || !got.Attachable {
		t.Fatalf("started session = %+v", got)
	}
	if got.Name != "Review" || got.Action != "codex" || got.Environment != "host-login-shell" || got.WorkingDir != workDir || got.Prompt != "review this change" {
		t.Fatalf("started metadata = %+v", got)
	}
	if got.WorkspaceID != testWorkspaceID || got.Workspace == nil || got.Workspace.Name != "Test workspace" {
		t.Fatalf("workspace ref = %+v", got.Workspace)
	}
	if got.Project == nil || got.Project.ID != "prj_test" || got.Project.Name != "Test" {
		t.Fatalf("project ref = %+v", got.Project)
	}
	if got.Params["model"] != "gpt-5-codex" || got.Params["full-auto"] != true {
		t.Fatalf("accepted params = %#v", got.Params)
	}
	wantName := zmx.NameForID(got.ID)
	if mux.startCalls != 1 || mux.startName != wantName || mux.startDir != workDir {
		t.Fatalf("wrapper start = %+v, want name %q dir %q", mux, wantName, workDir)
	}
	wantArgv := []string{loginShell(), "-l", "-i", "-c", "codex --full-auto --model gpt-5-codex --prompt 'review this change'"}
	if !reflect.DeepEqual(mux.startArgv, wantArgv) {
		t.Fatalf("argv = %#v, want %#v", mux.startArgv, wantArgv)
	}
	stored, err := st.Get(context.Background(), got.ID)
	if err != nil {
		t.Fatalf("Get stored: %v", err)
	}
	if stored.Status != store.StatusRunning || stored.Action != "codex" || stored.Environment != "host-login-shell" || string(stored.Params) != `{"full-auto":true,"model":"gpt-5-codex"}` {
		t.Fatalf("stored = %+v params=%s", stored, stored.Params)
	}
	if stored.WorkspaceID != testWorkspaceID || stored.WorkingDir != workDir {
		t.Fatalf("stored = %+v, want workspace id and dir snapshot", stored)
	}
}

func TestStartWithoutActionLaunchesInteractiveShell(t *testing.T) {
	t.Setenv("SHELL", "/usr/bin/zsh")
	mux := &fakeMux{}
	workDir := t.TempDir()
	svc, st, _ := newServiceWithResolver(t, mux, &fakeResolver{dir: workDir})

	got, err := svc.Start(context.Background(), StartInput{WorkspaceID: testWorkspaceID})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got.Action != "" || got.Status != StatusRunning {
		t.Fatalf("started = %+v, want interactive shell running", got)
	}
	// The interactive shell is the environment wrapper minus -c: the user's
	// shell itself, not a command run through it.
	wantArgv := []string{"/usr/bin/zsh", "-l", "-i"}
	if !reflect.DeepEqual(mux.startArgv, wantArgv) {
		t.Fatalf("argv = %#v, want %#v", mux.startArgv, wantArgv)
	}
	stored, err := st.Get(context.Background(), got.ID)
	if err != nil {
		t.Fatalf("Get stored: %v", err)
	}
	if stored.Action != "" {
		t.Fatalf("stored action = %q, want empty", stored.Action)
	}
}

func TestInteractiveShellFallsBackToBinSh(t *testing.T) {
	t.Setenv("SHELL", "")
	mux := &fakeMux{}
	svc, _, _ := newServiceWithResolver(t, mux, &fakeResolver{dir: t.TempDir()})

	if _, err := svc.Start(context.Background(), StartInput{WorkspaceID: testWorkspaceID}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	wantArgv := []string{"/bin/sh", "-l", "-i"}
	if !reflect.DeepEqual(mux.startArgv, wantArgv) {
		t.Fatalf("argv = %#v, want %#v", mux.startArgv, wantArgv)
	}
}

func TestStartRejectsValidationBeforeCreatingRecord(t *testing.T) {
	tests := []struct {
		name        string
		input       StartInput
		resolverErr error
		wantErr     error
	}{
		{name: "unknown action", input: StartInput{Action: "ghost", WorkspaceID: testWorkspaceID}, wantErr: ErrUnknownAction},
		{name: "unknown environment", input: StartInput{Action: "codex", Environment: "ghost", WorkspaceID: testWorkspaceID}, wantErr: ErrUnknownEnvironment},
		{name: "invalid param", input: StartInput{Action: "codex", WorkspaceID: testWorkspaceID, Params: map[string]any{"model": "gpt-4"}}, wantErr: ErrInvalidParam},
		{name: "unsupported prompt", input: StartInput{Action: "claude", WorkspaceID: testWorkspaceID, Prompt: "do it"}, wantErr: ErrInvalidParam},
		{name: "params without action", input: StartInput{WorkspaceID: testWorkspaceID, Params: map[string]any{"model": "gpt-5"}}, wantErr: ErrInvalidParam},
		{name: "prompt without action", input: StartInput{WorkspaceID: testWorkspaceID, Prompt: "do it"}, wantErr: ErrInvalidParam},
		{name: "missing workspace id", input: StartInput{Action: "codex"}, wantErr: workspace.ErrInvalidWorkspace},
		{name: "unknown workspace", input: StartInput{Action: "codex", WorkspaceID: "wsp_ghost"}, resolverErr: workspace.ErrWorkspaceNotFound, wantErr: workspace.ErrWorkspaceNotFound},
		{name: "archived workspace", input: StartInput{Action: "codex", WorkspaceID: testWorkspaceID}, resolverErr: workspace.ErrWorkspaceArchived, wantErr: workspace.ErrWorkspaceArchived},
		{name: "archived project", input: StartInput{Action: "codex", WorkspaceID: testWorkspaceID}, resolverErr: project.ErrProjectArchived, wantErr: project.ErrProjectArchived},
		{name: "vanished directory", input: StartInput{Action: "codex", WorkspaceID: testWorkspaceID}, resolverErr: project.ErrInvalidWorkingDir, wantErr: ErrInvalidWorkingDir},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := &fakeMux{}
			resolver := &fakeResolver{dir: t.TempDir(), err: tt.resolverErr}
			svc, st, _ := newServiceWithResolver(t, mux, resolver)
			_, err := svc.Start(context.Background(), tt.input)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if mux.startCalls != 0 {
				t.Fatal("wrapper Start called for invalid start")
			}
			list, err := st.List(context.Background(), store.ListFilter{IncludeArchived: true})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(list) != 0 {
				t.Fatalf("records = %+v, want none", list)
			}
		})
	}
}

func TestStartGuardRejectsWorkspaceArchivedBetweenResolveAndInsert(t *testing.T) {
	// The resolver passes but the workspace is archived in the store — the
	// guarded insert must reject and leave no record.
	mux := &fakeMux{}
	svc, st, _ := newServiceWithResolver(t, mux, &fakeResolver{dir: t.TempDir()})
	if _, err := st.ArchiveWorkspace(context.Background(), testWorkspaceID); err != nil {
		t.Fatalf("ArchiveWorkspace: %v", err)
	}

	_, err := svc.Start(context.Background(), StartInput{Action: "codex", WorkspaceID: testWorkspaceID})
	if !errors.Is(err, workspace.ErrWorkspaceArchived) {
		t.Fatalf("err = %v, want ErrWorkspaceArchived", err)
	}
	if mux.startCalls != 0 {
		t.Fatal("wrapper Start called despite archived workspace")
	}
}

func TestListFiltersByWorkspaceAndProject(t *testing.T) {
	mux := &fakeMux{}
	svc, st := newService(t, mux)
	ctx := context.Background()
	if _, err := st.CreateProject(ctx, store.CreateProjectInput{ID: "prj_other", Name: "Other", WorkingDir: "/work"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := st.CreateWorkspace(ctx, store.CreateWorkspaceInput{ID: "wsp_other", ProjectID: "prj_other", Name: "Other"}); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	scoped, err := svc.Start(ctx, StartInput{Action: "codex", WorkspaceID: testWorkspaceID})
	if err != nil {
		t.Fatalf("Start scoped: %v", err)
	}
	other, err := svc.Start(ctx, StartInput{Action: "codex", WorkspaceID: "wsp_other"})
	if err != nil {
		t.Fatalf("Start other: %v", err)
	}

	filtered, err := svc.List(ctx, true, "", ListScope{WorkspaceID: testWorkspaceID})
	if err != nil {
		t.Fatalf("List workspace filtered: %v", err)
	}
	assertSessionIDs(t, filtered, []string{scoped.ID})
	byProject, err := svc.List(ctx, true, "", ListScope{ProjectID: "prj_other"})
	if err != nil {
		t.Fatalf("List project filtered: %v", err)
	}
	assertSessionIDs(t, byProject, []string{other.ID})
	all, err := svc.List(ctx, true, "", ListScope{})
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("all sessions = %+v, want 2", all)
	}
}

func TestStartLaunchFailureRecordsFailedAndReturnsSessionID(t *testing.T) {
	mux := &fakeMux{startErr: errors.New("raw zmx error")}
	svc, st := newService(t, mux)

	_, err := svc.Start(context.Background(), StartInput{Action: "claude", WorkspaceID: testWorkspaceID})
	var launchErr *LaunchError
	if !errors.As(err, &launchErr) {
		t.Fatalf("err = %v, want LaunchError", err)
	}
	if launchErr.SessionID == "" || launchErr.FailureCode != CodeLaunchFailed || launchErr.Error() != launchFailedReason {
		t.Fatalf("launchErr = %+v", launchErr)
	}
	stored, err := st.Get(context.Background(), launchErr.SessionID)
	if err != nil {
		t.Fatalf("Get failed record: %v", err)
	}
	if stored.Status != StatusFailed || stored.FailureReason != launchFailedReason || stored.FailureCode != CodeLaunchFailed {
		t.Fatalf("stored failed = %+v", stored)
	}
}

func TestListAndReadReconcileFromLiveness(t *testing.T) {
	mux := &fakeMux{}
	svc, st := newService(t, mux)
	seedStarting(t, st, "ses_start_live")
	seedStarting(t, st, "ses_start_dead")
	seedRunning(t, st, "ses_run_live")
	seedRunning(t, st, "ses_run_dead")
	mux.sessions = []zmx.Session{
		{Name: zmx.NameForID("ses_start_live")},
		{Name: zmx.NameForID("ses_run_live")},
	}

	got, err := svc.List(context.Background(), true, "", ListScope{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	statuses := map[string]Status{}
	attachable := map[string]bool{}
	for _, s := range got {
		statuses[s.ID] = s.Status
		attachable[s.ID] = s.Attachable
	}
	if statuses["ses_start_live"] != StatusStarting || attachable["ses_start_live"] {
		t.Fatalf("start live status/attachable = %s/%v", statuses["ses_start_live"], attachable["ses_start_live"])
	}
	if statuses["ses_start_dead"] != StatusStarting || attachable["ses_start_dead"] {
		t.Fatalf("start dead status/attachable = %s/%v", statuses["ses_start_dead"], attachable["ses_start_dead"])
	}
	if statuses["ses_run_live"] != StatusRunning || !attachable["ses_run_live"] {
		t.Fatalf("run live status/attachable = %s/%v", statuses["ses_run_live"], attachable["ses_run_live"])
	}
	if statuses["ses_run_dead"] != StatusTerminated || attachable["ses_run_dead"] {
		t.Fatalf("run dead status/attachable = %s/%v", statuses["ses_run_dead"], attachable["ses_run_dead"])
	}

	detail, err := svc.Read(context.Background(), "ses_run_live")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if detail.Status != StatusRunning || !detail.Attachable {
		t.Fatalf("detail = %+v", detail)
	}
	startingDetail, err := svc.Read(context.Background(), "ses_start_live")
	if err != nil {
		t.Fatalf("Read starting: %v", err)
	}
	if startingDetail.Status != StatusStarting || startingDetail.Attachable {
		t.Fatalf("starting detail = %+v", startingDetail)
	}

	running, err := svc.List(context.Background(), true, StatusRunning, ListScope{})
	if err != nil {
		t.Fatalf("List running: %v", err)
	}
	assertSessionIDs(t, running, []string{"ses_run_live"})

	failed, err := svc.List(context.Background(), true, StatusFailed, ListScope{})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	assertSessionIDs(t, failed, []string{})

	starting, err := svc.List(context.Background(), true, StatusStarting, ListScope{})
	if err != nil {
		t.Fatalf("List starting: %v", err)
	}
	assertSessionIDs(t, starting, []string{"ses_start_dead", "ses_start_live"})
	assertStartingClean(t, st, "ses_start_live")
	assertStartingClean(t, st, "ses_start_dead")
}

func TestReadReconcileDoesNotClobberInFlightStart(t *testing.T) {
	mux := &fakeMux{}
	svc, st := newService(t, mux)
	seedStarting(t, st, "ses_inflight")

	listed, err := svc.List(context.Background(), true, "", ListScope{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 1 || listed[0].Status != StatusStarting || listed[0].Attachable {
		t.Fatalf("listed = %+v", listed)
	}
	detail, err := svc.Read(context.Background(), "ses_inflight")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if detail.Status != StatusStarting || detail.Attachable {
		t.Fatalf("detail = %+v", detail)
	}
	assertStartingClean(t, st, "ses_inflight")

	running, err := st.MarkRunning(context.Background(), "ses_inflight")
	if err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if running.Status != StatusRunning || running.FailureCode != "" || running.FailureReason != "" {
		t.Fatalf("running = %+v", running)
	}
}

func TestLivenessFailureLeavesStoredStatusUnmutated(t *testing.T) {
	mux := &fakeMux{listErr: errors.New("zmx unavailable")}
	svc, st := newService(t, mux)
	seedRunning(t, st, "ses_run")

	got, err := svc.List(context.Background(), false, "", ListScope{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Status != StatusRunning || got[0].Attachable {
		t.Fatalf("list = %+v", got)
	}
	stored, err := st.Get(context.Background(), "ses_run")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Status != StatusRunning {
		t.Fatalf("stored status = %s, want running", stored.Status)
	}
}

func TestReconcileUpdatesPersistedStatuses(t *testing.T) {
	mux := &fakeMux{}
	svc, st := newService(t, mux)
	seedStarting(t, st, "ses_start_live")
	seedStarting(t, st, "ses_start_dead")
	seedRunning(t, st, "ses_run_live")
	seedRunning(t, st, "ses_run_dead")
	mux.sessions = []zmx.Session{
		{Name: zmx.NameForID("ses_start_live")},
		{Name: zmx.NameForID("ses_run_live")},
	}

	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	assertStoredStatus(t, st, "ses_start_live", StatusRunning)
	assertStoredStatus(t, st, "ses_start_dead", StatusFailed)
	assertStoredStatus(t, st, "ses_run_live", StatusRunning)
	assertStoredStatus(t, st, "ses_run_dead", StatusTerminated)
	dead, err := st.Get(context.Background(), "ses_run_dead")
	if err != nil {
		t.Fatalf("Get ses_run_dead: %v", err)
	}
	if dead.TerminatedAt == nil {
		t.Fatal("ses_run_dead terminatedAt = nil")
	}
	startDead, err := st.Get(context.Background(), "ses_start_dead")
	if err != nil {
		t.Fatalf("Get ses_start_dead: %v", err)
	}
	if startDead.FailureCode != CodeLaunchFailed || startDead.FailureReason != startupIncompleteReason {
		t.Fatalf("start dead failure = %q/%q", startDead.FailureCode, startDead.FailureReason)
	}
}

func TestReconcileLivenessFailureIsNoOp(t *testing.T) {
	mux := &fakeMux{listErr: errors.New("zmx unavailable")}
	svc, st := newService(t, mux)
	seedStarting(t, st, "ses_start")
	seedRunning(t, st, "ses_run")

	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	assertStoredStatus(t, st, "ses_start", StatusStarting)
	assertStoredStatus(t, st, "ses_run", StatusRunning)
}

func TestSendTextAndKeyRequireLiveSession(t *testing.T) {
	mux := &fakeMux{}
	svc, st := newService(t, mux)
	seedRunning(t, st, "ses_live")
	mux.sessions = []zmx.Session{{Name: zmx.NameForID("ses_live")}}

	if err := svc.SendText(context.Background(), "ses_live", "implement the parser"); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if mux.sendName != zmx.NameForID("ses_live") || string(mux.sendPayload) != "implement the parser" {
		t.Fatalf("send = %q payload=%q", mux.sendName, mux.sendPayload)
	}

	if err := svc.SendKey(context.Background(), "ses_live", "enter"); err != nil {
		t.Fatalf("SendKey: %v", err)
	}
	if !reflect.DeepEqual(mux.sendPayload, []byte{0x0D}) {
		t.Fatalf("payload = %v, want enter bytes", mux.sendPayload)
	}

	if err := svc.SendKey(context.Background(), "ses_live", "f1"); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("err = %v, want ErrUnknownKey", err)
	}

	seedRunning(t, st, "ses_dead")
	if err := svc.SendText(context.Background(), "ses_dead", "hi"); !errors.Is(err, ErrSessionNotLive) {
		t.Fatalf("err = %v, want ErrSessionNotLive", err)
	}
	stored, err := st.Get(context.Background(), "ses_dead")
	if err != nil {
		t.Fatalf("Get dead: %v", err)
	}
	if stored.Status != StatusTerminated {
		t.Fatalf("dead status = %s, want terminated", stored.Status)
	}
}

func TestStartingOperationsDoNotResolveInFlightSession(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Service, string) error
	}{
		{
			name: "send text",
			run: func(svc *Service, id string) error {
				return svc.SendText(context.Background(), id, "hello")
			},
		},
		{
			name: "archive",
			run: func(svc *Service, id string) error {
				_, err := svc.Archive(context.Background(), id)
				return err
			},
		},
	}

	for _, live := range []bool{false, true} {
		liveName := "not-live"
		if live {
			liveName = "live"
		}
		for _, tt := range tests {
			t.Run(liveName+"/"+tt.name, func(t *testing.T) {
				mux := &fakeMux{}
				svc, st := newService(t, mux)
				seedStarting(t, st, "ses_inflight")
				if live {
					mux.sessions = []zmx.Session{{Name: zmx.NameForID("ses_inflight")}}
				}

				if err := tt.run(svc, "ses_inflight"); !errors.Is(err, ErrSessionNotLive) {
					t.Fatalf("%s err = %v, want ErrSessionNotLive", tt.name, err)
				}
				assertStartingClean(t, st, "ses_inflight")
				if mux.sendCalls != 0 {
					t.Fatalf("sendCalls = %d, want 0", mux.sendCalls)
				}
				if mux.terminateCalls != 0 {
					t.Fatalf("terminateCalls = %d, want 0", mux.terminateCalls)
				}
			})
		}
	}
}

// Starting sessions are active per the lifecycle contract: terminate and
// delete settle them instead of rejecting them, killing the multiplexer
// terminal when one is already live.
func TestTerminateAndDeleteSettleStartingSession(t *testing.T) {
	for _, live := range []bool{false, true} {
		liveName := "not-live"
		if live {
			liveName = "live"
		}
		t.Run(liveName+"/terminate", func(t *testing.T) {
			mux := &fakeMux{}
			svc, st := newService(t, mux)
			seedStarting(t, st, "ses_inflight")
			if live {
				mux.sessions = []zmx.Session{{Name: zmx.NameForID("ses_inflight")}}
			}

			terminated, err := svc.Terminate(context.Background(), "ses_inflight")
			if err != nil {
				t.Fatalf("Terminate: %v", err)
			}
			if terminated.Status != StatusTerminated || terminated.TerminatedAt == nil {
				t.Fatalf("terminated = %+v, want terminated", terminated)
			}
			wantTerminateCalls := 0
			if live {
				wantTerminateCalls = 1
			}
			if mux.terminateCalls != wantTerminateCalls {
				t.Fatalf("terminateCalls = %d, want %d", mux.terminateCalls, wantTerminateCalls)
			}
		})
		t.Run(liveName+"/delete", func(t *testing.T) {
			mux := &fakeMux{}
			svc, st := newService(t, mux)
			seedStarting(t, st, "ses_inflight")
			if live {
				mux.sessions = []zmx.Session{{Name: zmx.NameForID("ses_inflight")}}
			}

			if err := svc.Delete(context.Background(), "ses_inflight"); err != nil {
				t.Fatalf("Delete: %v", err)
			}
			if _, err := st.Get(context.Background(), "ses_inflight"); !errors.Is(err, store.ErrSessionNotFound) {
				t.Fatalf("Get after delete err = %v, want ErrSessionNotFound", err)
			}
		})
	}
}

// A Terminate that settles the record between the multiplexer launch and
// MarkRunning must win: Start tears down its own launch instead of
// resurrecting the session.
func TestStartTearsDownLaunchSettledConcurrently(t *testing.T) {
	mux := &fakeMux{}
	svc, st := newService(t, mux)
	mux.startHook = func() {
		records, err := st.List(context.Background(), store.ListFilter{})
		if err != nil || len(records) != 1 {
			t.Fatalf("List during start: %v (%d records)", err, len(records))
		}
		if _, err := st.MarkTerminated(context.Background(), records[0].ID); err != nil {
			t.Fatalf("MarkTerminated during start: %v", err)
		}
	}

	_, err := svc.Start(context.Background(), StartInput{Action: "claude", WorkspaceID: testWorkspaceID})
	if !errors.Is(err, ErrSessionNotLive) {
		t.Fatalf("Start err = %v, want ErrSessionNotLive", err)
	}
	if mux.terminateCalls != 1 {
		t.Fatalf("terminateCalls = %d, want 1", mux.terminateCalls)
	}
	records, err := st.List(context.Background(), store.ListFilter{})
	if err != nil || len(records) != 1 {
		t.Fatalf("List: %v (%d records)", err, len(records))
	}
	if records[0].Status != StatusTerminated {
		t.Fatalf("status = %s, want terminated", records[0].Status)
	}
}

// fakeActionLoader returns successive registries on successive loads so tests
// can delete an action between Start's command resolution and its post-insert
// re-resolution.
type fakeActionLoader struct {
	registries []ActionRegistry
	calls      int
}

func (f *fakeActionLoader) Load(ctx context.Context) (ActionRegistry, error) {
	i := f.calls
	f.calls++
	if i >= len(f.registries) {
		i = len(f.registries) - 1
	}
	return f.registries[i].Load(ctx)
}

func TestStartFailsWhenActionDeletedBeforeLaunch(t *testing.T) {
	mux := &fakeMux{}
	loader := &fakeActionLoader{registries: []ActionRegistry{testActions(), {}}}
	svc, st := newServiceWithActions(t, mux, loader, &fakeResolver{dir: t.TempDir()})

	_, err := svc.Start(context.Background(), StartInput{Action: "claude", WorkspaceID: testWorkspaceID})
	if !errors.Is(err, ErrUnknownAction) {
		t.Fatalf("Start err = %v, want ErrUnknownAction", err)
	}
	if mux.startCalls != 0 {
		t.Fatalf("startCalls = %d, want 0", mux.startCalls)
	}
	records, listErr := st.List(context.Background(), store.ListFilter{})
	if listErr != nil || len(records) != 1 {
		t.Fatalf("List: %v (%d records)", listErr, len(records))
	}
	if records[0].Status != StatusFailed || records[0].FailureCode != CodeActionRemoved {
		t.Fatalf("record = status %s code %s, want failed/%s", records[0].Status, records[0].FailureCode, CodeActionRemoved)
	}
}

func TestAttachExistingSessionCallsMux(t *testing.T) {
	mux := &fakeMux{}
	svc, st := newService(t, mux)
	seedRunning(t, st, "ses_live")
	mux.sessions = []zmx.Session{{Name: zmx.NameForID("ses_live")}}

	_, err := svc.Attach(context.Background(), "ses_live", 40, 120)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if mux.attachCalls != 1 || mux.attachName != zmx.NameForID("ses_live") {
		t.Fatalf("attachCalls = %d, attachName = %q", mux.attachCalls, mux.attachName)
	}
	if mux.attachRows != 40 || mux.attachCols != 120 {
		t.Fatalf("attach size = %dx%d, want 40x120", mux.attachRows, mux.attachCols)
	}
}

func TestTerminateIsIdempotentAndDisablesInput(t *testing.T) {
	mux := &fakeMux{}
	svc, st := newService(t, mux)
	seedRunning(t, st, "ses_live")
	mux.sessions = []zmx.Session{{Name: zmx.NameForID("ses_live")}}

	terminated, err := svc.Terminate(context.Background(), "ses_live")
	if err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	if terminated.Status != StatusTerminated || terminated.TerminatedAt == nil || terminated.Attachable {
		t.Fatalf("terminated = %+v", terminated)
	}
	if mux.terminateCalls != 1 || mux.terminateName != zmx.NameForID("ses_live") {
		t.Fatalf("terminate = %d/%q", mux.terminateCalls, mux.terminateName)
	}

	again, err := svc.Terminate(context.Background(), "ses_live")
	if err != nil {
		t.Fatalf("Terminate again: %v", err)
	}
	if again.Status != StatusTerminated || mux.terminateCalls != 1 {
		t.Fatalf("again = %+v terminateCalls=%d", again, mux.terminateCalls)
	}
	if err := svc.SendText(context.Background(), "ses_live", "hi"); !errors.Is(err, ErrSessionNotLive) {
		t.Fatalf("SendText err = %v, want ErrSessionNotLive", err)
	}
}

func TestArchiveRejectsLiveAndHidesArchivedFromDefaultList(t *testing.T) {
	mux := &fakeMux{}
	svc, st := newService(t, mux)
	seedRunning(t, st, "ses_live")
	seedRunning(t, st, "ses_dead")
	mux.sessions = []zmx.Session{{Name: zmx.NameForID("ses_live")}}

	if _, err := svc.Archive(context.Background(), "ses_live"); !errors.Is(err, ErrSessionLive) {
		t.Fatalf("err = %v, want ErrSessionLive", err)
	}
	archived, err := svc.Archive(context.Background(), "ses_dead")
	if err != nil {
		t.Fatalf("Archive dead: %v", err)
	}
	if archived.Status != StatusTerminated || archived.ArchivedAt == nil {
		t.Fatalf("archived = %+v", archived)
	}
	list, err := svc.List(context.Background(), false, "", ListScope{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != "ses_live" {
		t.Fatalf("default list = %+v, want only live session", list)
	}
}

func TestUnarchiveReturnsSessionToDefaultList(t *testing.T) {
	mux := &fakeMux{}
	svc, st := newService(t, mux)
	seedRunning(t, st, "ses_done")

	if _, err := svc.Archive(context.Background(), "ses_done"); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	unarchived, err := svc.Unarchive(context.Background(), "ses_done")
	if err != nil {
		t.Fatalf("Unarchive: %v", err)
	}
	if unarchived.ArchivedAt != nil {
		t.Fatalf("unarchived = %+v", unarchived)
	}
	// Unarchiving an unarchived session is a no-op, mirroring projects.
	again, err := svc.Unarchive(context.Background(), "ses_done")
	if err != nil {
		t.Fatalf("Unarchive again: %v", err)
	}
	if again.ArchivedAt != nil {
		t.Fatalf("again = %+v", again)
	}
	list, err := svc.List(context.Background(), false, "", ListScope{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	assertSessionIDs(t, list, []string{"ses_done"})

	if _, err := svc.Unarchive(context.Background(), "ses_missing"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("err = %v, want ErrSessionNotFound", err)
	}
}

func TestDeleteTerminatesActiveThenRemovesMetadata(t *testing.T) {
	mux := &fakeMux{}
	svc, st := newService(t, mux)
	seedRunning(t, st, "ses_live")
	mux.sessions = []zmx.Session{{Name: zmx.NameForID("ses_live")}}

	if err := svc.Delete(context.Background(), "ses_live"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if mux.terminateCalls != 1 {
		t.Fatalf("terminateCalls = %d, want 1", mux.terminateCalls)
	}
	if _, err := st.Get(context.Background(), "ses_live"); !errors.Is(err, store.ErrSessionNotFound) {
		t.Fatalf("Get after delete err = %v, want not found", err)
	}

	if err := svc.Delete(context.Background(), "ses_live"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("second delete err = %v, want ErrSessionNotFound", err)
	}
}

func TestDeleteStopFailureLeavesMetadataIntact(t *testing.T) {
	mux := &fakeMux{terminateErr: errors.New("zmx kill failed")}
	svc, st := newService(t, mux)
	seedRunning(t, st, "ses_live")
	mux.sessions = []zmx.Session{{Name: zmx.NameForID("ses_live")}}

	if err := svc.Delete(context.Background(), "ses_live"); err == nil {
		t.Fatal("Delete succeeded despite stop failure")
	}
	stored, err := st.Get(context.Background(), "ses_live")
	if err != nil {
		t.Fatalf("metadata lost after aborted delete: %v", err)
	}
	if stored.Status != StatusRunning {
		t.Fatalf("stored status = %s, want running (untouched)", stored.Status)
	}
}

func TestDeleteSettledSessionSkipsTerminate(t *testing.T) {
	mux := &fakeMux{}
	svc, st := newService(t, mux)
	seedRunning(t, st, "ses_done")
	if _, err := st.MarkTerminated(context.Background(), "ses_done"); err != nil {
		t.Fatalf("MarkTerminated: %v", err)
	}

	if err := svc.Delete(context.Background(), "ses_done"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if mux.terminateCalls != 0 {
		t.Fatalf("terminateCalls = %d, want 0", mux.terminateCalls)
	}
	if _, err := st.Get(context.Background(), "ses_done"); !errors.Is(err, store.ErrSessionNotFound) {
		t.Fatalf("Get after delete err = %v, want not found", err)
	}
}

func seedStarting(t *testing.T, st *store.Store, id string) {
	t.Helper()
	if _, err := st.CreateStarting(context.Background(), store.CreateSessionInput{
		ID:          id,
		Action:      "codex",
		Environment: "host-login-shell",
		WorkingDir:  "/work",
		WorkspaceID: testWorkspaceID,
	}); err != nil {
		t.Fatalf("CreateStarting(%s): %v", id, err)
	}
}

func seedRunning(t *testing.T, st *store.Store, id string) {
	t.Helper()
	seedStarting(t, st, id)
	if _, err := st.MarkRunning(context.Background(), id); err != nil {
		t.Fatalf("MarkRunning(%s): %v", id, err)
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

func assertStoredStatus(t *testing.T, st *store.Store, id string, want Status) {
	t.Helper()
	got, err := st.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get(%s): %v", id, err)
	}
	if got.Status != want {
		t.Fatalf("status for %s = %s, want %s", id, got.Status, want)
	}
}

func assertStartingClean(t *testing.T, st *store.Store, id string) store.Session {
	t.Helper()
	got, err := st.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get(%s): %v", id, err)
	}
	if got.Status != StatusStarting || got.FailureCode != "" || got.FailureReason != "" || got.TerminatedAt != nil || got.ArchivedAt != nil {
		t.Fatalf("session %s = %+v, want clean starting record", id, got)
	}
	return got
}
