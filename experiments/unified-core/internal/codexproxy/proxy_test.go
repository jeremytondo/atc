package codexproxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTrackerCorrelatesRootFromTUIWriterAndFiltersBroadcasts(t *testing.T) {
	tracker := newTracker()
	tracker.client([]byte(`{"id":17,"method":"thread/start","params":{}}`))
	if _, ok := tracker.server([]byte(`{"method":"thread/status/changed","params":{"threadId":"unrelated","status":{"type":"active"}}}`)); ok {
		t.Fatal("forwarded a broadcast before exact root correlation")
	}
	evidence, ok := tracker.server([]byte(`{"id":17,"result":{"thread":{"id":"root-1"}}}`))
	if !ok || !strings.Contains(string(evidence), `"atcExactRoot":"root-1"`) {
		t.Fatalf("root evidence = %s, %v", evidence, ok)
	}
	if _, ok := tracker.server([]byte(`{"method":"thread/status/changed","params":{"threadId":"unrelated","status":{"type":"active"}}}`)); ok {
		t.Fatal("forwarded unrelated root after correlation")
	}
	child, ok := tracker.server([]byte(`{"method":"item/started","params":{"threadId":"root-1","item":{"type":"subAgentActivity","kind":"started","agentThreadId":"child-1"}}}`))
	if !ok {
		t.Fatal("did not forward exact root subagent evidence")
	}
	assertExactRoot(t, child, "root-1")
	descendant, ok := tracker.server([]byte(`{"method":"thread/status/changed","params":{"threadId":"child-1","status":{"type":"active"}}}`))
	if !ok {
		t.Fatal("did not forward correlated descendant")
	}
	assertExactRoot(t, descendant, "root-1")
}

func TestTrackerSupportsStringRequestIDsAndResume(t *testing.T) {
	tracker := newTracker()
	tracker.client([]byte(`{"id":"resume-1","method":"thread/resume"}`))
	evidence, ok := tracker.server([]byte(`{"id":"resume-1","result":{"threadId":"saved-root"}}`))
	if !ok {
		t.Fatal("resume response was not correlated")
	}
	assertExactRoot(t, evidence, "saved-root")
}

func TestTrackerFollowsInProcessTUIThreadChanges(t *testing.T) {
	tracker := newTracker()
	tracker.client([]byte(`{"id":1,"method":"thread/start"}`))
	started, ok := tracker.server([]byte(`{"id":1,"result":{"thread":{"id":"root-one"}}}`))
	if !ok {
		t.Fatal("initial thread was not correlated")
	}
	assertTransition(t, started, "root-one", "start")

	tracker.client([]byte(`{"id":2,"method":"thread/fork"}`))
	forked, ok := tracker.server([]byte(`{"id":2,"result":{"thread":{"id":"root-two"}}}`))
	if !ok {
		t.Fatal("fork was not correlated")
	}
	assertTransition(t, forked, "root-two", "fork")
	if _, ok := tracker.server([]byte(`{"method":"thread/status/changed","params":{"threadId":"root-one","status":{"type":"active"}}}`)); ok {
		t.Fatal("forwarded the previously active root after a TUI switch")
	}
	current, ok := tracker.server([]byte(`{"method":"thread/status/changed","params":{"threadId":"root-two","status":{"type":"active"}}}`))
	if !ok {
		t.Fatal("did not forward the newly active root")
	}
	assertExactRoot(t, current, "root-two")
}

func assertExactRoot(t *testing.T, payload []byte, expected string) {
	t.Helper()
	var decoded struct {
		Root string `json:"atcExactRoot"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil || decoded.Root != expected {
		t.Fatalf("exact root payload = %s, %v", payload, err)
	}
}

func assertTransition(t *testing.T, payload []byte, root, transition string) {
	t.Helper()
	var decoded struct {
		Root       string `json:"atcExactRoot"`
		Transition string `json:"atcThreadTransition"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil || decoded.Root != root || decoded.Transition != transition {
		t.Fatalf("transition payload = %s, %v", payload, err)
	}
}
