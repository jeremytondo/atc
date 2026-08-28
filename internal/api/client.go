package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Bodies from a misbehaving peer are read to a cap, never to EOF: problem
// documents larger than this are truncated, and a response still streaming
// past it forfeits connection reuse instead of blocking the call.
const maxBodyRead = 1 << 20

// Client is the typed /v1 client every in-repo Go consumer speaks through;
// TUI and CLI commands use it and never touch server internals. One
// request path sets the bearer token and Atc-Client-Version and reads
// Atc-Server-Version on every exchange, so the skew handshake lives in
// exactly one place; each operation is a small typed method over it.
type Client struct {
	baseURL         string
	token           string
	version         string
	httpClient      *http.Client
	onServerVersion func(version string)
}

// NewClient returns a client for the server at baseURL (scheme and
// authority, e.g. "http://127.0.0.1:4779"). token may be empty for
// tokenless probing — the server's version still comes back on the typed
// error of the resulting 401. version is the client build identity, for
// skew reporting. A nil httpClient defaults to a plain http.Client; pass
// one to set timeouts or transports. onServerVersion, when non-nil, is
// invoked with the Atc-Server-Version header of every response that
// carries one — success or failure — which is how callers observe the
// server's side of the skew handshake without the client holding mutable
// last-response state; it runs on the calling goroutine.
func NewClient(baseURL, token, version string, httpClient *http.Client, onServerVersion func(version string)) *Client {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &Client{
		baseURL:         strings.TrimSuffix(baseURL, "/"),
		token:           token,
		version:         version,
		httpClient:      httpClient,
		onServerVersion: onServerVersion,
	}
}

// Health reports the server's liveness and build version.
func (c *Client) Health(ctx context.Context) (Health, error) {
	var health Health
	err := c.do(ctx, http.MethodGet, "/v1/health", nil, &health)
	return health, err
}

// CreateTerminal creates a terminal and starts its session, returning the
// resource once its status has settled.
func (c *Client) CreateTerminal(ctx context.Context, params TerminalCreateParams) (Terminal, error) {
	var terminal Terminal
	err := c.do(ctx, http.MethodPost, "/v1/terminals", params, &terminal)
	return terminal, err
}

// Terminal fetches one terminal by ID.
func (c *Client) Terminal(ctx context.Context, id string) (Terminal, error) {
	var terminal Terminal
	err := c.do(ctx, http.MethodGet, "/v1/terminals/"+id, nil, &terminal)
	return terminal, err
}

// Terminals lists every terminal, exited and missing ones included. A
// non-empty projectID filters to that project's terminals.
func (c *Client) Terminals(ctx context.Context, projectID string) ([]Terminal, error) {
	path := "/v1/terminals"
	if projectID != "" {
		path += "?project=" + url.QueryEscape(projectID)
	}
	var list TerminalList
	err := c.do(ctx, http.MethodGet, path, nil, &list)
	return list.Terminals, err
}

// UpdateTerminal renames a terminal; name is the only mutable field.
func (c *Client) UpdateTerminal(ctx context.Context, id string, params TerminalUpdateParams) (Terminal, error) {
	var terminal Terminal
	err := c.do(ctx, http.MethodPatch, "/v1/terminals/"+id, params, &terminal)
	return terminal, err
}

// DeleteTerminal stops the session best-effort and removes the record.
func (c *Client) DeleteTerminal(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/terminals/"+id, nil, nil)
}

// CreateProject registers a project rooted at a directory.
func (c *Client) CreateProject(ctx context.Context, params ProjectCreateParams) (Project, error) {
	var project Project
	err := c.do(ctx, http.MethodPost, "/v1/projects", params, &project)
	return project, err
}

// Project fetches one project by ID.
func (c *Client) Project(ctx context.Context, id string) (Project, error) {
	var project Project
	err := c.do(ctx, http.MethodGet, "/v1/projects/"+id, nil, &project)
	return project, err
}

// Projects lists every project.
func (c *Client) Projects(ctx context.Context) ([]Project, error) {
	var list ProjectList
	err := c.do(ctx, http.MethodGet, "/v1/projects", nil, &list)
	return list.Projects, err
}

// UpdateProject renames a project; name is the only mutable field.
func (c *Client) UpdateProject(ctx context.Context, id string, params ProjectUpdateParams) (Project, error) {
	var project Project
	err := c.do(ctx, http.MethodPatch, "/v1/projects/"+id, params, &project)
	return project, err
}

// DeleteProject removes an empty project; a project that still holds
// terminals is refused.
func (c *Client) DeleteProject(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/projects/"+id, nil, nil)
}

// Raw performs one authenticated request over the contract and returns the
// HTTP response for the caller to stream and close — the `atc api` gateway.
// It rides the same request path as every typed method (auth, version
// headers, skew callback) but leaves status handling to the caller.
func (c *Client) Raw(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return c.send(ctx, method, path, body)
}

// send is the one place requests are built and executed: bearer token,
// version header both ways, and the server-version callback live here for
// every exchange, typed or raw.
func (c *Client) send(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set(ClientVersionHeader, c.version)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if c.onServerVersion != nil {
		if serverVersion := resp.Header.Get(ServerVersionHeader); serverVersion != "" {
			c.onServerVersion(serverVersion)
		}
	}
	return resp, nil
}

// do is the single request path. Any HTTP response that is not a decodable
// success returns *Problem — the server's own problem document when it
// sent one, a synthesized one otherwise — so a *Problem uniformly means
// "the server answered". Any other error means no usable HTTP response
// arrived at all.
func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		encoded, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encoding %s %s request: %w", method, path, err)
		}
		body = bytes.NewReader(encoded)
	}
	resp, err := c.send(ctx, method, path, body)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() {
		// Drain a bounded amount so healthy keep-alive connections are
		// reusable; a peer still streaming past the cap loses reuse, not
		// the caller's time.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodyRead))
		_ = resp.Body.Close()
	}()
	serverVersion := resp.Header.Get(ServerVersionHeader)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return problemFrom(resp, serverVersion)
	}
	if out != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodyRead)).Decode(out); err != nil {
			// The server answered; a body we cannot decode must not look
			// like a transport failure to callers branching on *Problem.
			return &Problem{
				Title:         "malformed response body",
				Status:        resp.StatusCode,
				Detail:        fmt.Sprintf("decoding %s %s response: %v", method, path, err),
				ServerVersion: serverVersion,
			}
		}
	}
	return nil
}

// problemFrom turns a non-2xx response into a *Problem. A body that is not
// a problem document (a proxy's error page, some other process on the
// port) degrades to the status line instead of failing the decode — the
// caller still gets a typed error with the right status.
func problemFrom(resp *http.Response, serverVersion string) *Problem {
	problem := &Problem{}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyRead))
	if err := json.Unmarshal(body, problem); err != nil || problem.Status == 0 {
		*problem = Problem{Title: http.StatusText(resp.StatusCode)}
	}
	// The wire status member is advisory; callers branch on what the
	// transport actually said, immune to a lying or rewritten body.
	problem.Status = resp.StatusCode
	problem.ServerVersion = serverVersion
	return problem
}
