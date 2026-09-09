package authoring

// Building and previewing. A build runs on a snapshot assembled from the
// shipped platform and the copy's src/document alone — never from the
// rest of the copy's directory, so nothing an author or a tool left
// there (credentials, editor state, version control) can reach a build
// or its source archive. The snapshot is taken under the copy's lock and
// verified unchanged after copying, so an editor saving mid-copy cannot
// produce a build of a state that never existed. Preview runs Vite's dev
// server on the copy itself, for live editing.

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jeremytondo/atc/internal/ids"
)

// Build is a completed build's outputs.
type Build struct {
	// BuildArchive and SourceArchive are the gzip tars a publication
	// uploads: the static build, and the snapshot it was built from.
	BuildArchive  string
	SourceArchive string
	// Dir is the built site, for inspection; it lives in the copy's
	// scratch and is replaced by the next build.
	Dir string
}

// ErrCheckFailed marks a build stopped by the type check or the bundler;
// the tool's own output explains what to fix.
var ErrCheckFailed = errors.New("check failed")

// Check runs the type check on a snapshot of the copy and reports the
// tools' findings through Output.
func (s *Service) Check(ctx context.Context, ref string) error {
	_, err := s.build(ctx, ref, false)
	return err
}

// Build type-checks and bundles a snapshot of the copy, leaving the
// archives under the copy's scratch directory.
func (s *Service) Build(ctx context.Context, ref string) (Build, error) {
	return s.build(ctx, ref, true)
}

func (s *Service) build(ctx context.Context, ref string, bundle bool) (Build, error) {
	copy, inst, release, err := s.prepare(ctx, ref)
	if err != nil {
		return Build{}, err
	}
	defer release()
	return s.buildPrepared(ctx, copy, inst, bundle)
}

// buildPrepared is Build on a copy the caller has prepared and holds.
func (s *Service) buildPrepared(ctx context.Context, copy Copy, inst installation, bundle bool) (Build, error) {
	scratch := filepath.Join(copy.Dir, scratchDir)
	snapshot := filepath.Join(scratch, "snapshot-"+ids.NewLong(""))
	defer func() { _ = os.RemoveAll(snapshot) }()
	if err := s.snapshot(copy, snapshot); err != nil {
		return Build{}, err
	}
	if err := os.Symlink(inst.modules, filepath.Join(snapshot, "node_modules")); err != nil {
		return Build{}, err
	}
	env := inst.env("VITE_ATC_TITLE=" + copy.Title)
	_, _ = fmt.Fprintln(s.output, "checking types")
	if err := s.run(ctx, snapshot, env, inst.node, inst.tool("typescript/bin/tsc"), "--noEmit", "-p", "tsconfig.json"); err != nil {
		return Build{}, toolFailure(ctx, err, "the type check", "found errors (adapt the document to the current platform)")
	}
	if !bundle {
		return Build{}, nil
	}
	_, _ = fmt.Fprintln(s.output, "building")
	if err := s.run(ctx, snapshot, env, inst.node, inst.tool("vite/bin/vite.js"), "build", "--logLevel", "warn"); err != nil {
		return Build{}, toolFailure(ctx, err, "the build", "failed")
	}
	dist := filepath.Join(snapshot, "dist")
	if _, err := os.Stat(filepath.Join(dist, "index.html")); err != nil {
		return Build{}, fmt.Errorf("%w: the build produced no index.html", ErrCheckFailed)
	}
	result := Build{
		BuildArchive: filepath.Join(scratch, "build.tar.gz"), SourceArchive: filepath.Join(scratch, "source.tar.gz"),
		Dir: filepath.Join(scratch, "dist"),
	}
	if err := archiveTree(dist, result.BuildArchive, nil); err != nil {
		return Build{}, err
	}
	// The source snapshot is the project as built, minus what is not
	// source: the dependency link and the build output.
	if err := archiveTree(snapshot, result.SourceArchive, map[string]bool{"node_modules": true, "dist": true}); err != nil {
		return Build{}, err
	}
	if err := os.RemoveAll(result.Dir); err != nil {
		return Build{}, err
	}
	if err := os.Rename(dist, result.Dir); err != nil {
		return Build{}, err
	}
	return result, nil
}

