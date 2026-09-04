package threads

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jeremytondo/atc/internal/api"
)

// A record an Integration pre-created for a creation its program refused
// is discarded through the Integration's own hold, which refuses a
// user's delete; the identity reported later is a fresh record with no
// memory of the submission.
func TestDiscardExternal(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa")
	ctx := context.Background()
	observation := ExternalObservation{IntegrationID: "t3code", ProviderID: "t1", InitialDirectory: f.dir("proj-aaaaa"), Title: "T"}
	id, err := f.service.ObserveExternal(ctx, observation)
	if err != nil {
		t.Fatal(err)
	}
	submitted, err := f.service.SubmitTurn(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	f.drain()
	if err := f.service.Delete(ctx, id); !errors.Is(err, ErrActive) {
		t.Errorf("Delete of the held record = %v; want ErrActive", err)
	}
	if err := f.service.DiscardExternal(ctx, "t3code", "t1"); err != nil {
		t.Fatalf("DiscardExternal = %v", err)
	}
	if _, err := f.service.Get(id); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after discard = %v; want ErrNotFound", err)
	}
	if got := f.drain(); !slices.Equal(got, []string{"thread.deleted " + id}) {
		t.Errorf("events = %v", got)
	}
	if err := f.service.DiscardExternal(ctx, "t3code", "t1"); err != nil {
		t.Errorf("DiscardExternal of an unknown identity = %v; want nothing to do", err)
	}
	if ids := f.service.UnarchivedProviderIDs("t3code"); len(ids) != 0 {
		t.Errorf("provider ids after discard = %v", ids)
	}

	observation.Turn = &TurnObservation{ProviderID: "pt-1", State: api.TurnRunning}
	observation.Status = api.ThreadWorking
	again, err := f.service.ObserveExternal(ctx, observation)
	if err != nil {
		t.Fatal(err)
	}
	if again == id {
		t.Errorf("re-observed identity reused the discarded id %s", id)
	}
	if turn := f.turn(t, again); turn.ID == submitted || turn.State != api.TurnRunning {
		t.Errorf("turn on the fresh record = %+v; want a fresh id, not the discarded submission %s", turn, submitted)
	}
	if got := f.drain(); !slices.Equal(got, []string{"thread.created " + again}) {
		t.Errorf("events = %v", got)
	}
}
