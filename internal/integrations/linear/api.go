package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Linear's API surface as this Integration uses it: one GraphQL endpoint
// under the app's bearer token, and the OAuth token endpoint that renews
// that token. Every request is bounded by the client's timeout; nothing
// here retries — the outbox owns retry policy, and it needs to know
// which failures are worth one.
const (
	defaultAPIURL = "https://api.linear.app"
	graphqlPath   = "/graphql"
	tokenPath     = "/oauth/token"
	// maxAPIBodyBytes bounds a response read; Linear's answers to these
	// calls are small.
	maxAPIBodyBytes = 1 << 20

	activityMutation = `mutation($input: AgentActivityCreateInput!) { agentActivityCreate(input: $input) { success } }`
	linksMutation    = `mutation($id: String!, $input: AgentSessionUpdateInput!) { agentSessionUpdate(id: $id, input: $input) { success } }`
	viewerQuery      = `query { viewer { id name organization { id name } } }`
)

// apiError is a call Linear answered with a failure, classified for the
// outbox: transient failures earn a retry, an auth failure a token
// renewal, and a permanent one the record of why.
type apiError struct {
	kind    failureKind
	status  int
	message string
}

type failureKind int

const (
	failureTransient failureKind = iota
	failureAuth
	failurePermanent
	// failureDuplicate is Linear refusing an activity id it already has:
	// an earlier, ambiguous attempt landed, so the send is done.
	failureDuplicate
)

func (e *apiError) Error() string {
	if e.status != 0 {
		return fmt.Sprintf("Linear answered %d: %s", e.status, e.message)
	}
	return e.message
}

// classify sorts an error from graphql or refresh into what the outbox
// does about it. Network failures and timeouts are transient.
func classify(err error) failureKind {
	var api *apiError
	if errors.As(err, &api) {
		return api.kind
	}
	return failureTransient
}

// graphql posts one operation and decodes data into out. An HTTP failure
// or a GraphQL errors array is an *apiError.
func graphql(ctx context.Context, client *http.Client, apiURL, token, query string, variables any, out any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL+graphqlPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIBodyBytes))
	if err != nil {
		return err
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []graphqlError  `json:"errors"`
	}
	decodeErr := json.Unmarshal(data, &envelope)
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return &apiError{kind: failureAuth, status: resp.StatusCode, message: summarize(data, envelope.Errors != nil)}
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return &apiError{kind: failureTransient, status: resp.StatusCode, message: summarize(data, false)}
	case resp.StatusCode >= 400:
		if decodeErr == nil && len(envelope.Errors) > 0 {
			return graphqlFailure(resp.StatusCode, envelope.Errors)
		}
		return &apiError{kind: failurePermanent, status: resp.StatusCode, message: summarize(data, false)}
	case decodeErr != nil:
		return &apiError{kind: failureTransient, status: resp.StatusCode, message: "unreadable response body"}
	case len(envelope.Errors) > 0:
		return graphqlFailure(resp.StatusCode, envelope.Errors)
	}
	if out != nil && len(envelope.Data) > 0 {
		return json.Unmarshal(envelope.Data, out)
	}
	return nil
}

// graphqlError is one entry of a GraphQL errors array, with the code
// Linear attaches.
type graphqlError struct {
	Message    string `json:"message"`
	Extensions struct {
		Code string `json:"code"`
	} `json:"extensions"`
}

// graphqlFailure classifies a GraphQL-level failure from every error
// Linear returned, the most forgiving verdict winning: an authentication
// code renews the token, a rate limit or Linear's own internal error
// earns a retry, an activity id Linear already holds is a completed send,
// and a validation or lookup failure is Linear's final word. A code this
// does not know is treated as transient — retrying a refusal is cheaper
// than discarding a call Linear would have taken.
func graphqlFailure(status int, errs []graphqlError) error {
	verdict := failurePermanent
	known := false
	for _, e := range errs {
		lower := strings.ToLower(e.Message + " " + e.Extensions.Code)
		var kind failureKind
		recognized := true
		switch {
		case strings.Contains(lower, "authentication") || strings.Contains(lower, "unauthenticated") || strings.Contains(lower, "invalid token") || strings.Contains(lower, "expired"):
			kind = failureAuth
		case strings.Contains(lower, "already exists") || strings.Contains(lower, "duplicate"):
			kind = failureDuplicate
		case strings.Contains(lower, "ratelimit") || strings.Contains(lower, "rate limit") || strings.Contains(lower, "internal_error") || strings.Contains(lower, "internal server error"):
			kind = failureTransient
		case strings.Contains(lower, "invalid_input") || strings.Contains(lower, "validation") || strings.Contains(lower, "bad_user_input") || strings.Contains(lower, "not_found") || strings.Contains(lower, "not found") || strings.Contains(lower, "forbidden") || strings.Contains(lower, "user_error"):
			kind = failurePermanent
		default:
			recognized = false
			kind = failureTransient
		}
		known = known || recognized
		if kind == failureAuth || kind == failureDuplicate || kind == failureTransient && verdict == failurePermanent {
			verdict = kind
		}
	}
	if !known && len(errs) > 0 {
		verdict = failureTransient
	}
	return &apiError{kind: verdict, status: status, message: errs[0].Message}
}

