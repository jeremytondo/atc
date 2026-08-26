package upgrade

// Release discovery and download. Tokenless by design: the public
// releases/latest redirect names the production channel head, and asset
// URLs are static per tag. No GitHub API.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// assetURL is the tokenless download URL for one asset of one release.
func assetURL(tag, asset string) string {
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, tag, asset)
}

// assetName maps a platform to its release archive, mirroring the
// .goreleaser.yaml name_template. Unsupported platforms fail with a named
// diagnostic, never a bare 404.
func assetName(goos, goarch string) (string, error) {
	switch goos + "/" + goarch {
	case "darwin/arm64", "linux/amd64", "linux/arm64":
		return fmt.Sprintf("%s_%s_%s.tar.gz", binaryName, goos, goarch), nil
	}
	return "", fmt.Errorf("no atc release build for %s/%s (supported: darwin/arm64, linux/amd64, linux/arm64)", goos, goarch)
}

// latestProductionTag resolves the production channel head from the
// releases/latest redirect — GitHub's own rolling "latest non-prerelease"
// pointer, so the dev prerelease can never be selected.
func latestProductionTag(ctx context.Context) (string, error) {
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	address := fmt.Sprintf("https://github.com/%s/releases/latest", repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("cannot reach github.com to find the latest release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return tagFromLocation(resp.Header.Get("Location"))
}

// tagFromLocation extracts the release tag from the releases/latest
// redirect target, e.g. .../releases/tag/v0.1.0 → v0.1.0.
func tagFromLocation(location string) (string, error) {
	parsed, err := url.Parse(location)
	if err != nil {
		return "", fmt.Errorf("unexpected releases/latest redirect %q: %w", location, err)
	}
	dir, tag := path.Split(parsed.Path)
	if !strings.HasSuffix(dir, "/releases/tag/") || tag == "" {
		return "", fmt.Errorf("no production release found (releases/latest redirected to %q)", location)
	}
	return tag, nil
}

// fetch downloads one release asset fully into memory (release archives
// are a few megabytes).
func fetch(ctx context.Context, address string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%s does not exist — has this release been published?", address)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download of %s failed: HTTP %d", address, resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("download of %s failed: %w", address, err)
	}
	return data, nil
}
