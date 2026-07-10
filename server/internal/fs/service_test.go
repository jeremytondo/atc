package fs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	return NewService(nil)
}

// makeUnreadable removes all permissions from path and restores them on
// cleanup so t.TempDir removal succeeds.
func makeUnreadable(t *testing.T, path string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission modes are not enforced")
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o755) })
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func entryByName(t *testing.T, listing Listing, name string) Entry {
	t.Helper()
	for _, entry := range listing.Entries {
		if entry.Name == name {
			return entry
		}
	}
	t.Fatalf("entry %q not found in %v", name, entryNames(listing))
	return Entry{}
}

func entryNames(listing Listing) []string {
	names := make([]string, 0, len(listing.Entries))
	for _, entry := range listing.Entries {
		names = append(names, entry.Name)
	}
	return names
}

func TestListDefaultsToHome(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "marker"), "")
	svc := newTestService(t)
	svc.home = home

	listing, err := svc.List(context.Background(), "", false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if listing.Path != home {
		t.Fatalf("path = %q, want %q", listing.Path, home)
	}
	if got := entryByName(t, listing, "marker"); got.Kind != KindFile {
		t.Fatalf("marker = %+v, want file", got)
	}
}

func TestListHappyPath(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, "sub"))
	writeFile(t, filepath.Join(root, "readme.md"), "hello")
	symlink(t, filepath.Join(root, "sub"), filepath.Join(root, "sublink"))
	symlink(t, filepath.Join(root, "readme.md"), filepath.Join(root, "filelink"))
	symlink(t, filepath.Join(root, "nowhere"), filepath.Join(root, "dangling"))

	listing, err := newTestService(t).List(context.Background(), root, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if listing.Path != root {
		t.Errorf("path = %q, want %q", listing.Path, root)
	}
	if listing.Truncated {
		t.Error("truncated = true, want false")
	}

	sub := entryByName(t, listing, "sub")
	if sub.Kind != KindDirectory || sub.IsSymlink || sub.Size != nil || sub.ModifiedAt == nil {
		t.Errorf("sub = %+v, want plain directory with modifiedAt, no size", sub)
	}
	if sub.Path != filepath.Join(root, "sub") {
		t.Errorf("sub.Path = %q", sub.Path)
	}

	file := entryByName(t, listing, "readme.md")
	if file.Kind != KindFile || file.IsSymlink || file.ModifiedAt == nil {
		t.Errorf("file = %+v, want plain file with modifiedAt", file)
	}
	if file.Size == nil || *file.Size != int64(len("hello")) {
		t.Errorf("file.Size = %v, want 5", file.Size)
	}

	sublink := entryByName(t, listing, "sublink")
	if sublink.Kind != KindDirectory || !sublink.IsSymlink || sublink.Size != nil || sublink.ModifiedAt == nil {
		t.Errorf("sublink = %+v, want symlinked directory", sublink)
	}

	filelink := entryByName(t, listing, "filelink")
	if filelink.Kind != KindFile || !filelink.IsSymlink || filelink.Size == nil {
		t.Errorf("filelink = %+v, want symlinked file with size", filelink)
	}

	dangling := entryByName(t, listing, "dangling")
	if dangling.Kind != KindUnknown || !dangling.IsSymlink || dangling.Size != nil || dangling.ModifiedAt != nil {
		t.Errorf("dangling = %+v, want unknown symlink without size/modifiedAt", dangling)
	}
}

func TestListEmptyDirectory(t *testing.T) {
	root := t.TempDir()
	listing, err := newTestService(t).List(context.Background(), root, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if listing.Entries == nil || len(listing.Entries) != 0 {
		t.Fatalf("entries = %#v, want empty non-nil slice", listing.Entries)
	}
}

func TestListSorting(t *testing.T) {
	// Only names that coexist on case-insensitive filesystems (macOS APFS);
	// case-only ties are covered by TestCompareEntries on constructed values.
	root := t.TempDir()
	for _, dir := range []string{".hidden-dir", "Beta", "alpha"} {
		mkdir(t, filepath.Join(root, dir))
	}
	for _, file := range []string{".dotfile", "delta.txt", "Gamma.txt"} {
		writeFile(t, filepath.Join(root, file), "")
	}

	listing, err := newTestService(t).List(context.Background(), root, true)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{".hidden-dir", "alpha", "Beta", ".dotfile", "delta.txt", "Gamma.txt"}
	got := entryNames(listing)
	if len(got) != len(want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("names = %v, want %v", got, want)
		}
	}
}

func TestCompareEntries(t *testing.T) {
	entries := []Entry{
		{Name: "Gamma.txt", Kind: KindFile},
		{Name: "ab", Kind: KindFile},
		{Name: "alpha", Kind: KindDirectory},
		{Name: ".dotfile", Kind: KindFile},
		{Name: "AB", Kind: KindFile},
		{Name: "delta.txt", Kind: KindFile},
		{Name: ".hidden-dir", Kind: KindDirectory},
		{Name: "Beta", Kind: KindDirectory},
	}
	slices.SortFunc(entries, compareEntries)

	want := []string{".hidden-dir", "alpha", "Beta", ".dotfile", "AB", "ab", "delta.txt", "Gamma.txt"}
	for i := range want {
		if entries[i].Name != want[i] {
			names := make([]string, len(entries))
			for j, entry := range entries {
				names[j] = entry.Name
			}
			t.Fatalf("names = %v, want %v", names, want)
		}
	}
}

