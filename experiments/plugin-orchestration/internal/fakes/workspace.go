package fakes

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"time"

	"github.com/elevenideas/atc/experiments/plugin-orchestration/internal/model"
)

type Workspace struct {
	mu        sync.Mutex
	resources map[string]model.Resource
}

func NewWorkspace() *Workspace {
	return &Workspace{resources: map[string]model.Resource{
		"thread/external-42": {
			Kind: "coding_session", Title: "Discovered editor thread",
			Source:  model.Source{NativeID: "thread/external-42"},
			Owner:   model.Owner{Scope: model.OwnerExternal, ID: "developer@example.test", Label: "Developer"},
			Control: model.ControlObserved,
			Actions: []model.Capability{model.CapabilityOpenExternal},
			Status: model.Status{
				Phase: model.PhaseActive, Activity: model.ActivityWorking,
				Detail: "native state: generating",
			},
			Links: []model.Link{{Rel: "external", Label: "Open in editor", URL: "https://example.test/threads/external-42"}},
			Extensions: map[string]json.RawMessage{
				"fake-workspace": json.RawMessage(`{"workspace":"sample","nativeState":"generating"}`),
			},
		},
		"terminal:shared": {
			Kind: "terminal_session", Title: "Shared project shell",
			Source:  model.Source{NativeID: "terminal:shared"},
			Owner:   model.Owner{Scope: model.OwnerExternal, ID: "workspace-daemon", Label: "Workspace daemon"},
			Control: model.ControlDelegated,
			Actions: []model.Capability{model.CapabilityControl, model.CapabilityAttach, model.CapabilityOpenExternal},
			Status:  model.Status{Phase: model.PhaseActive, Activity: model.ActivityIdle},
			Links:   []model.Link{{Rel: "external", Label: "Open terminal dashboard", URL: "https://example.test/terminals/shared"}},
			Extensions: map[string]json.RawMessage{
				"fake-workspace": json.RawMessage(`{"socket":"private://workspace/terminal/shared"}`),
			},
		},
	}}
}

func (*Workspace) Descriptor() model.PluginDescriptor {
	return model.PluginDescriptor{
		ID: "fake-workspace", Name: "Fake external workspace",
		ResourceTypes: []model.ResourceType{
			{
				Kind:         "coding_session",
				Capabilities: []model.Capability{model.CapabilityDiscover, model.CapabilityOpenExternal},
			},
			{
				Kind: "terminal_session",
				Capabilities: []model.Capability{
					model.CapabilityDiscover, model.CapabilityControl,
					model.CapabilityAttach, model.CapabilityOpenExternal,
				},
			},
		},
	}
}

func (w *Workspace) Discover(context.Context) ([]model.Resource, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	result := make([]model.Resource, 0, len(w.resources))
	for _, resource := range w.resources {
		result = append(result, resource)
	}
	return result, nil
}

func (w *Workspace) Control(_ context.Context, nativeID, command string) (model.Resource, error) {
	return w.change(nativeID, func(resource *model.Resource) error {
		if resource.Kind != "terminal_session" {
			return model.NewError("unsupported_action", "only terminals accept control commands")
		}
		switch command {
		case "start":
			resource.Status = model.Status{
				Phase: model.PhaseActive, Activity: model.ActivityIdle,
				Detail: "started by workspace", UpdatedAt: time.Now().UTC(),
			}
			resource.Actions = []model.Capability{model.CapabilityControl, model.CapabilityAttach, model.CapabilityOpenExternal}
		case "stop":
			resource.Status = model.Status{
				Phase: model.PhaseEnded, Outcome: model.OutcomeSucceeded,
				Detail: "stopped by workspace", UpdatedAt: time.Now().UTC(),
			}
			resource.Actions = []model.Capability{model.CapabilityControl, model.CapabilityOpenExternal}
		default:
			return model.NewError("invalid_request", "control command must be start or stop")
		}
		return nil
	})
}

func (w *Workspace) Attach(_ context.Context, nativeID string) (model.Resource, model.Link, error) {
	resource, err := w.resource(nativeID)
	if err != nil {
		return model.Resource{}, model.Link{}, err
	}
	if resource.Kind != "terminal_session" || resource.Status.Phase != model.PhaseActive {
		return model.Resource{}, model.Link{}, model.NewError("attach_unavailable", "terminal is not active")
	}
	return resource, model.Link{
		Rel: "attach", Label: "Attach with prototype client",
		URL: "atc-proto://attach/" + nativeID,
	}, nil
}

func (w *Workspace) OpenExternal(_ context.Context, nativeID string) (model.Resource, model.Link, error) {
	resource, err := w.resource(nativeID)
	if err != nil {
		return model.Resource{}, model.Link{}, err
	}
	index := slices.IndexFunc(resource.Links, func(link model.Link) bool { return link.Rel == "external" })
	if index < 0 {
		return model.Resource{}, model.Link{}, model.NewError("open_unavailable", "resource has no external link")
	}
	return resource, resource.Links[index], nil
}

func (w *Workspace) resource(nativeID string) (model.Resource, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	resource, ok := w.resources[nativeID]
	if !ok {
		return model.Resource{}, model.NewError("resource_not_found", "workspace resource not found")
	}
	return resource, nil
}

func (w *Workspace) change(nativeID string, change func(*model.Resource) error) (model.Resource, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	resource, ok := w.resources[nativeID]
	if !ok {
		return model.Resource{}, model.NewError("resource_not_found", "workspace resource not found")
	}
	if err := change(&resource); err != nil {
		return model.Resource{}, err
	}
	w.resources[nativeID] = resource
	return resource, nil
}
