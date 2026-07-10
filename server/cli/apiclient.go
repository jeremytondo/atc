package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"time"

	"github.com/coder/websocket"
)

type apiClient struct {
	socketPath string
	token      string
	httpClient *http.Client
}

func newAPIClient(socketPath string) *apiClient {
	return newAPIClientWithToken(socketPath, "")
}

func newAPIClientWithToken(socketPath, token string) *apiClient {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialer := net.Dialer{Timeout: 2 * time.Second}
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}

	return &apiClient{
		socketPath: socketPath,
		token:      token,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   5 * time.Second,
		},
	}
}

func (client *apiClient) get(ctx context.Context, endpoint string) ([]byte, error) {
	return client.getQuery(ctx, endpoint, nil)
}

func (client *apiClient) getQuery(ctx context.Context, endpoint string, query url.Values) ([]byte, error) {
	reqURL := "http://localhost" + apiPath(endpoint)
	if len(query) > 0 {
		reqURL += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build API request: %w", err)
	}
	client.authorize(req)

	resp, err := client.httpClient.Do(req)
	if err != nil {
		return nil, client.serviceUnavailableError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read Atelier Code service API response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, apiError(resp.StatusCode, body)
	}
	return body, nil
}

// post sends a JSON body to the endpoint and returns the response body. It
// surfaces the API's structured error message for non-2xx responses.
func (client *apiClient) post(ctx context.Context, endpoint string, body any) ([]byte, error) {
	return client.send(ctx, http.MethodPost, endpoint, body)
}

// patch mirrors post with the PATCH method.
func (client *apiClient) patch(ctx context.Context, endpoint string, body any) ([]byte, error) {
	return client.send(ctx, http.MethodPatch, endpoint, body)
}

func (client *apiClient) send(ctx context.Context, method, endpoint string, body any) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode API request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, method, "http://localhost"+apiPath(endpoint), bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build API request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	client.authorize(req)

	resp, err := client.httpClient.Do(req)
	if err != nil {
		return nil, client.serviceUnavailableError(err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read Atelier Code service API response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, apiError(resp.StatusCode, respBody)
	}
	return respBody, nil
}

func (client *apiClient) dialAttach(ctx context.Context, id string) (*websocket.Conn, error) {
	header := http.Header{}
	if client.token != "" {
		header.Set("Authorization", "Bearer "+client.token)
	}
	conn, resp, err := websocket.Dial(ctx, "ws://localhost"+apiPath(sessionEndpoint(id, "attach")), &websocket.DialOptions{
		HTTPClient: client.httpClient,
		HTTPHeader: header,
	})
	if err == nil {
		return conn, nil
	}
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
		body, readErr := io.ReadAll(resp.Body)
		if readErr == nil && resp.StatusCode != 0 {
			return nil, apiError(resp.StatusCode, body)
		}
	}
	return nil, fmt.Errorf("attach to Atelier Code session %s: %w", id, err)
}

func (client *apiClient) authorize(req *http.Request) {
	if client.token != "" {
		req.Header.Set("Authorization", "Bearer "+client.token)
	}
}

func (client *apiClient) serviceUnavailableError(err error) error {
	return fmt.Errorf("Atelier Code service is not running or socket unavailable at %s: %w", client.socketPath, err)
}

// apiError builds an error from a non-2xx response, preferring the API's
// structured { "error", "message" } body when present.
func apiError(status int, body []byte) error {
	var e struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &e); err == nil && e.Message != "" {
		return fmt.Errorf("%s (HTTP %d)", e.Message, status)
	}
	return fmt.Errorf("Atelier Code service API returned HTTP %d", status)
}

func apiPath(endpoint string) string {
	return path.Join("/api", endpoint)
}
