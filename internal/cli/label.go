package cli

import (
	"fmt"

	"github.com/jeremytondo/atc/internal/api"
)

// Label is the row label every surface shows for a terminal (ATC-317):
// its number in the list it appears in, then the user-set name or, for
// an unnamed terminal, the program observed in its foreground — tmux's
// `1:zsh`, `4:api`. The number is a property of the list, not of the
// terminal: creation order within a space, contiguous from 1, so the API
// never serves it and every surface numbers the rows it shows.
func Label(number int, terminal api.Terminal) string {
	return fmt.Sprintf("%d:%s", number, DisplayName(terminal))
}

// DisplayName is the label without its number: the user-set name, else
// the observed process.
func DisplayName(terminal api.Terminal) string {
	if terminal.Name != "" {
		return terminal.Name
	}
	return terminal.Process
}

// GroupBySpace orders terminals for a numbered listing: grouped by space
// in the order spaces first appear, creation order within each group —
// the order the API already lists, partitioned. Numbering each group
// from 1 gives the label the picker shows for the same terminal.
func GroupBySpace(terminals []api.Terminal) [][]api.Terminal {
	index := map[string]int{}
	var groups [][]api.Terminal
	for _, terminal := range terminals {
		i, ok := index[terminal.SpaceID]
		if !ok {
			i = len(groups)
			index[terminal.SpaceID] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], terminal)
	}
	return groups
}
