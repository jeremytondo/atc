package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
)

func TestFetchDetectsNewThreadAndProjectsStatuses(t *testing.T) {
	fixtures := loadSnapshots(t)
	var request atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if diff := cmp.Diff("/api/orchestration/shell", r.URL.Path); diff != "" {
			t.Errorf("path mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff("Bearer read-token", r.Header.Get("Authorization")); diff != "" {
			t.Errorf("authorization mismatch (-want +got):\n%s", diff)
		}
		index := int(request.Add(1)) - 1
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixtures[index])
	}))
	defer server.Close()

	client, err := newClient(server.URL, "read-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	var previous []projectedThread
	want := []struct {
		kind   string
		status api.ThreadStatus
	}{
		{"", ""},
		{"created", api.ThreadWorking},
		{"status_changed", api.ThreadWaitingForPermission},
		{"status_changed", api.ThreadWaitingForInput},
		{"status_changed", api.ThreadError},
		{"status_changed", api.ThreadIdle},
	}
	for i := range fixtures {
		threads, err := client.fetch(context.Background(), "/work/atc")
		if err != nil {
			t.Fatalf("fetch fixture %d: %v", i, err)
		}
		changes := diff(previous, threads)
		if want[i].kind == "" {
			if len(changes) != 0 {
				t.Fatalf("fixture %d changes = %#v, want none", i, changes)
			}
		} else {
			got := []any{changes[0].Kind, changes[0].Thread.Status}
			expected := []any{want[i].kind, want[i].status}
			if diff := cmp.Diff(expected, got); diff != "" {
				t.Fatalf("fixture %d change mismatch (-want +got):\n%s", i, diff)
			}
		}
		if i == 4 {
			if diff := cmp.Diff("provider failed", threads[0].LastError); diff != "" {
				t.Fatalf("fixture %d last error mismatch (-want +got):\n%s", i, diff)
			}
		}
		previous = threads
	}
	if previous[0].LastError != "" {
		t.Fatalf("ready projection retained stale error %q", previous[0].LastError)
	}
}

func TestProjectStatusPrecedenceAndUnknowns(t *testing.T) {
	truth := true
	falsehood := false
	backgroundWorking := "working"
	backgroundNew := "new-state"
	lastError := "boom"
	tests := []struct {
		name   string
		thread threadShell
		want   api.ThreadStatus
	}{
		{
			name: "approval outranks running and input",
			thread: threadShell{HasPendingApprovals: &truth, HasPendingUserInput: &truth,
				Session: &sessionShell{Status: "running"}},
			want: api.ThreadWaitingForPermission,
		},
		{
			name: "input outranks session error",
			thread: threadShell{HasPendingApprovals: &falsehood, HasPendingUserInput: &truth,
				Session: &sessionShell{Status: "error", LastError: &lastError}},
			want: api.ThreadWaitingForInput,
		},
		{
			name: "background survives ready turn",
			thread: threadShell{HasPendingApprovals: &falsehood, HasPendingUserInput: &falsehood,
				Session: &sessionShell{Status: "ready"}, BackgroundLiveness: &backgroundWorking},
			want: api.ThreadWorking,
		},
		{
			name: "unknown session fails closed",
			thread: threadShell{HasPendingApprovals: &falsehood, HasPendingUserInput: &falsehood,
				Session: &sessionShell{Status: "future-status"}},
			want: api.ThreadUnknown,
		},
		{
			name: "unknown background fails closed",
			thread: threadShell{HasPendingApprovals: &falsehood, HasPendingUserInput: &falsehood,
				Session: &sessionShell{Status: "ready"}, BackgroundLiveness: &backgroundNew},
			want: api.ThreadUnknown,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, _, _ := projectStatus(test.thread)
			if diff := cmp.Diff(test.want, got); diff != "" {
				t.Errorf("status mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestFetchReportsExpiredCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"expired"}`))
	}))
	defer server.Close()
	client, err := newClient(server.URL, "expired", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.fetch(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("fetch error = %v, want actionable credential error", err)
	}
}

func TestFetchReportsUnavailableServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	origin := server.URL
	server.Close()
	client, err := newClient(origin, "read-token", &http.Client{Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.fetch(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("fetch error = %v, want unavailable error", err)
	}
}

func TestFetchRejectsIncompleteSchema(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"snapshotSequence": 1,
			"projects": [{"id":"project-1","title":"atc","workspaceRoot":"/work/atc"}],
			"threads": [{"id":"thread-1","projectId":"project-1","title":"thread","updatedAt":"2026-09-01T20:00:00Z"}],
			"updatedAt": "2026-09-01T20:00:00Z"
		}`))
	}))
	defer server.Close()
	client, err := newClient(server.URL, "read-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.fetch(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "pending-action flags") {
		t.Fatalf("fetch error = %v, want schema error", err)
	}
}

func loadSnapshots(t *testing.T) []json.RawMessage {
	t.Helper()
	data, err := os.ReadFile("testdata/snapshots.json")
	if err != nil {
		t.Fatal(err)
	}
	var snapshots []json.RawMessage
	if err := json.Unmarshal(data, &snapshots); err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 6 {
		t.Fatalf("fixture count = %d, want 6", len(snapshots))
	}
	return snapshots
}

func TestNewClientRejectsInvalidOrigin(t *testing.T) {
	_, err := newClient("file:///tmp/t3", "token", nil)
	if err == nil {
		t.Fatal("expected invalid origin error")
	}
}
