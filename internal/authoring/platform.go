package authoring

// The installed platform under <root>/platform:
//
//	runtime/<node version>/      the private runtime, one directory per version
//	runtime/current -> ...       the one in use
//	deps/<deps digest>/          package.json, package-lock.json, node_modules
//	deps/current -> ...          the one in use
//	npm-cache/                   npm's cache, kept inside the installation
//	lock                         the installation lock
//
// Every authoring operation ensures the installation is current before
// using it — first use installs, an ATC upgrade refreshes — under a file
// lock so two processes never install at once and no build sees a
// half-refreshed platform. A component is installed into its own
// directory and the current link switched to it atomically, so a failed
// or interrupted install leaves the previous installation in place, and
// a component is trusted only when its essential files are present.

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/jeremytondo/atc/internal/ids"
)

const (
	platformDir    = "platform"
	runtimeDir     = "runtime"
	depsDir        = "deps"
	currentLink    = "current"
	npmCacheDir    = "npm-cache"
	lockFile       = "lock"
	maxRuntimeFile = 512 << 20
)

// installation is a ready platform: the paths the tools run from.
type installation struct {
	dir     string
	node    string // the node executable
	npm     string // npm-cli.js
	modules string // node_modules
}

func (i installation) tool(name string) string {
	return filepath.Join(i.modules, filepath.FromSlash(name))
}

// env is the environment the runtime and its tools run with: the private
// runtime first on PATH (npm and Vite spawn node), npm kept inside the
// installation, and no update chatter.
func (i installation) env(extra ...string) []string {
	env := []string{
		"PATH=" + filepath.Dir(i.node) + string(os.PathListSeparator) + os.Getenv("PATH"),
		"npm_config_cache=" + filepath.Join(i.dir, npmCacheDir),
		"npm_config_update_notifier=false",
		"npm_config_fund=false",
		"npm_config_audit=false",
		"NO_UPDATE_NOTIFIER=1",
	}
	for _, name := range []string{"HOME", "TMPDIR", "LANG", "LC_ALL", "TERM"} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	return append(env, extra...)
}

// runtimeEssentials and depsEssentials are what an installed component
// must have to be trusted.
var (
	runtimeEssentials = []string{"bin/node", "lib/node_modules/npm/bin/npm-cli.js"}
	depsEssentials    = []string{"node_modules/typescript/bin/tsc", "node_modules/vite/bin/vite.js"}
)

// ensurePlatform returns the current installation, installing or
// refreshing whichever component is missing, incomplete, or from another
// version. The returned unlock releases the shared lock that keeps the
// installation stable while the caller uses it.
func (s *Service) ensurePlatform(ctx context.Context) (installation, func(), error) {
	dir := filepath.Join(s.root, platformDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return installation{}, nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, lockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return installation{}, nil, err
	}
	unlock := func() { _ = lock.Close() }
	runtimeTarget := filepath.Join(dir, runtimeDir, NodeVersion)
	depsTarget := filepath.Join(dir, depsDir, s.deps)
	inst := installation{
		dir:     dir,
		node:    filepath.Join(dir, runtimeDir, currentLink, "bin", "node"),
		npm:     filepath.Join(dir, runtimeDir, currentLink, "lib", "node_modules", "npm", "bin", "npm-cli.js"),
		modules: filepath.Join(dir, depsDir, currentLink, "node_modules"),
	}
	// Fast path: a shared lock and a complete installation. Anything else
	// takes the exclusive lock, re-checks (another process may have
	// finished the same install), and installs what is missing.
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_SH); err != nil {
		unlock()
		return installation{}, nil, err
	}
	if linked(filepath.Join(dir, runtimeDir), runtimeTarget, runtimeEssentials) && linked(filepath.Join(dir, depsDir), depsTarget, depsEssentials) {
		return inst, unlock, nil
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		unlock()
		return installation{}, nil, err
	}
	if !linked(filepath.Join(dir, runtimeDir), runtimeTarget, runtimeEssentials) {
		_, _ = fmt.Fprintf(s.output, "installing the private Node %s runtime\n", NodeVersion)
		if err := s.installRuntime(ctx, dir, runtimeTarget); err != nil {
			unlock()
			return installation{}, nil, fmt.Errorf("installing the Node runtime: %w (nothing was changed; retry when the download can complete)", err)
		}
	}
	if !linked(filepath.Join(dir, depsDir), depsTarget, depsEssentials) {
		_, _ = fmt.Fprintln(s.output, "installing the platform dependencies")
		if err := s.installDeps(ctx, dir, inst, depsTarget); err != nil {
			unlock()
			return installation{}, nil, fmt.Errorf("installing the platform dependencies: %w", err)
		}
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_SH); err != nil {
		unlock()
		return installation{}, nil, err
	}
	return inst, unlock, nil
}

// linked reports whether parent/current points at target and target has
// its essentials.
func linked(parent, target string, essentials []string) bool {
	if link, err := os.Readlink(filepath.Join(parent, currentLink)); err != nil || link != filepath.Base(target) {
		return false
	}
	return complete(target, essentials)
}

