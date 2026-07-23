package api

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/jeremytondo/atc/internal/diagnostics"
)

// The canonical cross-client API contract lives in
// packages/contracts/fixtures: one JSON file per request/response shape,
// listing every route that uses it. These tests bind the fixtures to the Go
// wire structs (the producer side); ATCKit and the web client decode
// the same files, so a shape change fails on every surface that hasn't
// caught up.

const fixturesDir = "../../../packages/contracts/fixtures"

// contractFixture is the on-disk fixture shape.
type contractFixture struct {
	Routes   []string        `json:"routes"`
	Request  json.RawMessage `json:"request"`
	Response json.RawMessage `json:"response"`
}

// responseTypes maps fixture basename to the Go type its response encodes.
var responseTypes = map[string]func() any{
	"health":                func() any { return &diagnostics.Health{} },
	"version":               func() any { return &diagnostics.Version{} },
	"error":                 func() any { return &errorResponse{} },
	"error-session-ended":   func() any { return &errorResponse{} },
	"error-zmx-unavailable": func() any { return &errorResponse{} },
	"fs-list":               func() any { return &fsListResponse{} },
	"projects-list":         func() any { return &ProjectListResponse{} },
	"project-detail":        func() any { return &Project{} },
	"project-create":        func() any { return &Project{} },
	"project-rename":        func() any { return &Project{} },
	"project-delete":        func() any { return &struct{}{} },
	"workspaces-list":       func() any { return &WorkspaceListResponse{} },
	"workspace-detail":      func() any { return &Workspace{} },
	"workspace-create":      func() any { return &Workspace{} },
	"workspace-rename":      func() any { return &Workspace{} },
	"workspace-delete":      func() any { return &struct{}{} },
	"workspace-sessions":    func() any { return &SessionListResponse{} },
	"sessions-list":         func() any { return &SessionListResponse{} },
	"session-start":         func() any { return &SessionResponse{} },
	"session-detail":        func() any { return &SessionResponse{} },
	"session-rename":        func() any { return &SessionResponse{} },
	"session-delete":        func() any { return &struct{}{} },
	"session-send-text":     func() any { return &struct{}{} },
	"session-send-key":      func() any { return &struct{}{} },
	"actions-list":          func() any { return &actionsResponse{} },
	"action-detail":         func() any { return &Action{} },
	"action-create":         func() any { return &Action{} },
	"action-update":         func() any { return &Action{} },
	"action-delete":         func() any { return &struct{}{} },
}

// requestTypes maps fixture basename to the Go type that decodes its request.
var requestTypes = map[string]func() any{
	"project-create":    func() any { return &createProjectRequest{} },
	"project-rename":    func() any { return &renameProjectRequest{} },
	"workspace-create":  func() any { return &createWorkspaceRequest{} },
	"workspace-rename":  func() any { return &renameWorkspaceRequest{} },
	"session-start":     func() any { return &startRequest{} },
	"session-rename":    func() any { return &renameSessionRequest{} },
	"session-send-text": func() any { return &sendTextRequest{} },
	"session-send-key":  func() any { return &sendKeyRequest{} },
	"action-create":     func() any { return &actionCreateRequest{} },
	"action-update":     func() any { return &actionPatchRequest{} },
}

func loadFixtures(t *testing.T) map[string]contractFixture {
	t.Helper()
	entries, err := os.ReadDir(fixturesDir)
	if err != nil {
		t.Fatalf("read fixtures dir: %v", err)
	}
	fixtures := make(map[string]contractFixture, len(entries))
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(fixturesDir, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		var fixture contractFixture
		if err := json.Unmarshal(data, &fixture); err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		fixtures[strings.TrimSuffix(entry.Name(), ".json")] = fixture
	}
	return fixtures
}

