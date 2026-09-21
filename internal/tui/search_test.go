package tui

import "testing"

func TestFuzzyScore(t *testing.T) {
	score := func(term, field string) int {
		t.Helper()
		got, ok := fuzzyScore([]rune(term), []rune(field))
		if !ok {
			t.Fatalf("%q does not match %q", term, field)
		}
		return got
	}
	for _, tc := range []struct{ term, field string }{
		{"xz", "codex"},     // out of order
		{"codexx", "codex"}, // more characters than the field holds
		{"a", ""},
	} {
		if _, ok := fuzzyScore([]rune(tc.term), []rune(tc.field)); ok {
			t.Errorf("%q matched %q", tc.term, tc.field)
		}
	}
	// Each pair is better, worse: the whole name over a longer one, a run
	// over scattered letters, a word's start over its middle, and the
	// word a term was typed for over earlier letters that happen to fit.
	for _, tc := range []struct{ term, better, worse string }{
		{"api", "api", "apiary"},
		{"api", "apiary", "app-installer"},
		{"co", "my-codex", "account"},
		{"sh", "zsh shell", "zsh-a-h"},
		{"abc", "a b--bc", "a-bxxc"}, // the better b is not the first one
	} {
		if better, worse := score(tc.term, tc.better), score(tc.term, tc.worse); better <= worse {
			t.Errorf("%q: %q scored %d, %q scored %d", tc.term, tc.better, better, tc.worse, worse)
		}
	}
	// The best alignment is found, not the first: `co` in `account codex`
	// scores as the start of `codex`, as it does in `codex account`.
	if first, second := score("co", "account codex"), score("co", "codex account"); first != second {
		t.Errorf("`co` scored %d in `account codex` and %d in `codex account`", first, second)
	}
}
