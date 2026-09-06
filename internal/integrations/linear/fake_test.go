package linear

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeLinear stands in for Linear's API: the GraphQL endpoint under the
// app's bearer token, and the OAuth token endpoint that rotates it. It
// records every activity and external-URL update by session and can be
// told to fail: with server errors, with an expired token, or with
// Linear's final refusal of a body.
type fakeLinear struct {
	t   *testing.T
	srv *httptest.Server

	mu           sync.Mutex
	token        string
	refreshToken string
	issued       int
	viewerID     string
	orgID        string
	activities   map[string][]recordedActivity
	activityIDs  map[string]bool
	links        map[string][]externalURL
	calls        int
	refreshes    int
	failNext     int    // server errors to answer before serving again
	expireToken  bool   // refuse the current token until refreshed
	refuseBodies string // a body containing this is refused for good
	// ambiguousNext records the next activities but answers as if the
	// request had failed — the network dropping the answer.
	ambiguousNext int
	// declineNext answers the next activities with success false.
	declineNext int
}

type recordedActivity struct {
	ID   string
	Type string
	Body string
}

func newFakeLinear(t *testing.T) *fakeLinear {
	t.Helper()
	f := &fakeLinear{
		t: t, token: "token-0", refreshToken: "refresh-0", viewerID: "app-user-1", orgID: "org-1",
		activities: map[string][]recordedActivity{}, activityIDs: map[string]bool{}, links: map[string][]externalURL{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /graphql", f.graphql)
	mux.HandleFunc("POST /oauth/token", f.oauth)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLinear) graphql(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	w.Header().Set("Content-Type", "application/json")
	if f.failNext > 0 {
		f.failNext--
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`upstream unavailable`))
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.token || f.expireToken {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"errors":[{"message":"Authentication required, not authenticated","extensions":{"code":"AUTHENTICATION_ERROR"}}]}`))
		return
	}
	var request struct {
		Query     string          `json:"query"`
		Variables json.RawMessage `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	switch {
	case strings.Contains(request.Query, "viewer"):
		_, _ = fmt.Fprintf(w, `{"data":{"viewer":{"id":%q,"name":"atc","organization":{"id":%q,"name":"Eleven Ideas"}}}}`, f.viewerID, f.orgID)
	case strings.Contains(request.Query, "agentActivityCreate"):
		var variables struct {
			Input activityInput `json:"input"`
		}
		if err := json.Unmarshal(request.Variables, &variables); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		input := variables.Input
		if f.refuseBodies != "" && strings.Contains(input.Content.Body, f.refuseBodies) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"errors":[{"message":"Argument Validation Error","extensions":{"code":"INVALID_INPUT"}}]}`))
			return
		}
		if f.activityIDs[input.ID] {
			_, _ = w.Write([]byte(`{"errors":[{"message":"An agent activity with this id already exists","extensions":{"code":"INVALID_INPUT"}}]}`))
			return
		}
		if f.declineNext > 0 {
			f.declineNext--
			_, _ = w.Write([]byte(`{"data":{"agentActivityCreate":{"success":false}}}`))
			return
		}
		f.activityIDs[input.ID] = true
		f.activities[input.AgentSessionID] = append(f.activities[input.AgentSessionID], recordedActivity{ID: input.ID, Type: input.Content.Type, Body: input.Content.Body})
		if f.ambiguousNext > 0 {
			f.ambiguousNext--
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`answer lost`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"agentActivityCreate":{"success":true}}}`))
	case strings.Contains(request.Query, "agentSessionUpdate"):
		var variables struct {
			ID    string `json:"id"`
			Input struct {
				ExternalURLs []externalURL `json:"externalUrls"`
			} `json:"input"`
		}
		if err := json.Unmarshal(request.Variables, &variables); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.links[variables.ID] = variables.Input.ExternalURLs
		_, _ = w.Write([]byte(`{"data":{"agentSessionUpdate":{"success":true}}}`))
	default:
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"message":"unknown operation"}]}`))
	}
}

func (f *fakeLinear) oauth(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshes++
	form := r.PostForm
	if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != f.refreshToken || form.Get("client_id") != "client-1" || form.Get("client_secret") != "secret-1" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		return
	}
	f.issued++
	f.token = fmt.Sprintf("token-%d", f.issued)
	f.refreshToken = fmt.Sprintf("refresh-%d", f.issued)
	f.expireToken = false
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": f.token, "refresh_token": f.refreshToken, "token_type": "Bearer", "expires_in": 86400, "scope": "read,write",
	})
}

// activitiesOf lists a session's activities in arrival order.
func (f *fakeLinear) activitiesOf(sessionID string) []recordedActivity {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedActivity(nil), f.activities[sessionID]...)
}

func (f *fakeLinear) linksOf(sessionID string) []externalURL {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]externalURL(nil), f.links[sessionID]...)
}

func (f *fakeLinear) set(change func(*fakeLinear)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f)
}

func (f *fakeLinear) counts() (calls, refreshes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.refreshes
}
