package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const (
	testToken         = "atc_test-token"
	testClientVersion = "v1.2.3-client"
	testServerVersion = "v1.2.3-server"
)

// speaking wraps a fake handler so every response carries the protocol
// header a real server sends; fakes that must answer otherwise override
// it. protocolOnly is the same without a version header.
func speaking(handler http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(ProtocolHeader, strconv.Itoa(Protocol))
		handler(w, r)
	})
}

// testServer answers /v1/health the way the real chassis does: version
// and protocol headers on every response, 401s included, problem+json on
// rejection.
func testServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(speaking(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(ServerVersionHeader, testServerVersion)
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(Problem{
				Title:  "Unauthorized",
				Status: http.StatusUnauthorized,
				Detail: "invalid or missing bearer token",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(Health{Status: "ok", Version: testServerVersion, Protocol: Protocol})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestHealth(t *testing.T) {
	srv := testServer(t)
	client := NewClient(srv.URL, testToken, testClientVersion, nil)
	health, err := client.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := Health{Status: "ok", Version: testServerVersion, Protocol: Protocol}
	if diff := cmp.Diff(want, health); diff != "" {
		t.Errorf("Health() mismatch (-want +got):\n%s", diff)
	}
}

func TestRequestCarriesTokenAndClientVersion(t *testing.T) {
	var authorization, clientVersion, protocol string
	srv := httptest.NewServer(speaking(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		clientVersion = r.Header.Get(ClientVersionHeader)
		protocol = r.Header.Get(ProtocolHeader)
		_ = json.NewEncoder(w).Encode(Health{Status: "ok"})
	}))
	defer srv.Close()

	if _, err := NewClient(srv.URL, testToken, testClientVersion, nil).Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	if authorization != "Bearer "+testToken {
		t.Errorf("Authorization = %q, want bearer token", authorization)
	}
	if clientVersion != testClientVersion {
		t.Errorf("%s = %q, want %q", ClientVersionHeader, clientVersion, testClientVersion)
	}
	if protocol != strconv.Itoa(Protocol) {
		t.Errorf("%s = %q, want %d", ProtocolHeader, protocol, Protocol)
	}
}

// A tokenless client (the probe) must not send an empty Authorization
// header, and must still learn the server version from the 401's typed
// error — the contract that lets `atc upgrade` verify a swap without
// credentials.
func TestTokenlessProbeReadsVersionOffUnauthorized(t *testing.T) {
	srv := testServer(t)
	_, err := NewClient(srv.URL, "", testClientVersion, nil).Health(context.Background())
	problem, ok := errors.AsType[*Problem](err)
	if !ok {
		t.Fatalf("err = %v, want *Problem", err)
	}
	if problem.Status != http.StatusUnauthorized {
		t.Errorf("Status = %d, want 401", problem.Status)
	}
	if problem.ServerVersion != testServerVersion {
		t.Errorf("ServerVersion = %q, want %q", problem.ServerVersion, testServerVersion)
	}
	if got, want := problem.Error(), "invalid or missing bearer token (HTTP 401)"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// A 2xx whose body cannot be decoded is still an HTTP response: it must
// surface as *Problem carrying the real status and server version, not as
// a plain error a caller would mistake for "nothing answered".
func TestMalformedSuccessBodyIsAProblem(t *testing.T) {
	srv := httptest.NewServer(speaking(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(ServerVersionHeader, testServerVersion)
		_, _ = w.Write([]byte("<html>not json</html>"))
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, testToken, testClientVersion, nil).Health(context.Background())
	problem, ok := errors.AsType[*Problem](err)
	if !ok {
		t.Fatalf("err = %v, want *Problem", err)
	}
	if problem.Status != http.StatusOK {
		t.Errorf("Status = %d, want 200", problem.Status)
	}
	if problem.ServerVersion != testServerVersion {
		t.Errorf("ServerVersion = %q, want %q", problem.ServerVersion, testServerVersion)
	}
}

// The problem body's status member is advisory; branching trusts what the
// transport actually said, immune to a lying or rewritten body.
func TestProblemStatusComesFromTransport(t *testing.T) {
	srv := httptest.NewServer(speaking(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(Problem{Title: "Server Error", Status: http.StatusInternalServerError, Detail: "lying body"})
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, "", testClientVersion, nil).Health(context.Background())
	problem, ok := errors.AsType[*Problem](err)
	if !ok {
		t.Fatalf("err = %v, want *Problem", err)
	}
	if problem.Status != http.StatusUnauthorized {
		t.Errorf("Status = %d, want the transport's 401", problem.Status)
	}
	if problem.Detail != "lying body" {
		t.Errorf("Detail = %q, want the body's fields retained", problem.Detail)
	}
}

// A non-problem error body (a proxy page, some other process on the port)
// degrades to the status line; the caller still gets a typed error.
func TestNonProblemBodyDegradesToStatus(t *testing.T) {
	// No ATC headers at all: a proxy answering for a backend that is down
	// is reported by its own status, not as a protocol mismatch.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>nope</html>"))
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, testToken, testClientVersion, nil).Health(context.Background())
	problem, ok := errors.AsType[*Problem](err)
	if !ok {
		t.Fatalf("err = %v, want *Problem", err)
	}
	want := &Problem{Title: "Bad Gateway", Status: http.StatusBadGateway}
	if diff := cmp.Diff(want, problem); diff != "" {
		t.Errorf("problem mismatch (-want +got):\n%s", diff)
	}
}

// Only an HTTP response becomes a *Problem; transport failure stays a
// plain error so "responding" and "rejected" remain distinguishable.
func TestTransportErrorIsNotAProblem(t *testing.T) {
	srv := httptest.NewServer(speaking(func(http.ResponseWriter, *http.Request) {}))
	srv.Close() // nothing listening anymore

	_, err := NewClient(srv.URL, testToken, testClientVersion, nil).Health(context.Background())
	if err == nil {
		t.Fatal("want an error from a dead server")
	}
	if problem, ok := errors.AsType[*Problem](err); ok {
		t.Errorf("transport failure decoded as *Problem: %v", problem)
	}
}

// The terminal methods are thin typed wrappers over one request path;
// method, path, and body round-trip is what there is to verify.
func TestTerminalMethods(t *testing.T) {
	type call struct {
		Method, Path, Query, Body string
	}
	var got call
	srv := httptest.NewServer(speaking(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = call{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: strings.TrimSpace(string(body))}
		switch r.URL.Path {
		case "/v1/terminals":
			if r.Method == http.MethodPost {
				_ = json.NewEncoder(w).Encode(Terminal{ID: "term-x7k2f", Status: TerminalRunning})
				return
			}
			_ = json.NewEncoder(w).Encode(TerminalList{Terminals: []Terminal{{ID: "term-x7k2f"}}})
		default:
			if r.Method == http.MethodDelete {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			_ = json.NewEncoder(w).Encode(Terminal{ID: "term-x7k2f"})
		}
	}))
	defer srv.Close()
	client := NewClient(srv.URL, testToken, testClientVersion, nil)
	ctx := context.Background()

	terminal, err := client.CreateTerminal(ctx, TerminalCreateParams{SpaceID: "spce-x7k2f", Command: "hx"})
	if err != nil || terminal.ID != "term-x7k2f" {
		t.Fatalf("CreateTerminal = %+v, %v", terminal, err)
	}
	want := call{http.MethodPost, "/v1/terminals", "", `{"spaceId":"spce-x7k2f","command":"hx"}`}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("create request (-want +got):\n%s", diff)
	}

	if _, err := client.Terminals(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if got.Method != http.MethodGet || got.Path != "/v1/terminals" {
		t.Errorf("list request = %+v", got)
	}

	if _, err := client.Terminals(ctx, "spce-x7k2f"); err != nil {
		t.Fatal(err)
	}
	if got.Query != "space=spce-x7k2f" {
		t.Errorf("filtered list query = %q", got.Query)
	}

	if _, err := client.Terminal(ctx, "term-x7k2f"); err != nil {
		t.Fatal(err)
	}
	if got.Path != "/v1/terminals/term-x7k2f" {
		t.Errorf("get path = %q", got.Path)
	}

	if _, err := client.UpdateTerminal(ctx, "term-x7k2f", TerminalUpdateParams{Name: Some("build")}); err != nil {
		t.Fatal(err)
	}
	want = call{http.MethodPatch, "/v1/terminals/term-x7k2f", "", `{"name":"build"}`}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("update request (-want +got):\n%s", diff)
	}

	if err := client.DeleteTerminal(ctx, "term-x7k2f"); err != nil {
		t.Fatal(err)
	}
	if got.Method != http.MethodDelete {
		t.Errorf("delete method = %q", got.Method)
	}
}

func TestProjectMethods(t *testing.T) {
	type call struct {
		Method, Path, Body string
	}
	var got call
	srv := httptest.NewServer(speaking(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = call{Method: r.Method, Path: r.URL.Path, Body: strings.TrimSpace(string(body))}
		switch {
		case r.URL.Path == "/v1/projects" && r.Method == http.MethodPost:
			_ = json.NewEncoder(w).Encode(Project{ID: "proj-x7k2f"})
		case r.URL.Path == "/v1/projects":
			_ = json.NewEncoder(w).Encode(ProjectList{Projects: []Project{{ID: "proj-x7k2f"}}})
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			_ = json.NewEncoder(w).Encode(Project{ID: "proj-x7k2f"})
		}
	}))
	defer srv.Close()
	client := NewClient(srv.URL, testToken, testClientVersion, nil)
	ctx := context.Background()

	project, err := client.CreateProject(ctx, ProjectCreateParams{Directory: "/proj", Name: "p"})
	if err != nil || project.ID != "proj-x7k2f" {
		t.Fatalf("CreateProject = %+v, %v", project, err)
	}
	want := call{http.MethodPost, "/v1/projects", `{"directory":"/proj","name":"p"}`}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("create request (-want +got):\n%s", diff)
	}

	if _, err := client.Projects(ctx); err != nil {
		t.Fatal(err)
	}
	if got.Method != http.MethodGet || got.Path != "/v1/projects" {
		t.Errorf("list request = %+v", got)
	}

	if _, err := client.Project(ctx, "proj-x7k2f"); err != nil {
		t.Fatal(err)
	}
	if got.Path != "/v1/projects/proj-x7k2f" {
		t.Errorf("get path = %q", got.Path)
	}

	if _, err := client.UpdateProject(ctx, "proj-x7k2f", ProjectUpdateParams{Name: Some("renamed")}); err != nil {
		t.Fatal(err)
	}
	want = call{http.MethodPatch, "/v1/projects/proj-x7k2f", `{"name":"renamed"}`}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("update request (-want +got):\n%s", diff)
	}

	if err := client.DeleteProject(ctx, "proj-x7k2f"); err != nil {
		t.Fatal(err)
	}
	if got.Method != http.MethodDelete {
		t.Errorf("delete method = %q", got.Method)
	}
}

// Raw is the `atc api` gateway: same auth and version headers, response
// returned as-is for streaming, status handling left to the caller.
func TestRawCarriesHeadersAndReturnsResponse(t *testing.T) {
	srv := testServer(t)
	client := NewClient(srv.URL, testToken, testClientVersion, nil)
	resp, err := client.Raw(context.Background(), http.MethodGet, "v1/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (leading slash added)", resp.StatusCode)
	}
	if got := resp.Header.Get(ServerVersionHeader); got != testServerVersion {
		t.Errorf("%s = %q, want %q", ServerVersionHeader, got, testServerVersion)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"ok"`) {
		t.Errorf("body = %q, want the health document", body)
	}
}

func TestProblemErrorFallbacks(t *testing.T) {
	for name, tc := range map[string]struct {
		problem Problem
		want    string
	}{
		"detail":      {Problem{Title: "Unauthorized", Status: 401, Detail: "bad token"}, "bad token (HTTP 401)"},
		"title":       {Problem{Title: "Unauthorized", Status: 401}, "Unauthorized (HTTP 401)"},
		"status only": {Problem{Status: 502}, "Bad Gateway (HTTP 502)"},
	} {
		if got := tc.problem.Error(); got != tc.want {
			t.Errorf("%s: Error() = %q, want %q", name, got, tc.want)
		}
	}
}

// The integration methods are thin typed wrappers; the id path segment
// is escaped so a user-typed id with reserved characters stays one
// unknown segment instead of changing the route.
func TestIntegrationMethods(t *testing.T) {
	var got struct{ Method, Path string }
	srv := httptest.NewServer(speaking(func(w http.ResponseWriter, r *http.Request) {
		got.Method, got.Path = r.Method, r.URL.EscapedPath()
		if r.URL.Path == "/v1/integrations" {
			_ = json.NewEncoder(w).Encode(IntegrationList{Integrations: []Integration{{ID: "claude"}, {ID: "t3code"}}})
			return
		}
		_ = json.NewEncoder(w).Encode(Integration{ID: "t3code"})
	}))
	defer srv.Close()
	client := NewClient(srv.URL, testToken, testClientVersion, nil)
	ctx := context.Background()

	integrations, err := client.Integrations(ctx)
	if err != nil || len(integrations) != 2 {
		t.Fatalf("Integrations = %+v, %v", integrations, err)
	}
	if got.Method != http.MethodGet || got.Path != "/v1/integrations" {
		t.Errorf("list request = %+v", got)
	}
	if integration, err := client.Integration(ctx, "t3code"); err != nil || integration.ID != "t3code" {
		t.Fatalf("Integration = %+v, %v", integration, err)
	}
	if got.Path != "/v1/integrations/t3code" {
		t.Errorf("get path = %q", got.Path)
	}

	if _, err := client.Integration(ctx, "x/y?z"); err != nil {
		t.Fatal(err)
	}
	if got.Path != "/v1/integrations/x%2Fy%3Fz" {
		t.Errorf("escaped get path = %q", got.Path)
	}
}

// The space methods are thin typed wrappers over the flat /v1/spaces
// surface.
func TestSpaceMethods(t *testing.T) {
	type call struct {
		Method, Path, Query, Body string
	}
	var got call
	srv := httptest.NewServer(speaking(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = call{r.Method, r.URL.Path, r.URL.RawQuery, strings.TrimSpace(string(body))}
		if r.URL.Path == "/v1/spaces" && r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(SpaceList{Spaces: []Space{{ID: "spce-x7k2f", IsDefault: true}}})
			return
		}
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_ = json.NewEncoder(w).Encode(Space{ID: "spce-x7k2f"})
	}))
	defer srv.Close()
	client := NewClient(srv.URL, testToken, testClientVersion, nil)
	ctx := context.Background()

	if _, err := client.CreateSpace(ctx, SpaceCreateParams{Directory: "/work", Name: "work"}); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(call{http.MethodPost, "/v1/spaces", "", `{"directory":"/work","name":"work"}`}, got); diff != "" {
		t.Errorf("create request (-want +got):\n%s", diff)
	}
	spaces, err := client.Spaces(ctx)
	if err != nil || len(spaces) != 1 || !spaces[0].IsDefault {
		t.Fatalf("Spaces = %+v, %v", spaces, err)
	}
	if _, err := client.Space(ctx, "spce-x7k2f"); err != nil || got.Path != "/v1/spaces/spce-x7k2f" {
		t.Errorf("get = %+v, %v", got, err)
	}
	if _, err := client.UpdateSpace(ctx, "spce-x7k2f", SpaceUpdateParams{Name: Some("renamed")}); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(call{http.MethodPatch, "/v1/spaces/spce-x7k2f", "", `{"name":"renamed"}`}, got); diff != "" {
		t.Errorf("update request (-want +got):\n%s", diff)
	}
	if err := client.DeleteSpace(ctx, "spce-x7k2f"); err != nil || got.Method != http.MethodDelete {
		t.Errorf("delete = %+v, %v", got, err)
	}
}

// The client's half of the protocol contract: an ATC server on another
// protocol, or one that predates the contract (version header, no
// protocol), is refused on every status with a typed problem that still
// says what answered. Raw requests are refused the same way.
func TestClientRefusesOtherProtocols(t *testing.T) {
	for name, tc := range map[string]struct {
		protocol     string
		status       int
		wantProtocol int
	}{
		"newer server, success":        {protocol: strconv.Itoa(Protocol + 1), status: http.StatusOK, wantProtocol: Protocol + 1},
		"newer server, rejected":       {protocol: strconv.Itoa(Protocol + 1), status: http.StatusUnauthorized, wantProtocol: Protocol + 1},
		"server predates the contract": {protocol: "", status: http.StatusOK},
		"unreadable protocol":          {protocol: "one", status: http.StatusOK},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set(ServerVersionHeader, testServerVersion)
				if tc.protocol != "" {
					w.Header().Set(ProtocolHeader, tc.protocol)
				}
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(Health{Status: "ok", Version: testServerVersion})
			}))
			defer srv.Close()
			client := NewClient(srv.URL, testToken, testClientVersion, nil)
			_, err := client.Health(context.Background())
			problem, ok := errors.AsType[*Problem](err)
			if !ok {
				t.Fatalf("err = %v, want *Problem", err)
			}
			want := &Problem{
				Title:          "protocol mismatch",
				Status:         http.StatusUpgradeRequired,
				Code:           CodeProtocolMismatch,
				Detail:         problem.Detail,
				ServerVersion:  testServerVersion,
				ServerProtocol: tc.wantProtocol,
			}
			if diff := cmp.Diff(want, problem); diff != "" {
				t.Errorf("problem mismatch (-want +got):\n%s", diff)
			}
			if !strings.Contains(problem.Detail, "protocol "+strconv.Itoa(Protocol)) {
				t.Errorf("Detail = %q, want this client's protocol named", problem.Detail)
			}
			_, err = client.Raw(context.Background(), http.MethodGet, "/v1/health", nil)
			rawProblem, ok := errors.AsType[*Problem](err)
			if !ok {
				t.Errorf("Raw err = %v, want *Problem", err)
			} else if diff := cmp.Diff(want, rawProblem); diff != "" {
				t.Errorf("Raw problem mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
