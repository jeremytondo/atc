package session

import (
	"reflect"
	"testing"
)

func TestActionLaunchCommandQuotesLiteralTokens(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	got := actionLaunchCommand([]string{"tool", "--label", "two words", "$HOME", "it's"})
	want := []string{"/bin/zsh", "-l", "-i", "-c", `tool --label 'two words' '$HOME' 'it'\''s'`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("actionLaunchCommand = %#v, want %#v", got, want)
	}
}

func TestInteractiveShellCommand(t *testing.T) {
	t.Setenv("SHELL", "/bin/fish")
	want := []string{"/bin/fish", "-l", "-i"}
	if got := interactiveShellCommand(); !reflect.DeepEqual(got, want) {
		t.Fatalf("interactiveShellCommand = %#v, want %#v", got, want)
	}
}
