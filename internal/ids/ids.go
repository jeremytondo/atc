// Package ids mints ATC's public identifiers (ATC-251): a type prefix
// plus a fixed-length 5-character random suffix. The alphabet is
// lowercase, excludes the ambiguous glyphs (0/o, 1/l/i) and all vowels —
// so an ID can never spell a word. Fixed length makes IDs prefix-free,
// which keeps zmx's trailing-* prefix matching safe to type. The format
// is permanent: it appears in un-versioned surfaces (zmx list) and must
// never be reformatted. NewLong is the exception, for identifiers a
// person never types (ATC-301).
package ids

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
)

const (
	// SuffixLength is the fixed random-suffix length of every ID a person
	// types.
	SuffixLength = 5
	// longSuffixLength is the suffix length of an ID a person never types
	// (a turn): 10 characters keep a duplicate improbable past
	// twenty million mints, which covers the kinds no collision check can
	// (a thread's latest turn keeps no history). Such IDs are never typed
	// into zmx, so the fixed-length rule above does not bind them.
	longSuffixLength = 10
	alphabet         = "23456789bcdfghjkmnpqrstvwxyz"
)

// New mints one candidate ID with the given type prefix; the caller
// collision-checks it against the database and re-rolls.
func New(prefix string) string {
	return mint(prefix, SuffixLength)
}

// NewLong mints an ID with a 10-character suffix, for identifiers a
// person never types.
func NewLong(prefix string) string {
	return mint(prefix, longSuffixLength)
}

func mint(prefix string, length int) string {
	suffix := make([]byte, length)
	// Rejection sampling keeps the distribution uniform: 256 is not a
	// multiple of 28, so bytes past the largest full multiple re-roll.
	limit := byte(256 - 256%len(alphabet))
	for i := 0; i < len(suffix); {
		var buf [16]byte
		rand.Read(buf[:])
		for _, b := range buf {
			if i == len(suffix) {
				break
			}
			if b >= limit {
				continue
			}
			suffix[i] = alphabet[int(b)%len(alphabet)]
			i++
		}
	}
	return prefix + string(suffix)
}

// UUID mints a random (version 4) UUID, for identifiers a provider
// expects in that form (T3 Code's thread and command ids).
func UUID() string {
	var b [16]byte
	rand.Read(b[:])
	return uuidOf(b)
}

// UUIDFrom derives a UUID-shaped identifier from a key, the same for the
// same key: for a provider that deduplicates on the id (Linear's agent
// activities), a retry after a lost answer presents the identity the
// first attempt did, however long ago that was.
func UUIDFrom(key string) string {
	sum := sha256.Sum256([]byte(key))
	var b [16]byte
	copy(b[:], sum[:16])
	return uuidOf(b)
}

func uuidOf(b [16]byte) string {
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
