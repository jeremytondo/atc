package integrations

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakePrepareThread is the creation seam of the fixture's connection-backed
// Integration; routing is what these tests check, not what it does.
func fakePrepareThread(context.Context, ThreadCreation) (PreparedThread, error) {
	return PreparedThread{ProviderID: "t-1", Title: "T"}, nil
}

// Creation routes by Integration id to the seam, refusing an unknown
// Integration, one without the seam, and an agent it does not list — each
// before the program's state is consulted.
func TestResolveThreadCreation(t *testing.T) {
	service := newTestService(t)
	prepare, err := service.ResolveThreadCreation("watcher", "gamma")
	if err != nil || prepare == nil {
		t.Fatalf("ResolveThreadCreation(watcher, gamma) = %v (nil seam: %t)", err, prepare == nil)
	}
	cases := []struct {
		integration, agent string
		want               error
		message            string
	}{
		{"nope", "gamma", ErrNotFound, `"nope"`},
		{"alpha", "alpha", ErrThreadCreationUnsupported, "Alpha"},
		{"watcher", "delta", ErrAgentNotFound, `Watcher lists no agent "delta"`},
		{"watcher", "", ErrAgentNotFound, `no agent ""`},
	}
	for _, tc := range cases {
		_, err := service.ResolveThreadCreation(tc.integration, tc.agent)
		if !errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.message) {
			t.Errorf("ResolveThreadCreation(%s, %s) = %v; want %v containing %q", tc.integration, tc.agent, err, tc.want, tc.message)
		}
	}
}
