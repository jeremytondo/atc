package t3code

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/jeremytondo/atc/internal/threads"
)

// Pending requests (ATC-307, ATC-308). The shell projection says only
// whether a thread has approvals or questions pending; which ones, what
// they ask, and how an answer ATC sent fared is read from the thread
// detail snapshot's activities — the same one-shot read as response
// recovery. A thread reported with either pending, or holding an answer
// ATC still awaits evidence for, is read: one read in flight per thread
// with one more queued behind it, so every report is eventually
// reflected without a read per event, and read again after a pause
// while an answer stays unresolved, since the evidence that settles it
// need not change the shell at all. A thread reported with nothing
// pending and nothing awaited resolves whatever ATC still held, with no
// read at all. Both request kinds and the answer evidence come out of
// one snapshot and go to the threads domain together.

// activity is what ATC reads of a thread activity: its kind, its
// payload — read only for the request kinds — and when it happened.
type activity struct {
	Kind      string          `json:"kind"`
	Summary   string          `json:"summary"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"createdAt"`
}

// The per-thread request read states: none, one running, one running
// with another owed.
const (
	readIdle = iota
	readRunning
	readQueued
)

// observeRequests reflects a thread's pending requests and the evidence
// on its answers: a read when T3 reports something pending or ATC awaits
// evidence, the empty sets right away otherwise — approvals resolve
// right away whenever T3 reports none, whether or not a read follows for
// the rest. Safe from any goroutine; reads run on their own, joined by
// Run.
func (s *Service) observeRequests(ctx context.Context, t3ThreadID string, approvals, inputs bool) {
	s.mu.Lock()
	s.requestReports[t3ThreadID]++
	watched := s.watched[t3ThreadID]
	inflight := s.requestReads[t3ThreadID]
	if approvals || inputs || watched {
		s.requestReads[t3ThreadID] = readQueued
	}
	s.mu.Unlock()
	if !approvals {
		if err := s.threads.ObserveApprovals(ctx, ID, t3ThreadID, nil); err != nil {
			s.logger.Warn("t3code: recording resolved approvals", "thread", t3ThreadID, "error", err)
		}
	}
	if !approvals && !inputs && !watched {
		if _, err := s.threads.ObserveInputs(ctx, ID, t3ThreadID, nil, nil); err != nil {
			s.logger.Warn("t3code: recording resolved questions", "thread", t3ThreadID, "error", err)
		}
		return
	}
	if inflight != readIdle {
		// A read is in flight; it runs once more when it lands.
		return
	}
	s.reads.Add(1)
	go func() {
		defer s.reads.Done()
		for {
			s.mu.Lock()
			s.requestReads[t3ThreadID] = readRunning
			generation := s.requestReports[t3ThreadID]
			// The connection's current credentials: the loop can outlive
			// the connection it started on.
			origin, token := s.origin, s.token
			s.mu.Unlock()
			report, ok := s.readRequests(ctx, origin, token, t3ThreadID)
			s.mu.Lock()
			// A report that arrived meanwhile supersedes what was read: a
			// newer "none pending" must not be undone by an older snapshot,
			// and a newer "some pending" is read again below.
			current := generation == s.requestReports[t3ThreadID]
			s.mu.Unlock()
			if ok && current {
				if err := s.threads.ObserveApprovals(ctx, ID, t3ThreadID, report.approvals); err != nil {
					s.logger.Warn("t3code: recording pending approvals", "thread", t3ThreadID, "error", err)
				}
				unresolved, err := s.threads.ObserveInputs(ctx, ID, t3ThreadID, report.inputs, report.resolutions)
				if err != nil {
					s.logger.Warn("t3code: recording pending questions", "thread", t3ThreadID, "error", err)
				}
				// The domain's word decides the watch; a read that yielded
				// nothing leaves it as it was.
				s.mu.Lock()
				if unresolved {
					s.watched[t3ThreadID] = true
				} else {
					delete(s.watched, t3ThreadID)
				}
				s.mu.Unlock()
			}
			// The decision to go on or to leave is made under the lock a
			// report takes to queue one more read, so no report queued
			// between a look and the leave is lost.
			s.mu.Lock()
			if s.requestReads[t3ThreadID] == readQueued && ctx.Err() == nil {
				s.mu.Unlock()
				continue
			}
			if !s.watched[t3ThreadID] || ctx.Err() != nil {
				delete(s.requestReads, t3ThreadID)
				s.mu.Unlock()
				return
			}
			s.mu.Unlock()
			// The evidence awaited may never touch the shell: read again
			// after a pause, unless a report queues one sooner.
			wait(ctx, s.responseRetry)
			s.mu.Lock()
			if ctx.Err() != nil || (s.requestReads[t3ThreadID] != readQueued && !s.watched[t3ThreadID]) {
				delete(s.requestReads, t3ThreadID)
				s.mu.Unlock()
				return
			}
			s.mu.Unlock()
		}
	}()
}

// requestReport is what one detail read yields.
type requestReport struct {
	approvals   []threads.ApprovalObservation
	inputs      []threads.InputObservation
	resolutions []threads.InputResolution
}

// readRequests reads the detail snapshot once and derives the pending
// requests and answer evidence it shows; false for a failed read, which
// reports nothing — what ATC holds stands until the next report.
func (s *Service) readRequests(ctx context.Context, origin, token, t3ThreadID string) (requestReport, bool) {
	select {
	case s.responseSlots <- struct{}{}:
		defer func() { <-s.responseSlots }()
	case <-ctx.Done():
		return requestReport{}, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/api/orchestration/threads/"+url.PathEscape(t3ThreadID), nil)
	if err != nil {
		return requestReport{}, false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	var detail struct {
		Thread struct {
			Activities []activity `json:"activities"`
		} `json:"thread"`
	}
	if err := doJSONBounded(s.httpClient, req, &detail, maxSnapshotBytes); err != nil {
		s.logger.Debug("t3code: reading a thread's pending requests", "thread", t3ThreadID, "error", err)
		return requestReport{}, false
	}
	inputs, resolutions := pendingInputs(detail.Thread.Activities)
	return requestReport{approvals: pendingApprovals(detail.Thread.Activities), inputs: inputs, resolutions: resolutions}, true
}

// watch marks a thread as holding an answer ATC awaits evidence for and
// starts a read for it, if the Integration is running.
func (s *Service) watch(t3ThreadID string) {
	s.mu.Lock()
	s.watched[t3ThreadID] = true
	ctx := s.runCtx
	thread, known := s.shell.threads[t3ThreadID]
	s.mu.Unlock()
	if ctx == nil || !known {
		return
	}
	s.observeRequests(ctx, t3ThreadID, *thread.HasPendingApprovals, *thread.HasPendingUserInput)
}
