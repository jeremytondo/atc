package events

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func publishN(h *Hub, n int) {
	for i := 1; i <= n; i++ {
		h.Publish("terminal.updated", "terminal", fmt.Sprintf("term-%05d", i))
	}
}

func TestSubscribeFreshHasNoReplayOrResync(t *testing.T) {
	h := NewHubAt(4, 1)
	publishN(h, 3)
	sub := h.Subscribe(0, false)
	defer sub.Close()
	if sub.Resync || len(sub.Replay) != 0 {
		t.Errorf("fresh subscribe: resync=%v replay=%d, want false/0", sub.Resync, len(sub.Replay))
	}
	if sub.Head != 3 {
		t.Errorf("Head = %d, want 3", sub.Head)
	}
}

func TestCursorCatchUpFromBacklog(t *testing.T) {
	h := NewHubAt(8, 1)
	publishN(h, 5)
	sub := h.Subscribe(3, true)
	defer sub.Close()
	if sub.Resync {
		t.Fatal("cursor inside backlog must not resync")
	}
	want := []Change{
		{Seq: 4, Type: "terminal.updated", Resource: "terminal", ID: "term-00004"},
		{Seq: 5, Type: "terminal.updated", Resource: "terminal", ID: "term-00005"},
	}
	if diff := cmp.Diff(want, sub.Replay); diff != "" {
		t.Errorf("replay (-want +got):\n%s", diff)
	}
}

func TestCursorAtHeadReplaysNothing(t *testing.T) {
	h := NewHubAt(8, 1)
	publishN(h, 5)
	sub := h.Subscribe(5, true)
	defer sub.Close()
	if sub.Resync || len(sub.Replay) != 0 {
		t.Errorf("cursor at head: resync=%v replay=%d", sub.Resync, len(sub.Replay))
	}
}

func TestCursorOffBacklogResyncs(t *testing.T) {
	h := NewHubAt(4, 1)
	publishN(h, 10) // ring holds 7..10
	for name, after := range map[string]uint64{
		"fallen behind":    2,
		"future (old run)": 99,
	} {
		sub := h.Subscribe(after, true)
		if !sub.Resync {
			t.Errorf("%s: cursor %d should resync", name, after)
		}
		if len(sub.Replay) != 0 {
			t.Errorf("%s: resync must not also replay", name)
		}
		if sub.Head != 10 {
			t.Errorf("%s: Head = %d, want 10", name, sub.Head)
		}
		sub.Close()
	}
	// The oldest retained event is still reachable without a resync.
	sub := h.Subscribe(6, true)
	defer sub.Close()
	if sub.Resync || len(sub.Replay) != 4 {
		t.Errorf("cursor at ring edge: resync=%v replay=%d, want false/4", sub.Resync, len(sub.Replay))
	}
}

func TestLiveDelivery(t *testing.T) {
	h := NewHubAt(4, 1)
	sub := h.Subscribe(0, false)
	defer sub.Close()
	h.Publish("terminal.created", "terminal", "term-aaaaa")
	got := <-sub.C
	want := Change{Seq: 1, Type: "terminal.created", Resource: "terminal", ID: "term-aaaaa"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("live event (-want +got):\n%s", diff)
	}
}

// A subscriber that stops draining is dropped once its fixed buffer fills;
// its channel closes and later subscribers are unaffected.
func TestSlowSubscriberIsDropped(t *testing.T) {
	h := NewHubAt(4, 1)
	slow := h.Subscribe(0, false)
	publishN(h, subscriberBuffer+1)
	if _, open := <-slow.C; !open {
		t.Fatal("first buffered event should still be readable")
	}
	// Drain; the channel must be closed by the hub.
	open := true
	for range subscriberBuffer + 1 {
		if _, open = <-slow.C; !open {
			break
		}
	}
	if open {
		t.Error("slow subscriber channel never closed")
	}
	// Closing after the hub already dropped it must not panic.
	slow.Close()
}

// A cursor from a previous server run must always resync: each process
// numbers events from a random base, so an old cursor cannot alias into
// the new run's live window.
func TestOldRunCursorResyncs(t *testing.T) {
	h := NewHub(4)
	publishN(h, 3)
	sub := h.Subscribe(7, true) // a plausible cursor from an earlier run
	defer sub.Close()
	if !sub.Resync {
		t.Error("old-run cursor did not resync")
	}
	if h.nextSeq <= 1<<20 {
		t.Errorf("random base = %d, suspiciously low", h.nextSeq)
	}
}
