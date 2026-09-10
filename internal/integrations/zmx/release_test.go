package zmx

// Release-asset validation (ATC-324): the shipped pins must match the real
// published assets on every supported target, so a wrong or drifted
// checksum fails a required job rather than a user's install. This
// downloads each target's archive and proves both recorded sums — the
// archive's and the executable inside it — without running the executable,
// so one host validates all three targets (Linux amd64, Linux arm64,
// macOS arm64). Execution and version are proven per platform by the
// real-zmx lifecycle test. Network-gated exactly like the other real
// tests: required under ATC_SUPERVISOR_TESTS=require, skipped otherwise.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"path"
	"testing"
	"time"
)

func TestPinnedAssetsMatchRealReleases(t *testing.T) {
	if testing.Short() {
		t.Skip("network")
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	for version, release := range releases {
		for target, a := range release.Assets {
			t.Run(version+"/"+target, func(t *testing.T) {
				url := releaseBase + "v" + version + "/" + a.Name
				archive, err := get(t, client, url)
				if err != nil {
					unavailable(t, "zmx release", err.Error())
				}
				if got := sha256hex(archive); got != a.ArchiveSHA256 {
					t.Fatalf("archive %s sha = %s, table records %s", a.Name, got, a.ArchiveSHA256)
				}
				exe, err := readExecutable(archive)
				if err != nil {
					t.Fatalf("reading zmx from %s: %v", a.Name, err)
				}
				if got := sha256hex(exe); got != a.ExecutableSHA256 {
					t.Fatalf("executable in %s sha = %s, table records %s", a.Name, got, a.ExecutableSHA256)
				}
			})
		}
	}
}

func get(t *testing.T, client *http.Client, url string) ([]byte, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// readExecutable returns the zmx member of a release archive, using the
// same one-member rule the installer enforces.
func readExecutable(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("no zmx member")
		}
		if err != nil {
			return nil, err
		}
		if header.Typeflag == tar.TypeReg && path.Base(header.Name) == executableName {
			return io.ReadAll(reader)
		}
	}
}
