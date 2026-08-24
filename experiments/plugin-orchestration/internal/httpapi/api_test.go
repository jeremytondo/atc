package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/elevenideas/atc/experiments/plugin-orchestration/internal/core"
	"github.com/elevenideas/atc/experiments/plugin-orchestration/internal/fakes"
	"github.com/elevenideas/atc/experiments/plugin-orchestration/internal/httpapi"
	"github.com/elevenideas/atc/experiments/plugin-orchestration/internal/model"
)

func TestCanonicalHTTPWorkflow(t *testing.T) {
	service, err := core.New(fakes.NewAgent(), fakes.NewWorkspace())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RefreshAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(service))
	t.Cleanup(server.Close)

	plugins := request[struct {
		Plugins []model.PluginDescriptor `json:"plugins"`
	}](t, http.MethodGet, server.URL+"/v1/plugins", nil, http.StatusOK)
	if len(plugins.Plugins) != 2 {
		t.Fatalf("plugins = %#v", plugins)
	}

	request[map[string]any](t, http.MethodPost, server.URL+"/v1/resources", map[string]any{
		"pluginId": "fake-agent", "kind": "agent_session", "title": "HTTP task", "unknown": true,
	}, http.StatusBadRequest)
	created := request[model.Resource](t, http.MethodPost, server.URL+"/v1/resources", map[string]any{
		"pluginId": "fake-agent", "kind": "agent_session", "title": "HTTP task",
	}, http.StatusCreated)
	if created.Status.Activity != model.ActivityIdle {
		t.Fatalf("created = %#v", created)
	}

	inspected := request[model.Resource](t, http.MethodGet, server.URL+"/v1/resources/"+url.PathEscape(created.ID), nil, http.StatusOK)
	if inspected.ID != created.ID {
		t.Fatalf("inspected = %#v", inspected)
	}
	responded := request[struct {
		Resource model.Resource `json:"resource"`
	}](t, http.MethodPost, server.URL+"/v1/resources/"+url.PathEscape(created.ID)+"/actions/respond", map[string]string{"text": "Continue"}, http.StatusOK)
	if responded.Resource.Status.Activity != model.ActivityWorking {
		t.Fatalf("responded = %#v", responded)
	}

	observed := service.Resources("fake-workspace", "coding_session")[0]
	request[map[string]any](t, http.MethodPost, server.URL+"/v1/resources/"+url.PathEscape(observed.ID)+"/actions/cancel", nil, http.StatusConflict)
	opened := request[struct {
		Link *model.Link `json:"link"`
	}](t, http.MethodPost, server.URL+"/v1/resources/"+url.PathEscape(observed.ID)+"/actions/open_external", nil, http.StatusOK)
	if opened.Link == nil || opened.Link.URL == "" {
		t.Fatalf("opened = %#v", opened)
	}

	events := request[struct {
		Events []model.Event `json:"events"`
	}](t, http.MethodGet, server.URL+"/v1/events?after=2", nil, http.StatusOK)
	if len(events.Events) != 3 || events.Events[0].Sequence != 3 {
		t.Fatalf("events = %#v", events)
	}
}

func request[T any](t *testing.T, method, target string, body any, wantStatus int) T {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, target, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != wantStatus {
		contents, _ := io.ReadAll(response.Body)
		t.Fatalf("%s %s status = %d, body = %s", method, target, response.StatusCode, contents)
	}
	var result T
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}
