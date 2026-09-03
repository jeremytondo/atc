// Package ids mints ATC's public identifiers (ATC-251): a type prefix
// plus a fixed-length 5-character random suffix. The alphabet is
// lowercase, excludes the ambiguous glyphs (0/o, 1/l/i) and all vowels —
// so an ID can never spell a word. Fixed length makes IDs prefix-free,
// which keeps zmx's trailing-* prefix matching safe to type. The format
// is permanent: it appears in un-versioned surfaces (zmx list) and must
// never be reformatted. NewLong is the one exception, for identifiers
// that are never typed and never collision-checked (ATC-301).
package ids

import "crypto/rand"

const (
	// SuffixLength is the fixed random-suffix length of every ID a person
	// types.
	SuffixLength = 5
	// longSuffixLength is the suffix length of an ID that is never
	// collision-checked because no history of its kind is kept (a thread's
	// latest turn): 10 characters keep a duplicate improbable past twenty
	// million mints. Such IDs are never typed into zmx, so the fixed-length
	// rule above does not bind them.
	longSuffixLength = 10
	alphabet         = "23456789bcdfghjkmnpqrstvwxyz"
)

// New mints one candidate ID with the given type prefix; the caller
// collision-checks it against the database and re-rolls.
func New(prefix string) string {
	return mint(prefix, SuffixLength)
}

// NewLong mints an ID with a 10-character suffix, for identifiers no
// collision check can cover.
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
