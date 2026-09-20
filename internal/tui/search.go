package tui

// Search (ATC-329): the terminal screen's fuzzy filter. A query is
// whitespace-separated terms; a row matches when every term is found in
// its Terminal's displayed name, its Space's name, or its connection's
// name — each term in whichever field suits it best, so `work codex`
// finds Terminal `codex` on connection `workstation`. A term is found in
// a field when its characters appear there in order, case ignored and
// characters skipped freely. Matches are ranked by how tightly they were
// found; rows that tie keep their normal order.

import (
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/jeremytondo/atc/internal/cli"
)

// The score of one term in one field. A character that starts a word is
// worth more than one that continues a run, which is worth more than one
// found after a skip; skipped characters inside the match cost, and the
// field's unmatched remainder breaks ties toward the shorter name without
// ever outweighing how the characters were found.
const (
	wordStartScore   = 30
	runScore         = 20
	skipPenalty      = 5
	maxLengthPenalty = 9
)

// searchRows is rows under query: all of them, in order, for an empty
// query; otherwise the matches, best first.
func searchRows(rows []terminalRow, query string) []terminalRow {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return rows
	}
	matched := make([]terminalRow, 0, len(rows))
	scores := map[terminalRef]int{}
	for _, row := range rows {
		if score, ok := rowScore(terms, row); ok {
			matched = append(matched, row)
			scores[row.ref()] = score
		}
	}
	sort.SliceStable(matched, func(i, j int) bool {
		return scores[matched[i].ref()] > scores[matched[j].ref()]
	})
	return matched
}

// rowScore sums each term's best field score; a term no field holds
// fails the row.
func rowScore(terms []string, row terminalRow) (int, bool) {
	fields := [][]rune{
		[]rune(strings.ToLower(cli.DisplayName(row.terminal))),
		[]rune(strings.ToLower(row.space.Name)),
		[]rune(strings.ToLower(row.conn)),
	}
	total := 0
	for _, term := range terms {
		best, found := 0, false
		for _, field := range fields {
			if score, ok := fuzzyScore([]rune(term), field); ok && (!found || score > best) {
				best, found = score, true
			}
		}
		if !found {
			return 0, false
		}
		total += best
	}
	return total, true
}

// fuzzyScore reports whether term's characters appear in field in order,
// and the score of the best way to place them. Both are already
// lowercase. The best placement, not the first, is what finds the word a
// term was typed for rather than earlier scattered letters that happen to
// fit; fields are names, so trying every placement costs nothing.
func fuzzyScore(term, field []rune) (int, bool) {
	const none = math.MinInt
	// placed[j] is the best score of the term so far with its last
	// character at field[j].
	var placed []int
	for i, r := range term {
		next := make([]int, len(field))
		for j := range next {
			next[j] = none
			if field[j] != r {
				continue
			}
			wordStart := j == 0 || !isWordRune(field[j-1])
			if i == 0 {
				next[j] = 0
				if wordStart {
					next[j] = wordStartScore
				}
				continue
			}
			for k := range j {
				if placed[k] == none {
					continue
				}
				score := placed[k] - (j-k-1)*skipPenalty
				switch {
				case wordStart:
					score += wordStartScore
				case k == j-1:
					score += runScore
				}
				next[j] = max(next[j], score)
			}
		}
		placed = next
	}
	best := none
	for _, score := range placed {
		best = max(best, score)
	}
	if best == none {
		return 0, false
	}
	return best - min(len(field)-len(term), maxLengthPenalty), true
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
