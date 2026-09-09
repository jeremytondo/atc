package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
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

// CreateTerminal creates a terminal and starts its session — a shell, a
// command, an App, or a thread's resume — returning the resource once
// its status has settled; a thread resume returns the terminal already
// holding the thread when one is running.
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
// non-empty spaceID filters to that space's terminals.
func (c *Client) Terminals(ctx context.Context, spaceID string) ([]Terminal, error) {
	path := "/v1/terminals"
	if spaceID != "" {
		path += "?space=" + url.QueryEscape(spaceID)
	}
	var list TerminalList
	err := c.do(ctx, http.MethodGet, path, nil, &list)
	return list.Terminals, err
}

// UpdateTerminal applies a merge patch: rename, move to another space.
func (c *Client) UpdateTerminal(ctx context.Context, id string, params TerminalUpdateParams) (Terminal, error) {
	var terminal Terminal
	err := c.do(ctx, http.MethodPatch, "/v1/terminals/"+id, params, &terminal)
	return terminal, err
}

// DeleteTerminal stops the session best-effort and removes the record.
func (c *Client) DeleteTerminal(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/terminals/"+id, nil, nil)
}

// CreateSpace registers a space for terminals to belong to.
func (c *Client) CreateSpace(ctx context.Context, params SpaceCreateParams) (Space, error) {
	var space Space
	err := c.do(ctx, http.MethodPost, "/v1/spaces", params, &space)
	return space, err
}

// Space fetches one space by ID.
func (c *Client) Space(ctx context.Context, id string) (Space, error) {
	var space Space
	err := c.do(ctx, http.MethodGet, "/v1/spaces/"+id, nil, &space)
	return space, err
}

// Spaces lists every space, the Default one included.
func (c *Client) Spaces(ctx context.Context) ([]Space, error) {
	var list SpaceList
	err := c.do(ctx, http.MethodGet, "/v1/spaces", nil, &list)
	return list.Spaces, err
}

// UpdateSpace applies a merge patch to a space's name and directory.
func (c *Client) UpdateSpace(ctx context.Context, id string, params SpaceUpdateParams) (Space, error) {
	var space Space
	err := c.do(ctx, http.MethodPatch, "/v1/spaces/"+id, params, &space)
	return space, err
}

// DeleteSpace deletes a space and every terminal in it; the Default
// space is refused.
func (c *Client) DeleteSpace(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/spaces/"+id, nil, nil)
}

// Directories lists the immediate subdirectories of path on the server's
// machine; an empty path lists the server user's home directory.
func (c *Client) Directories(ctx context.Context, path string) (DirectoryList, error) {
	route := "/v1/directories"
	if path != "" {
		route += "?path=" + url.QueryEscape(path)
	}
	var list DirectoryList
	err := c.do(ctx, http.MethodGet, route, nil, &list)
	return list, err
}

// Integrations lists the compiled-in Integrations with their Apps, agents,
// and evidence-based health, probed against the server's machine at
// request time.
func (c *Client) Integrations(ctx context.Context) ([]Integration, error) {
	var list IntegrationList
	err := c.do(ctx, http.MethodGet, "/v1/integrations", nil, &list)
	return list.Integrations, err
}

// Integration fetches one Integration by id. The id is user-typed on the
// CLI, so it is escaped: reserved characters make an unknown id, not a
// different route.
func (c *Client) Integration(ctx context.Context, id string) (Integration, error) {
	var integration Integration
	err := c.do(ctx, http.MethodGet, "/v1/integrations/"+url.PathEscape(id), nil, &integration)
	return integration, err
}

// Threads lists threads, newest-created last. Non-empty projectID and
// terminalID filter; archived threads are included only when asked.
func (c *Client) Threads(ctx context.Context, projectID, terminalID string, includeArchived bool) ([]Thread, error) {
	query := url.Values{}
	if projectID != "" {
		query.Set("project", projectID)
	}
	if terminalID != "" {
		query.Set("terminal", terminalID)
	}
	if includeArchived {
		query.Set("includeArchived", "true")
	}
	path := "/v1/threads"
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	var list ThreadList
	err := c.do(ctx, http.MethodGet, path, nil, &list)
	return list.Threads, err
}

