package codexbridge

import "testing"

func TestTUIArgumentsRouteThroughPrivateAppServer(t *testing.T) {
	got := tuiArguments("/tmp/project", "unix:///tmp/status.sock", []string{"--model", "gpt-test"})
	want := []string{
		"--cd", "/tmp/project", "--remote", "unix:///tmp/status.sock",
		"--model", "gpt-test",
	}
	if len(got) != len(want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("arguments = %#v, want %#v", got, want)
		}
	}
}

func TestOnlyStatusEvidenceIsPersisted(t *testing.T) {
	for _, method := range []string{"thread/started", "thread/status/changed"} {
		if !recordsStatus(method) {
			t.Fatalf("status method %q was rejected", method)
		}
	}
	for _, method := range []string{"item/completed", "item/agentMessage/delta", "turn/completed"} {
		if recordsStatus(method) {
			t.Fatalf("conversation method %q would be persisted", method)
		}
	}
}
