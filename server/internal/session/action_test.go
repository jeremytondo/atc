package session

import (
	"reflect"
	"testing"

	"github.com/jeremytondo/atc/internal/store"
)

func TestActionCommandUsesLiteralArgs(t *testing.T) {
	action := store.Action{
		Command: "tool",
		Args:    []string{"$HOME", "$(touch nope)", "{{prompt}}", "two words"},
	}
	want := []string{"tool", "$HOME", "$(touch nope)", "{{prompt}}", "two words"}
	if got := actionCommand(action); !reflect.DeepEqual(got, want) {
		t.Fatalf("actionCommand = %#v, want %#v", got, want)
	}
}
