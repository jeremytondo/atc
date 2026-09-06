package linear

import (
	"strings"
	"testing"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/threads"
)

// A final response Linear refuses for good still ends the session
// honestly: a short error names the refusal and points to T3.
func TestRefusedResponseFallsBackToAnError(t *testing.T) {
	f := newFixture(t)
	f.linear.set(func(l *fakeLinear) { l.refuseBodies = "far too long" })
	f.start()
	f.process(t, "dlv-1", createdEvent("sess-1", f.clock.Now()))
	_, providerID := f.startedThread("sess-1")
	f.waitActivities("sess-1", 2)
	f.report(providerID, api.ThreadIdle, &threads.TurnObservation{ProviderID: "t3-turn-1", State: api.TurnCompleted, Response: "an answer far too long for Linear"})
	acts := f.waitActivities("sess-1", 3)
	if acts[2].Type != contentError || !strings.Contains(acts[2].Body, "Linear refused the response") || !strings.Contains(acts[2].Body, "https://t3.test/env-1/"+providerID) {
		t.Errorf("fallback = %+v", acts[2])
	}
}
