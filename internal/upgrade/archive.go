package upgrade

// Checksum verification and archive extraction: pure functions over the
// downloaded bytes, fully tested.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// verifyChecksum checks the downloaded archive against its entry in
// GoReleaser's checksums.txt (lines of "<sha256>  <asset>").
func verifyChecksum(archive, checksums []byte, asset string) error {
	want, err := expectedChecksum(checksums, asset)
	if err != nil {
		return err
	}
	got := fmt.Sprintf("%x", sha256.Sum256(archive))
	if got != want {
		return fmt.Errorf("checksum mismatch for %s: checksums.txt says %s, the download is %s", asset, want, got)
	}
	return nil
}

func expectedChecksum(checksums []byte, asset string) (string, error) {
	for line := range strings.Lines(string(checksums)) {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == asset {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("%s has no entry in checksums.txt", asset)
}

// extractBinary returns the atc executable member of a release tar.gz.
func extractBinary(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("bad release archive: %w", err)
	}
	defer func() { _ = gz.Close() }()
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("release archive has no %q member", binaryName)
		}
		if err != nil {
			return nil, fmt.Errorf("bad release archive: %w", err)
		}
		if header.Typeflag == tar.TypeReg && path.Base(header.Name) == binaryName {
			binary, err := io.ReadAll(reader)
			if err != nil {
				return nil, fmt.Errorf("bad release archive: %w", err)
			}
			return binary, nil
		}
	}
}