// CreateThread starts a new conversation with its first prompt in an
// Integration's program (ATC-289), returning the thread as it stands once
// the program has committed the creation.
func (c *Client) CreateThread(ctx context.Context, params ThreadCreateParams) (Thread, error) {
	var thread Thread
	err := c.do(ctx, http.MethodPost, "/v1/threads", params, &thread)
	return thread, err
}

// Thread fetches one thread by ID.
func (c *Client) Thread(ctx context.Context, id string) (Thread, error) {
	var thread Thread
	err := c.do(ctx, http.MethodGet, "/v1/threads/"+id, nil, &thread)
	return thread, err
}

// UpdateThread merge-patches a thread's title, archived flag, and project
// (an explicit null clears the project) — archive and unarchive are this
// PATCH, there are no action routes.
func (c *Client) UpdateThread(ctx context.Context, id string, params ThreadUpdateParams) (Thread, error) {
	var thread Thread
	err := c.do(ctx, http.MethodPatch, "/v1/threads/"+id, params, &thread)
	return thread, err
}

// DeleteThread removes ATC's record of the conversation; the
// provider-side conversation is untouched.
func (c *Client) DeleteThread(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/threads/"+id, nil, nil)
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

// UpdateProject applies a merge patch to a project's name and directory.
func (c *Client) UpdateProject(ctx context.Context, id string, params ProjectUpdateParams) (Project, error) {
	var project Project
	err := c.do(ctx, http.MethodPatch, "/v1/projects/"+id, params, &project)
	return project, err
}

// DeleteProject removes a project; its threads survive unassigned.
func (c *Client) DeleteProject(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/projects/"+id, nil, nil)
}

// PublishArtifact creates an artifact from a publication: the params
// and the build and source archives (gzip tars), streamed as one
// multipart request.
func (c *Client) PublishArtifact(ctx context.Context, params ArtifactPublishParams, build, source io.Reader) (ArtifactPublication, error) {
	return c.publish(ctx, "/v1/artifacts", params, build, source)
}

// PublishArtifactVersion appends a version to an artifact; a nil build
// and source with params.RestoreFrom set restores a stored version.
func (c *Client) PublishArtifactVersion(ctx context.Context, id string, params ArtifactPublishParams, build, source io.Reader) (ArtifactPublication, error) {
	return c.publish(ctx, "/v1/artifacts/"+url.PathEscape(id)+"/versions", params, build, source)
}

func (c *Client) publish(ctx context.Context, path string, params ArtifactPublishParams, build, source io.Reader) (ArtifactPublication, error) {
	encoded, err := json.Marshal(params)
	if err != nil {
		return ArtifactPublication{}, fmt.Errorf("encoding publication params: %w", err)
	}
	// The multipart body is streamed through a pipe so an archive is never
	// held in memory whole; a writer failure surfaces as the request's.
	reader, writer := io.Pipe()
	form := multipart.NewWriter(writer)
	go func() {
		err := writeForm(form, encoded, build, source)
		_ = writer.CloseWithError(err)
	}()
	var result ArtifactPublication
	if err := c.exchange(ctx, http.MethodPost, path, reader, form.FormDataContentType(), &result); err != nil {
		_ = reader.Close()
		return ArtifactPublication{}, err
	}
	return result, nil
}

func writeForm(form *multipart.Writer, params []byte, build, source io.Reader) error {
	part, err := form.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": {`form-data; name="params"`}, "Content-Type": {"application/json"},
	})
	if err != nil {
		return err
	}
	if _, err := part.Write(params); err != nil {
		return err
	}
	for name, archive := range map[string]io.Reader{"build": build, "source": source} {
		if archive == nil {
			continue
		}
		part, err := form.CreatePart(textproto.MIMEHeader{
			"Content-Disposition": {fmt.Sprintf(`form-data; name="%s"; filename="%s.tar.gz"`, name, name)},
			"Content-Type":        {"application/gzip"},
		})
		if err != nil {
			return err
		}
		if _, err := io.Copy(part, archive); err != nil {
			return fmt.Errorf("reading %s archive: %w", name, err)
		}
	}
	return form.Close()
}

