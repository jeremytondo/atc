package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremytondo/atc/internal/store"
)

func newService(t *testing.T) *Service {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "atc.db"))
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return NewService(st, nil)
}

func TestValidateWorkingDirMatrix(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	symlinked := filepath.Join(base, "link")
	if err := os.Symlink(base, symlinked); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{name: "valid", path: base},
		{name: "symlinked dir", path: symlinked},
		{name: "blank", path: "  ", wantErr: true},
		{name: "relative", path: "relative/path", wantErr: true},
		{name: "missing", path: filepath.Join(base, "missing"), wantErr: true},
		{name: "file not dir", path: file, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateWorkingDir(tt.path)
			if tt.wantErr && !errors.Is(err, ErrInvalidWorkingDir) {
				t.Fatalf("err = %v, want ErrInvalidWorkingDir", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
		})
	}
}

func TestCreateValidatesAndCleans(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	workDir := t.TempDir()

	if _, err := svc.Create(ctx, "   ", workDir); !errors.Is(err, ErrInvalidProject) {
		t.Fatalf("blank name err = %v, want ErrInvalidProject", err)
	}
	if _, err := svc.Create(ctx, "atc", "relative/path"); !errors.Is(err, ErrInvalidWorkingDir) {
		t.Fatalf("relative dir err = %v, want ErrInvalidWorkingDir", err)
	}

	created, err := svc.Create(ctx, "  atc  ", workDir+string(os.PathSeparator))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.HasPrefix(created.ID, "prj_") || len(created.ID) != len("prj_")+26 {
		t.Fatalf("id = %q, want prj_-prefixed public id", created.ID)
	}
	if created.Name != "atc" {
		t.Fatalf("name = %q, want trimmed atc", created.Name)
	}
	if created.WorkingDir != workDir {
		t.Fatalf("workingDir = %q, want cleaned %q", created.WorkingDir, workDir)
	}
	if created.CreatedAt.IsZero() {
		t.Fatalf("created = %+v", created)
	}
}

func TestGetListRenameRoundTrip(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	workDir := t.TempDir()

	first, err := svc.Create(ctx, "First", workDir)
	if err != nil {
		t.Fatalf("Create first: %v", err)
	}
	second, err := svc.Create(ctx, "Second", workDir)
	if err != nil {
		t.Fatalf("Create second: %v", err)
	}

	got, err := svc.Get(ctx, first.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != first.ID || got.Name != "First" {
		t.Fatalf("Get = %+v", got)
	}
	if _, err := svc.Get(ctx, "prj_missing"); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("Get missing err = %v, want ErrProjectNotFound", err)
	}

	list, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 || list[0].ID != second.ID || list[1].ID != first.ID {
		t.Fatalf("list = %+v, want newest-first", list)
	}

	renamed, err := svc.Rename(ctx, first.ID, "  Renamed  ")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if renamed.Name != "Renamed" {
		t.Fatalf("renamed = %+v", renamed)
	}
	if _, err := svc.Rename(ctx, first.ID, "   "); !errors.Is(err, ErrInvalidProject) {
		t.Fatalf("blank rename err = %v, want ErrInvalidProject", err)
	}
	if _, err := svc.Rename(ctx, "prj_missing", "name"); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("rename missing err = %v, want ErrProjectNotFound", err)
	}
}

func TestResolveForStart(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	workDir := filepath.Join(t.TempDir(), "repo")
	if err := os.Mkdir(workDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	created, err := svc.Create(ctx, "atc", workDir)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	resolved, err := svc.ResolveForStart(ctx, created.ID)
	if err != nil {
		t.Fatalf("ResolveForStart: %v", err)
	}
	if resolved.ID != created.ID || resolved.WorkingDir != workDir {
		t.Fatalf("resolved = %+v", resolved)
	}

	if _, err := svc.ResolveForStart(ctx, "prj_missing"); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("missing err = %v, want ErrProjectNotFound", err)
	}

	// A directory that vanished since creation fails revalidation.
	if err := os.Remove(workDir); err != nil {
		t.Fatalf("remove dir: %v", err)
	}
	if _, err := svc.ResolveForStart(ctx, created.ID); !errors.Is(err, ErrInvalidWorkingDir) {
		t.Fatalf("vanished dir err = %v, want ErrInvalidWorkingDir", err)
	}
}
