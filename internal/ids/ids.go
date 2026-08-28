// Package ids mints ATC's public identifiers (ATC-251): a type prefix
// plus a fixed-length 5-character random suffix. The alphabet is
// lowercase, excludes the ambiguous glyphs (0/o, 1/l/i) and all vowels —
// so an ID can never spell a word. Fixed length makes IDs prefix-free,
// which keeps zmx's trailing-* prefix matching safe to type. The format
// is permanent: it appears in un-versioned surfaces (zmx list) and must
// never be reformatted.
package ids

import "crypto/rand"

const (
	// SuffixLength is the fixed random-suffix length of every public ID.
	SuffixLength = 5
	alphabet     = "23456789bcdfghjkmnpqrstvwxyz"
)

// New mints one candidate ID with the given type prefix; the caller
// collision-checks it against the database and re-rolls.
func New(prefix string) string {
	suffix := make([]byte, SuffixLength)
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
