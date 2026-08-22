// Package terminalname owns the private zmx namespace. Session names stay
// deliberately short because zmx's limit shrinks as its socket path grows.
package terminalname

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const (
	Prefix       = "atcu-"
	LegacyPrefix = "atc-unified-"
)

func FromID(terminalID string) string {
	digest := sha256.Sum256([]byte(terminalID))
	return Prefix + hex.EncodeToString(digest[:8])
}

func IsManaged(name string) bool {
	return strings.HasPrefix(name, Prefix) || strings.HasPrefix(name, LegacyPrefix)
}
