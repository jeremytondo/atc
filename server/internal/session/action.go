package session

import "github.com/jeremytondo/atc/internal/store"

// actionCommand returns the Action's fixed literal argv. No request data is
// interpolated into an Action command.
func actionCommand(action store.Action) []string {
	return append([]string{action.Command}, action.Args...)
}