func complete(target string, essentials []string) bool {
	for _, name := range essentials {
		info, err := os.Stat(filepath.Join(target, filepath.FromSlash(name)))
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	return true
}

// switchCurrent points parent/current at target atomically and removes
// the other installed versions.
func switchCurrent(parent, target string) error {
	tmp := filepath.Join(parent, "."+currentLink+"-"+ids.NewLong(""))
	if err := os.Symlink(filepath.Base(target), tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(parent, currentLink)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == currentLink || name == filepath.Base(target) || !entry.IsDir() {
			continue
		}
		_ = os.RemoveAll(filepath.Join(parent, name))
	}
	return nil
}

// installRuntime downloads Node's release archive for this platform,
// verifies it against SHASUMS256.txt, extracts it beside the runtime
// directory, moves it into place, and makes it current.
func (s *Service) installRuntime(ctx context.Context, dir, target string) error {
	asset, err := runtimeAsset()
	if err != nil {
		return err
	}
	parent := filepath.Join(dir, runtimeDir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	if !complete(target, runtimeEssentials) {
		sums, err := s.fetch(ctx, nodeDist+"SHASUMS256.txt")
		if err != nil {
			return err
		}
		want, err := expectedSum(sums, asset)
		_ = sums.Close()
		if err != nil {
			return err
		}
		staging, err := os.MkdirTemp(parent, ".install-*")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(staging) }()
		archive, err := os.Create(filepath.Join(staging, asset))
		if err != nil {
			return err
		}
		body, err := s.fetch(ctx, nodeDist+asset)
		if err != nil {
			_ = archive.Close()
			return err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(io.MultiWriter(archive, hash), body)
		_ = body.Close()
		if closeErr := archive.Close(); copyErr != nil || closeErr != nil {
			return errors.Join(copyErr, closeErr)
		}
		if got := hex.EncodeToString(hash.Sum(nil)); got != want {
			return fmt.Errorf("checksum mismatch for %s: SHASUMS256.txt says %s, the download is %s", asset, want, got)
		}
		extracted := filepath.Join(staging, "tree")
		if err := extractRuntime(filepath.Join(staging, asset), extracted); err != nil {
			return fmt.Errorf("extracting %s: %w", asset, err)
		}
		// The archive holds one top-level directory; that is the runtime.
		entries, err := os.ReadDir(extracted)
		if err != nil {
			return err
		}
		if len(entries) != 1 || !entries[0].IsDir() {
			return fmt.Errorf("%s does not contain a single runtime directory", asset)
		}
		if !complete(filepath.Join(extracted, entries[0].Name()), runtimeEssentials) {
			return fmt.Errorf("%s lacks node or npm", asset)
		}
		if err := os.RemoveAll(target); err != nil {
			return err
		}
		if err := os.Rename(filepath.Join(extracted, entries[0].Name()), target); err != nil {
			return err
		}
	}
	return switchCurrent(parent, target)
}

// runtimeAsset names Node's archive for this machine.
func runtimeAsset() (string, error) {
	var arch string
	switch runtime.GOARCH {
	case "amd64":
		arch = "x64"
	case "arm64":
		arch = "arm64"
	default:
		return "", fmt.Errorf("no Node runtime for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	switch runtime.GOOS {
	case "linux", "darwin":
	default:
		return "", fmt.Errorf("no Node runtime for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	return fmt.Sprintf("node-v%s-%s-%s.tar.gz", NodeVersion, runtime.GOOS, arch), nil
}

func expectedSum(sums io.Reader, asset string) (string, error) {
	scanner := bufio.NewScanner(sums)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[1] == asset {
			return fields[0], nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("%s has no entry in SHASUMS256.txt", asset)
}

// extractRuntime unpacks Node's tar.gz: directories, regular files with
// their executable bits, and the relative symlinks the runtime ships (npm
// and npx point into lib). Anything escaping dir is refused.
func extractRuntime(archive, dir string) error {
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
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(header.Name)
		if name == "." || strings.HasPrefix(name, "../") || path.IsAbs(name) {
			continue
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if header.Size > maxRuntimeFile {
				return fmt.Errorf("%s is implausibly large", header.Name)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			mode := os.FileMode(0o600)
			if header.Mode&0o111 != 0 {
				mode = 0o700
			}
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
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
		case tar.TypeSymlink:
			link := header.Linkname
			resolved := path.Clean(path.Join(path.Dir(name), link))
			if path.IsAbs(link) || strings.HasPrefix(resolved, "../") {
				return fmt.Errorf("symlink %s escapes the runtime", header.Name)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			if err := os.Symlink(filepath.FromSlash(link), target); err != nil {
				return err
			}
		default:
			// pax headers and the like carry no content of their own.
		}
	}
}

// installDeps materializes the platform's manifest and lockfile and runs
// a clean, script-free install into the digest's own directory, then
// makes it current.
func (s *Service) installDeps(ctx context.Context, dir string, inst installation, target string) error {
	parent := filepath.Join(dir, depsDir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	if !complete(target, depsEssentials) {
		staging, err := os.MkdirTemp(parent, ".install-*")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(staging) }()
		for _, name := range []string{"package.json", "package-lock.json"} {
			content, err := fs.ReadFile(platform, name)
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(staging, name), content, 0o600); err != nil {
				return err
			}
		}
		cmd := exec.CommandContext(ctx, inst.node, inst.npm, "ci", "--ignore-scripts", "--no-audit", "--no-fund", "--loglevel=error")
		cmd.Dir = staging
		cmd.Env = inst.env()
		cmd.Stdout = s.output
		cmd.Stderr = s.output
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("npm ci: %w", err)
		}
		if !complete(staging, depsEssentials) {
			return errors.New("npm ci produced no usable typescript or vite")
		}
		if err := os.RemoveAll(target); err != nil {
			return err
		}
		if err := os.Rename(staging, target); err != nil {
			return err
		}
	}
	return switchCurrent(parent, target)
}
