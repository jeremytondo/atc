package t3

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestExchangeBootstrapCredentialAndIssueTicket(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/oauth/token":
			if err := request.ParseForm(); err != nil {
				t.Errorf("parse token form: %v", err)
				response.WriteHeader(http.StatusBadRequest)
				return
			}
			if got := request.Form.Get("subject_token"); got != "bootstrap-secret" {
				t.Errorf("subject_token = %q", got)
			}
			if got := request.Form.Get("grant_type"); got != "urn:ietf:params:oauth:grant-type:token-exchange" {
				t.Errorf("grant_type = %q", got)
			}
			if got := request.Form.Get("scope"); got != requestedScopes {
				t.Errorf("scope = %q", got)
			}
			writeJSON(t, response, map[string]any{"access_token": "access-secret"})
		case "/api/auth/websocket-ticket":
			if got := request.Header.Get("Authorization"); got != "Bearer access-secret" {
				t.Errorf("Authorization = %q", got)
			}
			writeJSON(t, response, map[string]any{"ticket": "socket-secret"})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	auth, err := ExchangeBootstrapCredential(ctx, server.Client(), server.URL+"/", "bootstrap-secret")
	if err != nil {
		t.Fatal(err)
	}
	socketURL, err := auth.WebSocketURL(ctx)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(socketURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "ws" || parsed.Path != "/ws" || parsed.Query().Get("wsTicket") != "socket-secret" {
		t.Fatalf("unexpected websocket URL %q", socketURL)
	}
}

func TestCallUsesEffectRPCEnvelopes(t *testing.T) {
	t.Parallel()

	serverErrors := make(chan error, 1)
	server := websocketServer(t, func(ctx context.Context, connection *websocket.Conn) error {
		_, encoded, err := connection.Read(ctx)
		if err != nil {
			return err
		}
		var request struct {
			Tag     string         `json:"_tag"`
			ID      string         `json:"id"`
			Method  string         `json:"tag"`
			Payload map[string]any `json:"payload"`
			Headers []any          `json:"headers"`
		}
		if err := json.Unmarshal(encoded, &request); err != nil {
			return err
		}
		if request.Tag != "Request" || request.Method != "server.getConfig" || request.ID == "" {
			return fmt.Errorf("unexpected request: %s", encoded)
		}
		return connection.Write(ctx, websocket.MessageText, mustJSON(map[string]any{
			"_tag": "Exit", "requestId": request.ID,
			"exit": map[string]any{"_tag": "Success", "value": map[string]any{"version": "test"}},
		}))
	}, serverErrors)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := Dial(ctx, websocketURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var output struct {
		Version string `json:"version"`
	}
	if err := client.Call(ctx, "server.getConfig", map[string]any{"probe": true}, &output); err != nil {
		t.Fatal(err)
	}
	if output.Version != "test" {
		t.Fatalf("version = %q", output.Version)
	}
	assertNoServerError(t, serverErrors)
}

func TestSubscriptionAcknowledgesChunksAndInterrupts(t *testing.T) {
	t.Parallel()

	serverErrors := make(chan error, 1)
	server := websocketServer(t, func(ctx context.Context, connection *websocket.Conn) error {
		request := readEnvelope(t, ctx, connection)
		requestID, _ := request["id"].(string)
		if request["_tag"] != "Request" || request["tag"] != "orchestration.subscribeThread" || requestID == "" {
			return fmt.Errorf("unexpected subscription request: %#v", request)
		}
		if err := connection.Write(ctx, websocket.MessageText, mustJSON(map[string]any{
			"_tag": "Chunk", "requestId": requestID,
			"values": []any{map[string]any{"kind": "snapshot"}, map[string]any{"kind": "event"}},
		})); err != nil {
			return err
		}
		ack := readEnvelope(t, ctx, connection)
		if ack["_tag"] != "Ack" || ack["requestId"] != requestID {
			return fmt.Errorf("unexpected ack: %#v", ack)
		}
		interrupt := readEnvelope(t, ctx, connection)
		if interrupt["_tag"] != "Interrupt" || interrupt["requestId"] != requestID {
			return fmt.Errorf("unexpected interrupt: %#v", interrupt)
		}
		return nil
	}, serverErrors)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := Dial(ctx, websocketURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	subscription, err := client.Subscribe(ctx, "orchestration.subscribeThread", map[string]any{"threadId": "thread-1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"snapshot", "event"} {
		select {
		case encoded := <-subscription.Items:
			var item struct {
				Kind string `json:"kind"`
			}
			if err := json.Unmarshal(encoded, &item); err != nil {
				t.Fatal(err)
			}
			if item.Kind != want {
				t.Fatalf("item kind = %q, want %q", item.Kind, want)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	subscription.Close()
	assertNoServerError(t, serverErrors)
}

func websocketServer(
	t *testing.T,
	handle func(context.Context, *websocket.Conn) error,
	errors chan<- error,
) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		connection, err := websocket.Accept(response, request, nil)
		if err != nil {
			errors <- err
			return
		}
		defer connection.Close(websocket.StatusNormalClosure, "test complete")
		ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
		defer cancel()
		errors <- handle(ctx, connection)
	}))
}

func readEnvelope(t *testing.T, ctx context.Context, connection *websocket.Conn) map[string]any {
	t.Helper()
	_, encoded, err := connection.Read(ctx)
	if err != nil {
		return map[string]any{"readError": err.Error()}
	}
	var envelope map[string]any
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		return map[string]any{"decodeError": err.Error(), "raw": string(encoded)}
	}
	return envelope
}

func websocketURL(httpURL string) string {
	return "ws" + strings.TrimPrefix(httpURL, "http")
}

func mustJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

func writeJSON(t *testing.T, response http.ResponseWriter, value any) {
	t.Helper()
	response.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(response).Encode(value); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func assertNoServerError(t *testing.T, errors <-chan error) {
	t.Helper()
	select {
	case err := <-errors:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for websocket server")
	}
}
