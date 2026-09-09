package artifacts

// Content handling: validating and staging uploaded archives, placing a
// staged version under its artifact, copying a stored version for a
// restoration, and reconciling the content directory against the store
// after a restart. Archives are gzip-compressed tar with only regular
// files and directories at safe relative paths, bounded by Limits, and a
// build must carry index.html at its root.

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/jeremytondo/atc/internal/ids"
)

// stage validates both archives into a fresh staging directory: the build
// extracted under build/, the source copied verbatim to source.tar.gz.
// On any failure the staging is removed and a *PackageError (or the
// I/O error) returned.
func (s *Service) stage(build, source io.Reader) (string, error) {
	dir := filepath.Join(s.root, stagingDir, ids.NewLong("stage-"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := s.stageInto(dir, build, source); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

func (s *Service) stageInto(dir string, build, source io.Reader) error {
	if err := extractBuild(build, filepath.Join(dir, buildDir), s.limits); err != nil {
		return err
	}
	return copySource(source, filepath.Join(dir, sourceFile), s.limits)
}

// extractBuild writes the build archive's files under dir.
func extractBuild(archive io.Reader, dir string, limits Limits) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var hasIndex bool
	err := walkArchive("build", archive, limits, func(name string, header *tar.Header, content io.Reader) error {
		target := filepath.Join(dir, filepath.FromSlash(name))
		if header.Typeflag == tar.TypeDir {
			return os.MkdirAll(target, 0o700)
		}
		if name == indexFile {
			hasIndex = true
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(file, content); err != nil {
			_ = file.Close()
			return err
		}
		return file.Close()
	})
	if err != nil {
		return err
	}
	if !hasIndex {
		return &PackageError{Detail: "build archive has no index.html at its root"}
	}
	return nil
}

// copySource validates the source archive while copying it to path; the
// stored bytes are exactly what was uploaded.
func copySource(archive io.Reader, path string, limits Limits) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	tee := io.TeeReader(archive, file)
	var files int
	err = walkArchive("source", tee, limits, func(name string, header *tar.Header, content io.Reader) error {
		if header.Typeflag != tar.TypeDir {
			files++
		}
		_, err := io.Copy(io.Discard, content)
		return err
	})
	if err != nil {
		return err
	}
	if files == 0 {
		return &PackageError{Detail: "source archive has no files"}
	}
	// The tar reader stops at the end-of-archive marker; the gzip trailer
	// still has to reach the file.
	if _, err := io.Copy(io.Discard, tee); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return file.Close()
}

// walkArchive reads a gzip tar, refusing anything but regular files and
// directories at safe relative paths within limits, and hands each entry
// to visit with a reader bounded to the entry's declared size.
func walkArchive(kind string, archive io.Reader, limits Limits, visit func(name string, header *tar.Header, content io.Reader) error) error {
	invalid := func(format string, args ...any) error {
		return &PackageError{Detail: kind + " archive: " + fmt.Sprintf(format, args...)}
	}
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return invalid("not gzip: %v", err)
	}
	defer func() { _ = gz.Close() }()
	reader := tar.NewReader(gz)
	var files int
	var total int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return invalid("not a tar archive: %v", err)
		}
		switch header.Typeflag {
		case tar.TypeXGlobalHeader, tar.TypeXHeader, tar.TypeGNULongName, tar.TypeGNULongLink:
			continue
		case tar.TypeDir, tar.TypeReg:
		default:
			return invalid("%q is not a regular file or directory (symlinks and special files are refused)", header.Name)
		}
		name, ok := safeName(header.Name)
		if !ok {
			return invalid("%q is not a safe relative path", header.Name)
		}
		if name == "" {
			continue
		}
		if header.Typeflag == tar.TypeDir {
			if err := visit(name, header, io.LimitReader(reader, 0)); err != nil {
				return err
			}
			continue
		}
		files++
		if files > limits.MaxFiles {
			return invalid("more than %d files", limits.MaxFiles)
		}
		if header.Size > limits.MaxFileBytes {
			return invalid("%q exceeds %d bytes", header.Name, limits.MaxFileBytes)
		}
		total += header.Size
		if total > limits.MaxTotalBytes {
			return invalid("total size exceeds %d bytes", limits.MaxTotalBytes)
		}
		if err := visit(name, header, reader); err != nil {
			return err
		}
	}
}

// safeName reduces an archive member name to a clean relative slash path
// inside the archive root; "" is the root itself. Absolute paths, parent
// references, and anything fs.ValidPath refuses are unsafe.
func safeName(name string) (string, bool) {
	if strings.ContainsRune(name, 0) || strings.HasPrefix(name, "/") || strings.HasPrefix(name, `\`) {
		return "", false
	}
	clean := path.Clean(strings.TrimPrefix(name, "./"))
	if clean == "." || clean == "" {
		return "", true
	}
	if clean == ".." || strings.HasPrefix(clean, "../") || !fs.ValidPath(clean) {
		return "", false
	}
	return clean, true
}

// commit places a staged (or copied) version under its artifact: one
// rename, so the version's content is complete or absent, never partial.
func (s *Service) commit(staged, artifactID string, number int) (string, error) {
	if err := os.MkdirAll(s.artifactDir(artifactID), 0o700); err != nil {
		return "", err
	}
	target := s.versionDir(artifactID, number)
	if err := os.Rename(staged, target); err != nil {
		return "", err
	}
	return target, nil
}

// copyVersion copies a stored version's snapshots into a fresh staging
// directory for a restoration; nothing is rebuilt.
func (s *Service) copyVersion(artifactID string, number int) (string, error) {
	dir := filepath.Join(s.root, stagingDir, ids.NewLong("stage-"))
	if err := os.CopyFS(dir, os.DirFS(s.versionDir(artifactID, number))); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("copying version %d: %w", number, err)
	}
	return dir, nil
}

// reconcile makes the content directory match the store: stagings are
// interrupted publications, version directories without rows never
// committed, artifact directories without rows were deleted. Rows whose
// content is missing are logged — they cannot be repaired here.
func (s *Service) reconcile(ctx context.Context) error {
	keys, err := s.repository.VersionKeys(ctx)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(s.root, entry.Name())
		if entry.Name() == stagingDir {
			stagings, err := os.ReadDir(dir)
			if err != nil {
				return err
			}
			for _, staging := range stagings {
				if err := os.RemoveAll(filepath.Join(dir, staging.Name())); err != nil {
					return err
				}
			}
			continue
		}
		numbers, known := keys[entry.Name()]
		if !known {
			s.logger.Info("removing content of deleted artifact", "artifact", entry.Name())
			if err := os.RemoveAll(dir); err != nil {
				return err
			}
			continue
		}
		versions, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, version := range versions {
			number, parseErr := strconv.Atoi(version.Name())
			if parseErr == nil && slices.Contains(numbers, number) {
				continue
			}
			s.logger.Info("removing uncommitted artifact content", "artifact", entry.Name(), "entry", version.Name())
			if err := os.RemoveAll(filepath.Join(dir, version.Name())); err != nil {
				return err
			}
		}
	}
	for artifactID, numbers := range keys {
		for _, number := range numbers {
			if _, err := os.Stat(filepath.Join(s.versionDir(artifactID, number), buildDir, indexFile)); err != nil {
				s.logger.Warn("artifact version content missing", "artifact", artifactID, "version", number, "error", err)
			}
		}
	}
	return nil
}
