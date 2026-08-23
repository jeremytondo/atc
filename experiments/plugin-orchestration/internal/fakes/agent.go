// Package fakes supplies deliberately unlike in-memory integrations so the
// orchestration model can be exercised without provider processes or accounts.
package fakes

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/elevenideas/atc/experiments/plugin-orchestration/internal/model"
	"github.com/elevenideas/atc/experiments/plugin-orchestration/internal/plugin"
)

type Agent struct {
	mu        sync.Mutex
	nextID    int
	resources map[string]model.Resource
}

func NewAgent() *Agent {
	return &Agent{resources: make(map[string]model.Resource)}
}

func (*Agent) Descriptor() model.PluginDescriptor {
	return model.PluginDescriptor{
		ID: "fake-agent", Name: "Fake managed agent",
		ResourceTypes: []model.ResourceType{{
			Kind: "agent_session",
			Capabilities: []model.Capability{
				model.CapabilityDiscover, model.CapabilityCreate,
				model.CapabilityRespond, model.CapabilityCancel,
			},
		}},
	}
}

func (a *Agent) Discover(context.Context) ([]model.Resource, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make([]model.Resource, 0, len(a.resources))
	for _, resource := range a.resources {
		result = append(result, resource)
	}
	return result, nil
}

func (a *Agent) Create(_ context.Context, request plugin.CreateRequest) (model.Resource, error) {
	if request.Kind != "agent_session" {
		return model.Resource{}, model.NewError("unsupported_kind", "fake agent creates only agent_session resources")
	}
	if request.Title == "" {
		return model.Resource{}, model.NewError("invalid_request", "title cannot be empty")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nextID++
	nativeID := fmt.Sprintf("session-%d", a.nextID)
	resource := model.Resource{
		Kind: "agent_session", Title: request.Title,
		Source:  model.Source{NativeID: nativeID},
		Owner:   model.Owner{Scope: model.OwnerATC, ID: "atc", Label: "ATC"},
		Control: model.ControlManaged,
		Actions: []model.Capability{model.CapabilityRespond, model.CapabilityCancel},
		Status:  model.Status{Phase: model.PhaseActive, Activity: model.ActivityIdle},
		Extensions: map[string]json.RawMessage{
			"fake-agent": json.RawMessage(fmt.Sprintf(`{"providerSessionId":%q}`, "provider-"+nativeID)),
		},
	}
	a.resources[nativeID] = resource
	return resource, nil
}

func (a *Agent) Respond(_ context.Context, nativeID, text string) (model.Resource, error) {
	if text == "" {
		return model.Resource{}, model.NewError("invalid_request", "response text cannot be empty")
	}
	return a.change(nativeID, func(resource *model.Resource) {
		resource.Status = model.Status{
			Phase: model.PhaseActive, Activity: model.ActivityWorking,
			Detail: "agent accepted a response", UpdatedAt: time.Now().UTC(),
		}
	})
}

func (a *Agent) Cancel(_ context.Context, nativeID string) (model.Resource, error) {
	return a.change(nativeID, func(resource *model.Resource) {
		resource.Status = model.Status{
			Phase: model.PhaseEnded, Outcome: model.OutcomeCancelled,
			Detail: "cancelled through ATC", UpdatedAt: time.Now().UTC(),
		}
		resource.Actions = []model.Capability{}
	})
}

// NeedsInput simulates an unsolicited provider lifecycle update. A real
// integration would call the core update seam from its protocol event loop.
func (a *Agent) NeedsInput(nativeID string) (model.Resource, error) {
	return a.change(nativeID, func(resource *model.Resource) {
		resource.Status = model.Status{
			Phase: model.PhaseActive, Activity: model.ActivityNeedsInput,
			Detail: "provider requested a decision", UpdatedAt: time.Now().UTC(),
		}
	})
}

func (a *Agent) change(nativeID string, change func(*model.Resource)) (model.Resource, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	resource, ok := a.resources[nativeID]
	if !ok {
		return model.Resource{}, model.NewError("resource_not_found", "agent session not found")
	}
	change(&resource)
	a.resources[nativeID] = resource
	return resource, nil
}
