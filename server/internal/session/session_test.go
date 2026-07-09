package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jeremytondo/atelier-code/internal/project"
	"github.com/jeremytondo/atelier-code/internal/store"
	"github.com/jeremytondo/atelier-code/internal/zmx"
)

// fakeMux is a faked Multiplexer that records calls and serves a fixed list.
type fakeMux struct {
	sessions []zmx.Session
	listErr  error

	startName, startDir string
	startArgv           []string
	startErr            error
	startCalls          int

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

func newService(t *testing.T, mux Multiplexer) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/atc.db")
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return NewService(st, mux, testActions(), testEnvironments(), nil, nil), st
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
	svc, st := newService(t, mux)
	workDir := t.TempDir()

	got, err := svc.Start(context.Background(), StartInput{
		Action:     "codex",
		Params:     map[string]any{"full-auto": true},
		WorkingDir: workDir,
		Prompt:     "review this change",
		Name:       "Review",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got.ID == "" || got.Status != StatusRunning || !got.Attachable {
		t.Fatalf("started session = %+v", got)
	}
	if got.Name != "Review" || got.Action != "codex" || got.Environment != "host-login-shell" || got.WorkingDir != workDir || got.Prompt != "review this change" {
		t.Fatalf("started metadata = %+v", got)
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
}

func TestStartRejectsValidationBeforeCreatingRecord(t *testing.T) {
	workDir := t.TempDir()
	notADir := filepath.Join(workDir, "file.txt")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	tests := []struct {
		name    string
		input   StartInput
		wantErr error
	}{
		{name: "unknown action", input: StartInput{Action: "ghost", Environment: "host-login-shell", WorkingDir: workDir}, wantErr: ErrUnknownAction},
		{name: "unknown environment", input: StartInput{Action: "codex", Environment: "ghost", WorkingDir: workDir}, wantErr: ErrUnknownEnvironment},
		{name: "invalid param", input: StartInput{Action: "codex", Environment: "host-login-shell", WorkingDir: workDir, Params: map[string]any{"model": "gpt-4"}}, wantErr: ErrInvalidParam},
		{name: "unsupported prompt", input: StartInput{Action: "claude", Environment: "host-login-shell", WorkingDir: workDir, Prompt: "do it"}, wantErr: ErrInvalidParam},
		{name: "blank working dir", input: StartInput{Action: "codex", Environment: "host-login-shell", WorkingDir: "   "}, wantErr: ErrInvalidWorkingDir},
		{name: "relative working dir", input: StartInput{Action: "codex", Environment: "host-login-shell", WorkingDir: "relative/path"}, wantErr: ErrInvalidWorkingDir},
		{name: "missing working dir", input: StartInput{Action: "codex", Environment: "host-login-shell", WorkingDir: filepath.Join(workDir, "missing")}, wantErr: ErrInvalidWorkingDir},
		{name: "working dir is a file", input: StartInput{Action: "codex", Environment: "host-login-shell", WorkingDir: notADir}, wantErr: ErrInvalidWorkingDir},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := &fakeMux{}
			svc, st := newService(t, mux)
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

// fakeResolver is a faked ProjectResolver recording resolve calls.
type fakeResolver struct {
	project project.Project
	err     error
	calls   int
	lastID  string
}

func (f *fakeResolver) ResolveForStart(_ context.Context, id string) (project.Project, error) {
	f.calls++
	f.lastID = id
	if f.err != nil {
		return project.Project{}, f.err
	}
	return f.project, nil
}

// newProjectService seeds a project row (the sessions FK needs a real record)
// and returns a service whose resolver serves it.
func newProjectService(t *testing.T, mux Multiplexer, resolver *fakeResolver) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/atc.db")
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if resolver.project.ID != "" {
		if _, err := st.CreateProject(context.Background(), store.CreateProjectInput{
			ID:         resolver.project.ID,
			Name:       resolver.project.Name,
			WorkingDir: resolver.project.WorkingDir,
		}); err != nil {
			t.Fatalf("CreateProject: %v", err)
		}
	}
	return NewService(st, mux, testActions(), testEnvironments(), resolver, nil), st
}

func TestStartWithProjectInheritsDirectoryAndPersistsProjectID(t *testing.T) {
	mux := &fakeMux{}
	projectDir := t.TempDir()
	resolver := &fakeResolver{project: project.Project{ID: "prj_home", Name: "Home", WorkingDir: projectDir}}
	svc, st := newProjectService(t, mux, resolver)

	got, err := svc.Start(context.Background(), StartInput{
		Action:    "codex",
		ProjectID: "prj_home",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if resolver.calls != 1 || resolver.lastID != "prj_home" {
		t.Fatalf("resolver calls = %d lastID = %q", resolver.calls, resolver.lastID)
	}
	if got.WorkingDir != projectDir || got.ProjectID != "prj_home" {
		t.Fatalf("started = %+v, want inherited dir %q", got, projectDir)
	}
	if got.Project == nil || got.Project.ID != "prj_home" || got.Project.Name != "Home" {
		t.Fatalf("project ref = %+v", got.Project)
	}
	if mux.startDir != projectDir {
		t.Fatalf("launch dir = %q, want project dir", mux.startDir)
	}
	stored, err := st.Get(context.Background(), got.ID)
	if err != nil {
		t.Fatalf("Get stored: %v", err)
	}
	if stored.ProjectID != "prj_home" || stored.WorkingDir != projectDir {
		t.Fatalf("stored = %+v, want project id and dir snapshot", stored)
	}
}

func TestStartWithProjectRejectsResolverErrorsBeforeCreatingRecord(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		wantErr error
	}{
		{name: "archived project", err: project.ErrProjectArchived, wantErr: project.ErrProjectArchived},
		{name: "unknown project", err: project.ErrProjectNotFound, wantErr: project.ErrProjectNotFound},
		{name: "vanished directory", err: project.ErrInvalidWorkingDir, wantErr: ErrInvalidWorkingDir},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := &fakeMux{}
			resolver := &fakeResolver{err: tt.err}
			svc, st := newProjectService(t, mux, resolver)

			_, err := svc.Start(context.Background(), StartInput{Action: "codex", ProjectID: "prj_home"})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if mux.startCalls != 0 {
				t.Fatal("wrapper Start called for rejected project start")
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

func TestListFiltersByProject(t *testing.T) {
	mux := &fakeMux{}
	projectDir := t.TempDir()
	resolver := &fakeResolver{project: project.Project{ID: "prj_home", Name: "Home", WorkingDir: projectDir}}
	svc, _ := newProjectService(t, mux, resolver)

	scoped, err := svc.Start(context.Background(), StartInput{Action: "codex", ProjectID: "prj_home"})
	if err != nil {
		t.Fatalf("Start scoped: %v", err)
	}
	if _, err := svc.Start(context.Background(), StartInput{Action: "codex", WorkingDir: t.TempDir()}); err != nil {
		t.Fatalf("Start unscoped: %v", err)
	}

	filtered, err := svc.List(context.Background(), true, "", "prj_home")
	if err != nil {
		t.Fatalf("List filtered: %v", err)
	}
	assertSessionIDs(t, filtered, []string{scoped.ID})
	all, err := svc.List(context.Background(), true, "", "")
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

	_, err := svc.Start(context.Background(), StartInput{Action: "claude", Environment: "host-login-shell", WorkingDir: t.TempDir()})
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

	got, err := svc.List(context.Background(), true, "", "")
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

	running, err := svc.List(context.Background(), true, StatusRunning, "")
	if err != nil {
		t.Fatalf("List running: %v", err)
	}
	assertSessionIDs(t, running, []string{"ses_run_live"})

	failed, err := svc.List(context.Background(), true, StatusFailed, "")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	assertSessionIDs(t, failed, []string{})

	starting, err := svc.List(context.Background(), true, StatusStarting, "")
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

	listed, err := svc.List(context.Background(), true, "", "")
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

	got, err := svc.List(context.Background(), false, "", "")
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
			name: "terminate",
			run: func(svc *Service, id string) error {
				_, err := svc.Terminate(context.Background(), id)
				return err
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
		live := live
		liveName := "not-live"
		if live {
			liveName = "live"
		}
		for _, tt := range tests {
			tt := tt
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
	list, err := svc.List(context.Background(), false, "", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != "ses_live" {
		t.Fatalf("default list = %+v, want only live session", list)
	}
}

func seedStarting(t *testing.T, st *store.Store, id string) {
	t.Helper()
	if _, err := st.CreateStarting(context.Background(), store.CreateSessionInput{
		ID:          id,
		Action:      "codex",
		Environment: "host-login-shell",
		WorkingDir:  "/work",
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
