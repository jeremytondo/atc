package session

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jeremytondo/atc/internal/store"
	"github.com/jeremytondo/atc/internal/zmx"
)

const testWorkspaceID = "wsp_test"

type fakeMux struct {
	startErr error
	started  []string
	dir      string
	argv     []string
	live     map[string]bool
}

func (f *fakeMux) Start(_ context.Context, name, dir string, argv []string) error {
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
	return nil
}

func (f *fakeMux) Send(context.Context, string, []byte) error { return nil }
func (f *fakeMux) Attach(context.Context, string, uint16, uint16) (zmx.PTY, error) {
	return nil, nil
}
func (f *fakeMux) List(context.Context) ([]zmx.Session, error) {
	out := make([]zmx.Session, 0, len(f.live))
	for name := range f.live {
		out = append(out, zmx.Session{Name: name})
	}
	return out, nil
}
func (f *fakeMux) Terminate(_ context.Context, name string) error {
	delete(f.live, name)
	return nil
}

type fakeResolver struct {
	dir string
}

func (f fakeResolver) ResolveForStart(context.Context, string) (string, error) {
	return f.dir, nil
}

func newTestService(t *testing.T, mux *fakeMux) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "atc.db"))
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
	return NewService(st, mux, fakeResolver{dir: "/work"}, nil), st
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
