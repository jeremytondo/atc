package terminals

import "github.com/jeremytondo/atc/internal/ids"

// Terminal IDs use the ATC-wide public ID scheme (internal/ids) with the
// terminal type prefix.
const (
	idPrefix = "term-"
	// IDLength is the fixed byte length of every terminal ID — what the
	// zmx driver budgets socket paths against.
	IDLength = len(idPrefix) + ids.SuffixLength
)

// randomID mints one candidate ID; the caller collision-checks it against
// the database and re-rolls.
func randomID() string {
	return ids.New(idPrefix)
}