// toolFailure tells a tool that ran and judged the source (ErrCheckFailed,
// with its report already on Output) from one that could not run or was
// interrupted, whose cause is the error itself.
func toolFailure(ctx context.Context, err error, tool, verdict string) error {
	var exit *exec.ExitError
	if ctx.Err() != nil || !errors.As(err, &exit) {
		return fmt.Errorf("%s did not complete: %w", tool, err)
	}
	return fmt.Errorf("%w: %s %s", ErrCheckFailed, tool, verdict)
}

// snapshot assembles the build tree: the shipped platform and a copy of
// src/document, verified unchanged across the copy (an editor saving
// mid-copy is retried; a document that will not hold still fails).
func (s *Service) snapshot(copy Copy, dir string) error {
	for attempt := 0; attempt < 3; attempt++ {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		if err := writePlatformFiles(dir); err != nil {
			return err
		}
		before, err := manifest(copy.Document)
		if err != nil {
			return err
		}
		if err := copyDocument(copy.Document, filepath.Join(dir, filepath.FromSlash(documentDir))); err != nil {
			return err
		}
		after, err := manifest(copy.Document)
		if err != nil {
			return err
		}
		if before == after {
			return nil
		}
	}
	return errors.New("src/document kept changing while it was being copied; pause the edits and retry")
}

// manifest fingerprints a tree by the names, sizes, and modification
// times of its files.
func manifest(dir string) (string, error) {
	var b strings.Builder
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(&b, "%s\x00%d\x00%d\x00%d\n", p, info.Mode(), info.Size(), info.ModTime().UnixNano())
		return nil
	})
	return b.String(), err
}

// copyDocument copies the regular files under src into dir; symlinks
// and anything else are left out (a build works on real files only).
func copyDocument(src, dir string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dir, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		content, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, content, 0o600)
	})
}

// archiveTree writes a gzip tar of dir's regular files, skipping the
// named top-level entries.
func archiveTree(dir, archive string, skip map[string]bool) error {
	file, err := os.Create(archive)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return err
		}
		name := filepath.ToSlash(rel)
		if skip[strings.SplitN(name, "/", 2)[0]] {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return tw.WriteHeader(&tar.Header{Name: name + "/", Typeflag: tar.TypeDir, Mode: 0o755})
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: info.Size(), ModTime: info.ModTime()}); err != nil {
			return err
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, in)
		_ = in.Close()
		return err
	})
	return errors.Join(err, tw.Close(), gz.Close(), file.Close())
}

// Preview serves the working copy with Vite's dev server on 127.0.0.1 at
// port (0 lets Vite choose) until ctx is cancelled, printing the URL and
// the server's output through Output. The copy stays locked for the
// duration: a build or publish waits for the preview to end.
func (s *Service) Preview(ctx context.Context, ref string, port int) error {
	copy, inst, release, err := s.prepare(ctx, ref)
	if err != nil {
		return err
	}
	defer release()
	args := []string{inst.tool("vite/bin/vite.js"), "--host", "127.0.0.1", "--clearScreen", "false"}
	if port > 0 {
		args = append(args, "--port", strconv.Itoa(port), "--strictPort")
	}
	_, _ = fmt.Fprintf(s.output, "previewing %s (%s); the URL follows, edit %s and reload\n", copy.ID, copy.Title, copy.Document)
	return s.run(ctx, copy.Dir, inst.env("VITE_ATC_TITLE="+copy.Title), inst.node, args...)
}

func (s *Service) run(ctx context.Context, dir string, env []string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = s.output
	cmd.Stderr = s.output
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 5 * time.Second
	return cmd.Run()
}
