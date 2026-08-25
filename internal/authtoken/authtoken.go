// Package authtoken manages the one bearer credential that gates every API
// request, loopback included (ATC-247 §5). The mechanics are the legacy
// contract adopted wholesale by ATC-259 (tag legacy-product-2026-08,
// authToken.ts): a prefixed 256-bit random token persisted as a 0600 file,
// SSH-host-key style.
//
// Rotation rule: Verify re-reads the file on every check instead of caching,
// so rotation takes effect immediately — the old token stops working without
// a server restart and there is no cache-invalidation machinery to get
// wrong. A missing, unreadable, or malformed file fails closed.
package authtoken

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// format is exactly the shape generate produces (32 base64url bytes =
// 43 chars). A file holding anything else — a restored fragment, a
// hand-typed value — is never adopted as a credential: Ensure refuses it
// with a remedy and Verify fails closed, so weak or corrupt contents cannot
// become a remotely accepted secret. The prefix makes leaks greppable.
var format = regexp.MustCompile(`^atc_[A-Za-z0-9_-]{43}$`)

var bearer = regexp.MustCompile(`(?i)^Bearer\s+(.+)$`)

// Store reads and writes the token file at Path.
type Store struct {
	Path string
}

func generate() string {
	buf := make([]byte, 32)
	rand.Read(buf)
	return "atc_" + base64.RawURLEncoding.EncodeToString(buf)
}

func (s Store) malformed() error {
	return fmt.Errorf("token file %s does not hold a valid token; delete it or run `atc server token rotate`", s.Path)
}

// read returns the trimmed file contents, or "" when the file does not exist.
func (s Store) read() (string, error) {
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// writeTemp fully writes token to a fresh 0600 temp file beside Path and
// returns the temp path. Complete-before-visible is the invariant both
// issuance paths build on: the token only ever reaches Path by an atomic
// install of this finished file, so no reader — and no crash — can observe
// a partial credential at the final path.
func (s Store) writeTemp(token string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Dir(s.Path), filepath.Base(s.Path)+".*.tmp")
	if err != nil {
		return "", err
	}
	temp := f.Name()
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		os.Remove(temp)
		return "", err
	}
	if _, err := f.WriteString(token + "\n"); err != nil {
		f.Close()
		os.Remove(temp)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(temp)
		return "", err
	}
	return temp, nil
}

// Ensure returns the persisted token, generating and persisting one if
// absent. Safe against a concurrent Ensure (server boot racing `atc server
// token` on a fresh install): the candidate is fully written to a temp file
// and installed by hard link — atomic and refusing to replace — so creator
// election stays exclusive without ever exposing a partial file at Path,
// and the loser adopts the winner's token — otherwise each caller could
// mint and hand out a different credential.
func (s Store) Ensure() (string, error) {
	existing, err := s.read()
	if err != nil {
		return "", err
	}
	if existing != "" {
		if !format.MatchString(existing) {
			return "", s.malformed()
		}
		// Re-assert 0600: a copy or restore may have widened the mode, and
		// a world-readable bearer credential defeats the point of one.
		if err := os.Chmod(s.Path, 0o600); err != nil {
			return "", err
		}
		return existing, nil
	}
	token := generate()
	temp, err := s.writeTemp(token)
	if err != nil {
		return "", err
	}
	defer os.Remove(temp)
	if err := os.Link(temp, s.Path); errors.Is(err, fs.ErrExist) {
		// A concurrent Ensure won the install. Adopt its token — it is
		// complete by the invariant above; when it still cannot be read
		// back as valid, fail: returning our own value here would hand out
		// a credential that is not on disk and will never verify.
		winner, err := s.read()
		if err != nil {
			return "", err
		}
		if !format.MatchString(winner) {
			return "", s.malformed()
		}
		return winner, nil
	} else if err != nil {
		return "", err
	}
	return token, nil
}

// Rotate unconditionally reissues the token; the old one stops working at
// once. Atomic replace: the finished temp file renamed over the target, so
// a concurrent Verify never observes a truncated or half-written token and
// the mode is honored unconditionally (it applies at creation of the temp,
// which rename preserves).
func (s Store) Rotate() (string, error) {
	token := generate()
	temp, err := s.writeTemp(token)
	if err != nil {
		return "", err
	}
	if err := os.Rename(temp, s.Path); err != nil {
		os.Remove(temp)
		return "", err
	}
	return token, nil
}

// Verify reports whether an Authorization header value presents the current
// token. It re-reads the file every call (see the rotation rule above) and
// fails closed on any error. The comparison is timing-safe; length is not
// secret (the format is public), so a length mismatch may return early.
func (s Store) Verify(authorization string) bool {
	m := bearer.FindStringSubmatch(authorization)
	if m == nil {
		return false
	}
	token, err := s.read()
	if err != nil || !format.MatchString(token) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(m[1]), []byte(token)) == 1
}
