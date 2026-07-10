// Package publicid generates opaque Atelier Code-owned public identifiers.
package publicid

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"strings"
)

// New generates an opaque public id with the given prefix. It uses 128 bits of
// entropy encoded as lowercase base32hex without padding, keeping ids URL-safe
// and independent from any external naming scheme.
func New(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate %sid: %w", prefix, err)
	}
	enc := base32.HexEncoding.WithPadding(base32.NoPadding)
	return prefix + strings.ToLower(enc.EncodeToString(b[:])), nil
}
