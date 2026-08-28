// Package events is the in-memory change-event hub behind /v1/events
// (ATC-247 §2). It numbers events, keeps a fixed ring of recent ones for
// Last-Event-ID catch-up, and fans live events out to subscribers over
// fixed-size buffers. Memory is bounded everywhere: the ring is fixed, the
// per-subscriber buffers are fixed, and a subscriber that cannot keep up
// is disconnected to catch up over a fresh connection — the notification
// pipe is not durable history.
//
// Sequence numbers restart with the server process at a random base, so a
// cursor from an earlier run cannot alias into the new run's narrow live
// window: it lands outside the backlog and is answered with a resync, the
// same signal as falling behind (the Kubernetes watch/relist contract).
package events

import (
	"crypto/rand"
	"encoding/binary"
	"sync"
)

// Change is one numbered change event: what changed, never the state
// itself.
type Change struct {
	Seq uint64
	// Type is the event name, e.g. "terminal.updated" (api.EventTerminal*).
	Type     string
	Resource string
	ID       string
}

// DefaultBacklog is the production ring size behind /v1/events — enough
// for any realistic reconnect gap at "what changed" granularity, small
// enough to never matter in memory.
const DefaultBacklog = 256

// subscriberBuffer is each subscriber's fixed send buffer. A slow consumer
// overflows it and is dropped, by design.
const subscriberBuffer = 64

// Hub is safe for concurrent use.
type Hub struct {
	mu          sync.Mutex
	ring        []Change // newest-last window of the last cap(ring) events
	capacity    int
	nextSeq     uint64
	subscribers map[chan Change]struct{}
}

// NewHub returns a hub retaining the last backlog events for catch-up.
// Sequences start at a random per-process base (see the package comment).
func NewHub(backlog int) *Hub {
	var buf [8]byte
	rand.Read(buf[:])
	// Confined well under 2^53 so a sequence survives JSON number decoding
	// in any future client, with headroom that no run ever overflows.
	base := binary.BigEndian.Uint64(buf[:])%(1<<50) + 1<<20
	return NewHubAt(backlog, base)
}

// NewHubAt pins the first sequence number — deterministic hubs for tests.
func NewHubAt(backlog int, firstSeq uint64) *Hub {
	return &Hub{
		ring:        make([]Change, 0, backlog),
		capacity:    backlog,
		nextSeq:     firstSeq,
		subscribers: make(map[chan Change]struct{}),
	}
}

// Publish numbers and emits one change event. Subscribers whose buffer is
// full are disconnected (their channel closes).
func (h *Hub) Publish(eventType, resource, id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	change := Change{Seq: h.nextSeq, Type: eventType, Resource: resource, ID: id}
	h.nextSeq++
	if len(h.ring) == h.capacity {
		copy(h.ring, h.ring[1:])
		h.ring[len(h.ring)-1] = change
	} else {
		h.ring = append(h.ring, change)
	}
	for subscriber := range h.subscribers {
		select {
		case subscriber <- change:
		default:
			delete(h.subscribers, subscriber)
			close(subscriber)
		}
	}
}

// Subscription is one subscriber's view: any backlog replay decided at
// subscribe time, whether the cursor required a resync, and the live
// channel. C closes when the hub drops the subscriber for falling behind.
type Subscription struct {
	// Resync is set when the cursor fell off the backlog (or came from an
	// earlier server run): the client must refetch snapshots once.
	Resync bool
	// Head is the last sequence number published before subscribing — what
	// a resync event reports.
	Head   uint64
	Replay []Change
	C      <-chan Change
	send   chan Change
	hub    *Hub
}

// Subscribe registers a subscriber. after is the client's Last-Event-ID
// cursor; hasCursor false means a fresh connection (no replay, no resync —
// the client fetches snapshots after connecting). Replay and registration
// happen atomically, so no event is missed between them.
func (h *Hub) Subscribe(after uint64, hasCursor bool) *Subscription {
	h.mu.Lock()
	defer h.mu.Unlock()
	send := make(chan Change, subscriberBuffer)
	subscription := &Subscription{C: send, send: send, hub: h, Head: h.nextSeq - 1}
	if hasCursor {
		oldest := h.nextSeq - uint64(len(h.ring))
		newest := h.nextSeq - 1
		switch {
		case after > newest || after+1 < oldest:
			subscription.Resync = true
		case after < newest:
			replay := h.ring[len(h.ring)-int(newest-after):]
			subscription.Replay = append([]Change(nil), replay...)
		}
	}
	h.subscribers[send] = struct{}{}
	return subscription
}

// Close unregisters the subscriber. Safe to call after the hub already
// dropped it.
func (s *Subscription) Close() {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	if _, ok := s.hub.subscribers[s.send]; ok {
		delete(s.hub.subscribers, s.send)
		close(s.send)
	}
}