// summarize keeps a short, secret-free failure text for logs and status.
func summarize(data []byte, structured bool) string {
	text := strings.Join(strings.Fields(string(data)), " ")
	if structured {
		var envelope struct {
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		if json.Unmarshal(data, &envelope) == nil && len(envelope.Errors) > 0 {
			text = envelope.Errors[0].Message
		}
	}
	if len(text) > 200 {
		text = text[:199] + "…"
	}
	return text
}

// tokens is a token endpoint answer.
type tokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

// refresh renews the app's access token with its refresh token.
func refresh(ctx context.Context, client *http.Client, apiURL string, setup Setup, now time.Time) (tokens, error) {
	if setup.RefreshToken == "" {
		return tokens{}, &apiError{kind: failureAuth, message: "the setup file has no refresh_token; re-authorize the app and update the file"}
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {setup.RefreshToken},
		"client_id":     {setup.ClientID},
		"client_secret": {setup.ClientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL+tokenPath, strings.NewReader(form.Encode()))
	if err != nil {
		return tokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return tokens{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIBodyBytes))
	if err != nil {
		return tokens{}, err
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return tokens{}, &apiError{kind: failureTransient, status: resp.StatusCode, message: summarize(data, false)}
	case resp.StatusCode >= 400:
		// A refused refresh is the credential's end: only the operator
		// can mint another.
		return tokens{}, &apiError{kind: failureAuth, status: resp.StatusCode, message: "token refresh refused: " + summarize(data, false)}
	}
	var answer struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(data, &answer); err != nil || answer.AccessToken == "" {
		return tokens{}, &apiError{kind: failureTransient, status: resp.StatusCode, message: "token refresh answered without an access token"}
	}
	result := tokens{AccessToken: answer.AccessToken, RefreshToken: answer.RefreshToken}
	if answer.ExpiresIn > 0 {
		result.ExpiresAt = now.Add(time.Duration(answer.ExpiresIn) * time.Second)
	}
	return result, nil
}

// viewer is what the probe learns about the token: the app user it acts
// as and the workspace it is installed in.
type viewer struct {
	ID           string
	Name         string
	Organization struct {
		ID   string
		Name string
	}
}

func fetchViewer(ctx context.Context, client *http.Client, apiURL, token string) (viewer, error) {
	var data struct {
		Viewer viewer `json:"viewer"`
	}
	if err := graphql(ctx, client, apiURL, token, viewerQuery, nil, &data); err != nil {
		return viewer{}, err
	}
	if data.Viewer.ID == "" {
		return viewer{}, &apiError{kind: failurePermanent, message: "viewer query answered without an id"}
	}
	return data.Viewer, nil
}

// Outbox bodies: the GraphQL variables for each owed call, JSON in the
// row. The activity carries the id Linear deduplicates on.

// mutationResult is what both mutations answer: whether Linear applied
// the change. A 200 with success false is Linear declining without an
// error, and counts as a refusal.
type mutationResult struct {
	Activity *struct {
		Success bool `json:"success"`
	} `json:"agentActivityCreate"`
	Session *struct {
		Success bool `json:"success"`
	} `json:"agentSessionUpdate"`
}

func (r mutationResult) succeeded() bool {
	return r.Activity != nil && r.Activity.Success || r.Session != nil && r.Session.Success
}

type activityInput struct {
	ID             string          `json:"id"`
	AgentSessionID string          `json:"agentSessionId"`
	Content        activityContent `json:"content"`
	// Signal and SignalMetadata carry Linear's select signal on an
	// elicitation: the options a user may pick, each with the label shown
	// and the value Linear hands back (ATC-309).
	Signal         string          `json:"signal,omitempty"`
	SignalMetadata *signalMetadata `json:"signalMetadata,omitempty"`
}

type signalMetadata struct {
	Options []selectOption `json:"options"`
}

type selectOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type activityContent struct {
	Type string `json:"type"`
	Body string `json:"body"`
}

type linksInput struct {
	ID           string        `json:"id"`
	ExternalURLs []externalURL `json:"externalUrls"`
}

type externalURL struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}