func TestListHiddenFiltering(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".secret"), "")
	writeFile(t, filepath.Join(root, "visible"), "")
	svc := newTestService(t)

	listing, err := svc.List(context.Background(), root, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if names := entryNames(listing); len(names) != 1 || names[0] != "visible" {
		t.Fatalf("default names = %v, want [visible]", names)
	}

	listing, err = svc.List(context.Background(), root, true)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if names := entryNames(listing); len(names) != 2 {
		t.Fatalf("showHidden names = %v, want both entries", names)
	}
}

func TestListCapCountsPostFilter(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{".h1", ".h2", "a", "b", "c"} {
		writeFile(t, filepath.Join(root, name), "")
	}
	svc := newTestService(t)
	svc.maxEntries = 3

	listing, err := svc.List(context.Background(), root, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listing.Entries) != 3 || listing.Truncated {
		t.Fatalf("entries = %d truncated = %v, want 3 untruncated (hidden filtered before cap)", len(listing.Entries), listing.Truncated)
	}

	listing, err = svc.List(context.Background(), root, true)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listing.Entries) != 3 || !listing.Truncated {
		t.Fatalf("entries = %d truncated = %v, want 3 truncated", len(listing.Entries), listing.Truncated)
	}
}

func TestListTruncatesAtRealCap(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: skipping 10k-file tree")
	}
	root := t.TempDir()
	for i := range maxListEntries + 1 {
		file, err := os.Create(filepath.Join(root, fmt.Sprintf("f%05d", i)))
		if err != nil {
			t.Fatal(err)
		}
		file.Close()
	}

	listing, err := newTestService(t).List(context.Background(), root, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listing.Entries) != maxListEntries || !listing.Truncated {
		t.Fatalf("entries = %d truncated = %v, want %d truncated", len(listing.Entries), listing.Truncated, maxListEntries)
	}
}

func TestListNormalization(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	mkdir(t, sub)
	svc := newTestService(t)

	for _, path := range []string{
		sub + "/",
		root + "//sub",
		root + "/./sub",
		root + "/sub/../sub",
	} {
		listing, err := svc.List(context.Background(), path, false)
		if err != nil {
			t.Fatalf("List(%q): %v", path, err)
		}
		if listing.Path != sub {
			t.Errorf("List(%q).Path = %q, want %q", path, listing.Path, sub)
		}
	}

	for _, path := range []string{"relative/path", "/a\x00b"} {
		if _, err := svc.List(context.Background(), path, false); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("List(%q) err = %v, want ErrInvalidPath", path, err)
		}
	}
}

func TestListRootPathIsValid(t *testing.T) {
	listing, err := newTestService(t).List(context.Background(), "/", false)
	if err != nil {
		t.Fatalf("List(/): %v", err)
	}
	if listing.Path != "/" {
		t.Fatalf("path = %q, want /", listing.Path)
	}
}

func TestListAcceptsResolvedSymlinkTargets(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	mkdir(t, root)
	mkdir(t, outside)
	writeFile(t, filepath.Join(outside, "marker"), "")
	symlink(t, outside, filepath.Join(root, "link"))
	svc := newTestService(t)

	lexical := filepath.Join(root, "link")
	listing, err := svc.List(context.Background(), lexical, false)
	if err != nil {
		t.Fatalf("List(symlinked dir): %v", err)
	}
	if listing.Path != lexical {
		t.Errorf("path = %q, want lexical %q", listing.Path, lexical)
	}

	listing, err = svc.List(context.Background(), outside, false)
	if err != nil {
		t.Fatalf("List(resolved target): %v", err)
	}
	if got := entryByName(t, listing, "marker"); got.Path != filepath.Join(outside, "marker") {
		t.Errorf("marker path = %q, want under resolved target", got.Path)
	}
}

func TestListFIFOIsUnknown(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}

	listing, err := newTestService(t).List(context.Background(), root, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	pipe := entryByName(t, listing, "pipe")
	if pipe.Kind != KindUnknown || pipe.IsSymlink || pipe.Size != nil || pipe.ModifiedAt != nil {
		t.Fatalf("pipe = %+v, want unknown non-symlink without size/modifiedAt", pipe)
	}
}

func TestListErrors(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "file"), "x")
	svc := newTestService(t)

	if _, err := svc.List(context.Background(), filepath.Join(root, "missing"), false); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing err = %v, want ErrNotFound", err)
	}
	if _, err := svc.List(context.Background(), filepath.Join(root, "file"), false); !errors.Is(err, ErrNotDirectory) {
		t.Errorf("file err = %v, want ErrNotDirectory", err)
	}
}

func TestListPermissionDenied(t *testing.T) {
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	mkdir(t, locked)
	makeUnreadable(t, locked)
	svc := newTestService(t)

	// The unreadable child is still classified as a directory in its parent.
	listing, err := svc.List(context.Background(), root, false)
	if err != nil {
		t.Fatalf("List(parent): %v", err)
	}
	if got := entryByName(t, listing, "locked"); got.Kind != KindDirectory {
		t.Errorf("locked kind = %q, want directory", got.Kind)
	}

	// Listing it directly is denied.
	if _, err := svc.List(context.Background(), locked, false); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("locked err = %v, want ErrPermissionDenied", err)
	}
}

func TestListCanceledContext(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := newTestService(t).List(ctx, root, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestListTimeout(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "file"), "")
	svc := newTestService(t)
	svc.timeout = time.Nanosecond

	_, err := svc.List(context.Background(), root, false)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}
