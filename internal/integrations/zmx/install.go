package zmx

// The single zmx installation routine (ATC-324): download the pinned
// asset, prove the archive's SHA-256 before opening it, take only the zmx
// member out of it, prove the executable's SHA-256 and that it runs and
// reports the pinned version, then publish the whole version directory
// with one rename. Installs serialize across processes on a file lock;
// an interrupted attempt leaves only a staging directory the next attempt
// removes. Installing never touches the namespace's selection — bytes
// and activation are separate (runtime.go).

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

//go:embed NOTICES.txt
var notices []byte

const (
	// executableName is the member taken from the archive and the file
	// published under the version directory.
	executableName = "zmx"
	noticesName    = "NOTICES.txt"
	installLock    = "lock"
	stagingPrefix  = ".install-"
	// maxArchiveBytes bounds a download; release archives are a few
	// megabytes.
	maxArchiveBytes = 64 << 20
	// lockPoll is the retry cadence while another process holds a lock.
	lockPoll = 100 * time.Millisecond
)

// downloadClient bounds connecting and the wait for headers; the body
// transfer is bounded by the caller's context.
var downloadClient = &http.Client{Transport: &http.Transport{
	DialContext:           (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
	TLSHandshakeTimeout:   15 * time.Second,
	ResponseHeaderTimeout: 30 * time.Second,
	Proxy:                 http.ProxyFromEnvironment,
}}

// Install ensures version's verified executable is installed and returns
// its path. Valid bytes already present are reused without any network
// (the common case: every startup after the first); otherwise the asset
// is fetched, verified, and published. The namespace selection is never
// changed here.
func (r *Runtime) Install(ctx context.Context, version string) (string, error) {
	a, err := asset(r.releases, version)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(r.installDir, 0o700); err != nil {
		return "", err
	}
	unlock, err := lock(ctx, filepath.Join(r.installDir, installLock), syscall.LOCK_EX)
	if err != nil {
		return "", fmt.Errorf("waiting for another zmx installation: %w", err)
	}
	defer unlock()

	target := filepath.Join(r.installDir, version)
	executable := filepath.Join(target, executableName)
	if err := validExecutable(executable, a.ExecutableSHA256); err == nil {
		// Reused bytes are trusted, but the attribution beside them may
		// have been removed or altered; restore it from the embedded copy
		// without a download so a reused installation is never incomplete.
		if err := ensureNotices(target); err != nil {
			return "", err
		}
		return executable, nil
	}
	// Under the lock no install is in flight, so every staging directory
	// is a leftover of an interrupted attempt.
	entries, _ := os.ReadDir(r.installDir)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), stagingPrefix) {
			_ = os.RemoveAll(filepath.Join(r.installDir, entry.Name()))
		}
	}
	staging, err := os.MkdirTemp(r.installDir, stagingPrefix)
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(staging) }()

	archive := filepath.Join(staging, a.Name)
	if err := download(ctx, r.downloadBase+"v"+version+"/"+a.Name, archive, a.ArchiveSHA256); err != nil {
		return "", err
	}
	staged := filepath.Join(staging, executableName)
	if err := extractExecutable(archive, staged); err != nil {
		return "", fmt.Errorf("extracting %s: %w", a.Name, err)
	}
	if err := validExecutable(staged, a.ExecutableSHA256); err != nil {
		return "", fmt.Errorf("%s: %w", a.Name, err)
	}
	if err := checkVersion(ctx, staged, version, staging); err != nil {
		return "", err
	}
	if err := ensureNotices(staging); err != nil {
		return "", err
	}
	// A damaged installation is replaced whole, never patched in place:
	// the old directory goes and the verified one takes its name.
	if err := os.RemoveAll(target); err != nil {
		return "", err
	}
	if err := os.Rename(staging, target); err != nil {
		return "", err
	}
	r.logger.Info("installed zmx", "version", version, "executable", executable)
	return executable, nil
}

// lock takes a flock on path, polling while another holder has it so the
// wait honors ctx. The returned func releases it.
func lock(ctx context.Context, path string, how int) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err := syscall.Flock(int(file.Fd()), how|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = file.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-time.After(lockPoll):
		}
	}
}

// download fetches address into dst and proves its SHA-256 is want; a
// mismatch leaves nothing to trust and the file is removed.
func download(ctx context.Context, address, dst, want string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	resp, err := downloadClient.Do(req)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", address, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: HTTP %d", address, resp.StatusCode)
	}
	file, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(resp.Body, maxArchiveBytes+1))
	if closeErr := file.Close(); copyErr != nil || closeErr != nil {
		return fmt.Errorf("downloading %s: %w", address, errors.Join(copyErr, closeErr))
	}
	if info, err := os.Stat(dst); err == nil && info.Size() > maxArchiveBytes {
		return fmt.Errorf("downloading %s: larger than %d bytes", address, maxArchiveBytes)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != want {
		_ = os.Remove(dst)
		return fmt.Errorf("checksum mismatch for %s: ATC expects %s, the download is %s", path.Base(address), want, got)
	}
	return nil
}

// extractExecutable writes the archive's zmx member to dst, mode 0700.
// Any entry that is not a plain directory or regular file, or whose path
// leaves the archive root, is refused outright — nothing from an archive
// like that is trusted. Other regular files are ignored.
func extractExecutable(archive, dst string) error {
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	reader := tar.NewReader(gz)
	found := false
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := path.Clean(header.Name)
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("unsafe archive entry %q", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir, tar.TypeXGlobalHeader, tar.TypeXHeader:
			continue
		case tar.TypeReg:
		default:
			return fmt.Errorf("unsafe archive entry %q (type %q)", header.Name, header.Typeflag)
		}
		if path.Base(name) != executableName {
			continue
		}
		if found {
			return fmt.Errorf("archive holds more than one %s", executableName)
		}
		found = true
		if header.Size > maxArchiveBytes {
			return fmt.Errorf("%s is implausibly large", header.Name)
		}
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, reader); err != nil {
			_ = out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
	}
	if !found {
		return fmt.Errorf("archive holds no %s", executableName)
	}
	return nil
}

// validExecutable proves path is a regular executable file whose SHA-256
// is want.
func validExecutable(path, want string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if info.Mode()&0o100 == 0 {
		return fmt.Errorf("%s is not executable", path)
	}
	got, err := hashFile(path)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("%s does not match its verified identity (want %s, have %s)", path, want, got)
	}
	return nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// checkVersion runs the staged executable's version command — proving it
// runs on this machine at all — and requires it to report version. The
// scratch directory stands in for the socket directory so the probe can
// never touch a real namespace.
func checkVersion(ctx context.Context, executable, version, scratch string) error {
	runCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, executable, "version")
	cmd.Env = Env(scratch, true)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("the downloaded zmx does not run on this machine: %w", err)
	}
	if got := reportedVersion(string(out)); got != version {
		return fmt.Errorf("the downloaded zmx reports version %q, not %s", got, version)
	}
	return nil
}

// reportedVersion parses `zmx version` output: the first line is
// "zmx<tabs><version>".
func reportedVersion(output string) string {
	line, _, _ := strings.Cut(output, "\n")
	fields := strings.Fields(line)
	if len(fields) == 2 && fields[0] == executableName {
		return fields[1]
	}
	return ""
}

// ensureNotices writes the embedded attribution into dir when it is absent
// or altered, atomically. No download is ever needed — the notices ship in
// the binary — so it can heal a reused installation as well as populate a
// fresh one.
func ensureNotices(dir string) error {
	path := filepath.Join(dir, noticesName)
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, notices) {
		return nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, notices, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
