package terminals

import "crypto/rand"

// The ATC-wide public ID scheme (ATC-251): type prefix plus a fixed-length
// 5-character random suffix. The alphabet is lowercase, excludes the
// ambiguous glyphs (0/o, 1/l/i) and all vowels — so an ID can never spell
// a word. Fixed length makes IDs prefix-free, which keeps zmx's trailing-*
// prefix matching safe to type. The format is permanent: it appears in
// un-versioned surfaces (zmx list) and must never be reformatted.
const (
	idPrefix       = "term-"
	idSuffixLength = 5
	idAlphabet     = "23456789bcdfghjkmnpqrstvwxyz"
)

// randomID mints one candidate ID; the caller collision-checks it against
// the database and re-rolls.
func randomID() string {
	suffix := make([]byte, idSuffixLength)
	// Rejection sampling keeps the distribution uniform: 256 is not a
	// multiple of 28, so bytes past the largest full multiple re-roll.
	limit := byte(256 - 256%len(idAlphabet))
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
			suffix[i] = idAlphabet[int(b)%len(idAlphabet)]
			i++
		}
	}
	return idPrefix + string(suffix)
}
