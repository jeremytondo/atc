package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestObserveBinding(t *testing.T) {
	startedAt := time.Date(2026, time.August, 31, 6, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		events []startedThread
		want   bindingResult
	}{
		{
			name: "one matching event binds",
			events: []startedThread{
				{ID: "thread-1", CWD: "/work/atc", ObservedAt: startedAt.Add(time.Millisecond)},
			},
			want: bindingResult{
				Bound: true,
				Candidates: []bindingCandidate{
					{ThreadID: "thread-1", CWD: "/work/atc", Elapsed: time.Millisecond},
				},
			},
		},
		{
			name: "unrelated cwd is ignored",
			events: []startedThread{
				{ID: "thread-1", CWD: "/work/other", ObservedAt: startedAt.Add(time.Millisecond)},
			},
			want: bindingResult{},
		},
		{
			name: "multiple matching events fail closed",
			events: []startedThread{
				{ID: "thread-1", CWD: "/work/atc", ObservedAt: startedAt.Add(time.Millisecond)},
				{ID: "thread-2", CWD: "/work/atc", ObservedAt: startedAt.Add(2 * time.Millisecond)},
			},
			want: bindingResult{
				Candidates: []bindingCandidate{
					{ThreadID: "thread-1", CWD: "/work/atc", Elapsed: time.Millisecond},
					{ThreadID: "thread-2", CWD: "/work/atc", Elapsed: 2 * time.Millisecond},
				},
			},
		},
		{
			name: "event before process launch is ignored",
			events: []startedThread{
				{ID: "thread-1", CWD: "/work/atc", ObservedAt: startedAt.Add(-time.Millisecond)},
			},
			want: bindingResult{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := make(chan startedThread, len(test.events))
			for _, event := range test.events {
				events <- event
			}
			close(events)
			readErrors := make(chan error)

			got := observeBinding("/work/atc", startedAt, time.Millisecond, events, readErrors)
			if diff := cmp.Diff(test.want, got); diff != "" {
				t.Fatalf("observeBinding() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDecodeStartedThread(t *testing.T) {
	raw := json.RawMessage("{\"thread\":{\"id\":\"thread-1\",\"cwd\":\"/work/atc\"}}")
	want := startedThread{ID: "thread-1", CWD: "/work/atc"}
	got, ok := decodeStartedThread(raw)
	if !ok {
		t.Fatal("decodeStartedThread() reported invalid event")
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("decodeStartedThread() mismatch (-want +got):\n%s", diff)
	}
}
