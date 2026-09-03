package server

import (
	"context"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/sse"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/events"
)

// defaultHeartbeatInterval paces SSE comment heartbeats; a client that
// stops hearing them reconnects (ATC-247 §2).
const defaultHeartbeatInterval = 15 * time.Second

// withWriteDeadlines exposes per-write deadlines on the response writer
// through http.ResponseController. Huma's SSE sender looks for exactly
// this: without it every send warns on stderr and a stalled client can
// block a write forever; with it a send that cannot complete within the
// SSE write timeout fails and ends that client's stream.
//
// The wrapper also records write failures, reachable from the request
// context: Huma's comment path swallows write errors, so a stalled client
// receiving only heartbeats would otherwise keep its handler and hub
// subscription alive forever.
func withWriteDeadlines(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wrapped := &deadlineWriter{ResponseWriter: w, controller: http.NewResponseController(w)}
		ctx := context.WithValue(r.Context(), writerStateKey{}, wrapped)
		next.ServeHTTP(wrapped, r.WithContext(ctx))
	})
}

type writerStateKey struct{}

type deadlineWriter struct {
	http.ResponseWriter
	controller *http.ResponseController
	failed     atomic.Bool
}

func (w *deadlineWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if err != nil {
		w.failed.Store(true)
	}
	return n, err
}

func (w *deadlineWriter) SetWriteDeadline(deadline time.Time) error {
	return w.controller.SetWriteDeadline(deadline)
}

// Unwrap lets flusher discovery (and ResponseController itself) reach the
// underlying writer.
func (w *deadlineWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// writeFailed reports whether any response write on this request has
// failed — the disconnect signal Huma's comment sends do not surface.
func writeFailed(ctx context.Context) bool {
	wrapped, ok := ctx.Value(writerStateKey{}).(*deadlineWriter)
	return ok && wrapped.failed.Load()
}

type eventsInput struct {
	LastEventID string `header:"Last-Event-ID" doc:"Sequence number of the last event received, for catch-up after a reconnect."`
}

// registerEvents serves the one global SSE feed. Reconnect is the core of
// the design: Last-Event-ID catch-up against the hub's backlog, a resync
// event when the cursor has fallen off it, heartbeats between events, and
// disconnection of clients that cannot keep up (they catch up on
// reconnect).
func registerEvents(humaAPI huma.API, hub *events.Hub, heartbeat time.Duration) {
	sse.Register(humaAPI, huma.Operation{
		OperationID: "get-events",
		Method:      http.MethodGet,
		Path:        "/v1/events",
		Summary:     "Change-event stream",
		Description: "Numbered change events saying what changed, never the state itself; clients refetch the changed resource. Comment lines are heartbeats.",
	}, eventSchemas(), func(ctx context.Context, input *eventsInput, send sse.Sender) {
		after, hasCursor := uint64(0), false
		if input.LastEventID != "" {
			parsed, err := strconv.ParseUint(input.LastEventID, 10, 64)
			if err != nil {
				// A cursor we cannot read is a cursor we cannot honor:
				// same defined signal as falling off the backlog.
				parsed = ^uint64(0)
			}
			after, hasCursor = parsed, true
		}
		subscription := hub.Subscribe(after, hasCursor)
		defer subscription.Close()

		// The opening comment commits the stream so clients observe "open"
		// (and can start a resync fetch) before the first event exists.
		if err := send.Comment("connected"); err != nil {
			return
		}
		if subscription.Resync {
			if err := send(sse.Message{ID: int(subscription.Head), Data: api.ResyncEvent{Seq: subscription.Head}}); err != nil {
				return
			}
		}
		for _, change := range subscription.Replay {
			if err := sendChange(send, change); err != nil {
				return
			}
		}

		ticker := time.NewTicker(heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Comment sends swallow write errors in Huma's SSE path;
				// the recorded writer state is what ends a stalled
				// heartbeat-only stream.
				if err := send.Comment("heartbeat"); err != nil || writeFailed(ctx) {
					return
				}
			case change, open := <-subscription.C:
				if !open {
					// Dropped by the hub for falling behind; the client
					// reconnects and catches up.
					return
				}
				if err := sendChange(send, change); err != nil || writeFailed(ctx) {
					return
				}
			}
		}
	})
}

// changeEvents is the one registry of change-event names to their
// payload constructors: it feeds both the SSE schema map (the document)
// and the encoder (the wire), so a name cannot be documented without
// being sent or sent without being documented.
var changeEvents = map[string]func(api.ChangeEvent) any{
	api.EventTerminalCreated:    func(c api.ChangeEvent) any { return api.TerminalCreatedEvent{ChangeEvent: c} },
	api.EventTerminalUpdated:    func(c api.ChangeEvent) any { return api.TerminalUpdatedEvent{ChangeEvent: c} },
	api.EventTerminalDeleted:    func(c api.ChangeEvent) any { return api.TerminalDeletedEvent{ChangeEvent: c} },
	api.EventSpaceCreated:       func(c api.ChangeEvent) any { return api.SpaceCreatedEvent{ChangeEvent: c} },
	api.EventSpaceUpdated:       func(c api.ChangeEvent) any { return api.SpaceUpdatedEvent{ChangeEvent: c} },
	api.EventSpaceDeleted:       func(c api.ChangeEvent) any { return api.SpaceDeletedEvent{ChangeEvent: c} },
	api.EventProjectCreated:     func(c api.ChangeEvent) any { return api.ProjectCreatedEvent{ChangeEvent: c} },
	api.EventProjectUpdated:     func(c api.ChangeEvent) any { return api.ProjectUpdatedEvent{ChangeEvent: c} },
	api.EventProjectDeleted:     func(c api.ChangeEvent) any { return api.ProjectDeletedEvent{ChangeEvent: c} },
	api.EventThreadCreated:      func(c api.ChangeEvent) any { return api.ThreadCreatedEvent{ChangeEvent: c} },
	api.EventThreadUpdated:      func(c api.ChangeEvent) any { return api.ThreadUpdatedEvent{ChangeEvent: c} },
	api.EventThreadDeleted:      func(c api.ChangeEvent) any { return api.ThreadDeletedEvent{ChangeEvent: c} },
	api.EventIntegrationUpdated: func(c api.ChangeEvent) any { return api.IntegrationUpdatedEvent{ChangeEvent: c} },
}

// eventSchemas is the SSE registration map: every change event's payload
// type, plus resync.
func eventSchemas() map[string]any {
	schemas := map[string]any{api.EventResync: api.ResyncEvent{}}
	for name, payload := range changeEvents {
		schemas[name] = payload(api.ChangeEvent{})
	}
	return schemas
}

func sendChange(send sse.Sender, change events.Change) error {
	payload, ok := changeEvents[change.Type]
	if !ok {
		// A type outside the registry would panic Huma's type lookup; the
		// hub only carries names the domains publish, all registered.
		return nil
	}
	return send(sse.Message{ID: int(change.Seq), Data: payload(api.ChangeEvent{Seq: change.Seq, Resource: change.Resource, ID: change.ID})})
}