// Artifacts lists artifacts, or only projectID's when it is set.
func (c *Client) Artifacts(ctx context.Context, projectID string) ([]Artifact, error) {
	path := "/v1/artifacts"
	if projectID != "" {
		path += "?" + url.Values{"project": {projectID}}.Encode()
	}
	var list ArtifactList
	err := c.do(ctx, http.MethodGet, path, nil, &list)
	return list.Artifacts, err
}

// Artifact fetches one artifact.
func (c *Client) Artifact(ctx context.Context, id string) (Artifact, error) {
	var artifact Artifact
	err := c.do(ctx, http.MethodGet, "/v1/artifacts/"+url.PathEscape(id), nil, &artifact)
	return artifact, err
}

// UpdateArtifact applies a merge patch: title, Project, or both.
func (c *Client) UpdateArtifact(ctx context.Context, id string, params ArtifactUpdateParams) (Artifact, error) {
	var artifact Artifact
	err := c.do(ctx, http.MethodPatch, "/v1/artifacts/"+url.PathEscape(id), params, &artifact)
	return artifact, err
}

// DeleteArtifact removes an artifact and its whole history.
func (c *Client) DeleteArtifact(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/artifacts/"+url.PathEscape(id), nil, nil)
}

// ArtifactVersions lists an artifact's history, oldest first.
func (c *Client) ArtifactVersions(ctx context.Context, id string) ([]ArtifactVersion, error) {
	var list ArtifactVersionList
	err := c.do(ctx, http.MethodGet, "/v1/artifacts/"+url.PathEscape(id)+"/versions", nil, &list)
	return list.Versions, err
}

// ArtifactVersion fetches one version.
func (c *Client) ArtifactVersion(ctx context.Context, id string, number int) (ArtifactVersion, error) {
	var version ArtifactVersion
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/v1/artifacts/%s/versions/%d", url.PathEscape(id), number), nil, &version)
	return version, err
}

// ArtifactSource streams a version's source archive (a gzip tar); the
// caller closes it. A non-2xx answer is returned as *Problem.
func (c *Client) ArtifactSource(ctx context.Context, id string, number int) (io.ReadCloser, error) {
	resp, err := c.send(ctx, http.MethodGet, fmt.Sprintf("/v1/artifacts/%s/versions/%d/source", url.PathEscape(id), number), nil, "")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		defer func() { _ = resp.Body.Close() }()
		return nil, problemFrom(resp, resp.Header.Get(ServerVersionHeader))
	}
	return resp.Body, nil
}

// Documents reports the document origin: readiness, local base URL, and
// tailnet exposure.
func (c *Client) Documents(ctx context.Context) (Documents, error) {
	var status Documents
	err := c.do(ctx, http.MethodGet, "/v1/documents", nil, &status)
	return status, err
}

// Webhooks reports the state of webhook ingress: readiness, public URL,
// registered routes, any awaited setup action, and inbox counters.
func (c *Client) Webhooks(ctx context.Context) (Webhooks, error) {
	var status Webhooks
	err := c.do(ctx, http.MethodGet, "/v1/webhooks", nil, &status)
	return status, err
}

// Raw performs one authenticated request over the contract and returns the
// HTTP response for the caller to stream and close — the `atc api` gateway.
// It rides the same request path as every typed method (auth, version
// headers, skew callback) but leaves status handling to the caller.
func (c *Client) Raw(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return c.send(ctx, method, path, body, "application/json")
}

// send is the one place requests are built and executed: bearer token,
// version header both ways, and the server-version callback live here for
// every exchange, typed or raw.
func (c *Client) send(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", contentType)
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
	return c.exchange(ctx, method, path, body, "application/json", out)
}

// exchange is do for an already-encoded body of any content type.
func (c *Client) exchange(ctx context.Context, method, path string, body io.Reader, contentType string, out any) error {
	resp, err := c.send(ctx, method, path, body, contentType)
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
