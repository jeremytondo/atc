package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Errors from a misbehaving peer are capped rather than trusted to be
// small.
const maxProblemBody = 1 << 20

// Client is the typed /v1 client every in-repo Go consumer speaks through;
// TUI and CLI commands use it and never touch server internals. One
// request path sets the bearer token and Atc-Client-Version and reads
// Atc-Server-Version on every exchange, so the skew handshake lives in
// exactly one place; each operation is a small typed method over it.
type Client struct {
	baseURL    string
	token      string
	version    string
	httpClient *http.Client
}

// NewClient returns a client for the server at baseURL (scheme and
// authority, e.g. "http://127.0.0.1:4779"). token may be empty for
// tokenless probing — the server's version still comes back on the typed
// error of the resulting 401. version is the client build identity, for
// skew reporting. A nil httpClient defaults to a plain http.Client; pass
// one to set timeouts or transports.
func NewClient(baseURL, token, version string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &Client{
		baseURL:    strings.TrimSuffix(baseURL, "/"),
		token:      token,
		version:    version,
		httpClient: httpClient,
	}
}

// Health reports the server's liveness and build version.
func (c *Client) Health(ctx context.Context) (Health, error) {
	var health Health
	err := c.do(ctx, http.MethodGet, "/v1/health", &health)
	return health, err
}

// do is the single request path. A non-2xx response returns *Problem; any
// other error means no usable HTTP response arrived at all.
func (c *Client) do(ctx context.Context, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("building %s %s request: %w", method, path, err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set(ClientVersionHeader, c.version)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() {
		// Drain so the keep-alive connection is reusable.
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return problemFrom(resp)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decoding %s %s response: %w", method, path, err)
		}
	}
	return nil
}

// problemFrom turns a non-2xx response into a *Problem. A body that is not
// a problem document (a proxy's error page, some other process on the
// port) degrades to the status line instead of failing the decode — the
// caller still gets a typed error with the right status.
func problemFrom(resp *http.Response) *Problem {
	problem := &Problem{}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxProblemBody))
	if err := json.Unmarshal(body, problem); err != nil || problem.Status == 0 {
		*problem = Problem{Title: http.StatusText(resp.StatusCode), Status: resp.StatusCode}
	}
	problem.ServerVersion = resp.Header.Get(ServerVersionHeader)
	return problem
}