// TestContractResponsesRoundTrip pins each response fixture to its Go wire
// struct: decoding then re-encoding must reproduce the fixture exactly.
// A field added to the fixture but not the struct disappears on re-encode;
// a renamed/removed struct field diverges the other way. Both fail here.
func TestContractResponsesRoundTrip(t *testing.T) {
	fixtures := loadFixtures(t)
	for name, fixture := range fixtures {
		t.Run(name, func(t *testing.T) {
			maker, ok := responseTypes[name]
			if !ok {
				t.Fatalf("fixture %s.json has no entry in responseTypes", name)
			}
			dst := maker()
			if err := json.Unmarshal(fixture.Response, dst); err != nil {
				t.Fatalf("decode response into %T: %v", dst, err)
			}
			encoded, err := json.Marshal(dst)
			if err != nil {
				t.Fatalf("re-encode %T: %v", dst, err)
			}
			var want, got any
			mustUnmarshal(t, fixture.Response, &want)
			mustUnmarshal(t, encoded, &got)
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("response round-trip drifted\nfixture: %s\nGo %T:  %s", fixture.Response, dst, encoded)
			}
		})
	}
}

// TestContractRequestsDecode pins each request fixture to the Go struct that
// decodes it: every fixture field must survive a decode/encode round-trip
// (extra zero-valued keys from non-omitempty Go fields are fine).
func TestContractRequestsDecode(t *testing.T) {
	fixtures := loadFixtures(t)
	for name, fixture := range fixtures {
		if fixture.Request == nil {
			continue
		}
		t.Run(name, func(t *testing.T) {
			maker, ok := requestTypes[name]
			if !ok {
				t.Fatalf("fixture %s.json has a request but no entry in requestTypes", name)
			}
			dst := maker()
			if err := json.Unmarshal(fixture.Request, dst); err != nil {
				t.Fatalf("decode request into %T: %v", dst, err)
			}
			encoded, err := json.Marshal(dst)
			if err != nil {
				t.Fatalf("re-encode %T: %v", dst, err)
			}
			var want, got map[string]any
			mustUnmarshal(t, fixture.Request, &want)
			mustUnmarshal(t, encoded, &got)
			if err := isSubset(want, got); err != nil {
				t.Fatalf("request field lost in %T: %v\nfixture: %s\nGo: %s", dst, err, fixture.Request, encoded)
			}
		})
	}
}

// TestContractFixturesCoverEveryRoute keeps the fixture set and the route
// table in lockstep: every registered route (except the WebSocket attach,
// which has no JSON bodies) needs a fixture, and fixtures cannot name routes
// that no longer exist.
func TestContractFixturesCoverEveryRoute(t *testing.T) {
	exempt := map[string]bool{
		"GET /sessions/{id}/attach": true, // WebSocket; no JSON contract
	}
	registered := map[string]bool{}
	for pattern := range (apiRoutes{}).endpoints() {
		if !exempt[pattern] {
			registered[pattern] = true
		}
	}

	covered := map[string]string{}
	for name, fixture := range loadFixtures(t) {
		for _, route := range fixture.Routes {
			if !registered[route] {
				t.Errorf("fixture %s.json lists unknown route %q", name, route)
			}
			covered[route] = name
		}
	}
	var missing []string
	for pattern := range registered {
		if _, ok := covered[pattern]; !ok {
			missing = append(missing, pattern)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("routes without a contract fixture: %v", missing)
	}
}

func mustUnmarshal(t *testing.T, data []byte, dst any) {
	t.Helper()
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
}

// isSubset reports whether every key in want exists in got with a deeply
// equal value (recursing into nested objects).
func isSubset(want, got map[string]any) error {
	for key, wantValue := range want {
		gotValue, ok := got[key]
		if !ok {
			return fmt.Errorf("missing key %q", key)
		}
		wantMap, wantIsMap := wantValue.(map[string]any)
		gotMap, gotIsMap := gotValue.(map[string]any)
		if wantIsMap && gotIsMap {
			if err := isSubset(wantMap, gotMap); err != nil {
				return fmt.Errorf("%s.%w", key, err)
			}
			continue
		}
		if !reflect.DeepEqual(wantValue, gotValue) {
			return fmt.Errorf("key %q = %v, want %v", key, gotValue, wantValue)
		}
	}
	return nil
}
