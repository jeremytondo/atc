package core_test

import (
	"context"
	"slices"
	"testing"

	"github.com/elevenideas/atc/experiments/plugin-orchestration/internal/core"
	"github.com/elevenideas/atc/experiments/plugin-orchestration/internal/fakes"
	"github.com/elevenideas/atc/experiments/plugin-orchestration/internal/model"
	"github.com/elevenideas/atc/experiments/plugin-orchestration/internal/plugin"
)

func TestContrastingPluginsShareOneResourceAndLifecycleSurface(t *testing.T) {
	ctx := context.Background()
	agent := fakes.NewAgent()
	workspace := fakes.NewWorkspace()
	service, err := core.New(agent, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RefreshAll(ctx); err != nil {
		t.Fatal(err)
	}
	if err := service.RefreshAll(ctx); err != nil {
		t.Fatal(err)
	}
	if events := service.EventsAfter(0); len(events) != 2 {
		t.Fatalf("unchanged refresh emitted events: %#v", events)
	}

	plugins := service.Plugins()
	if len(plugins) != 2 || plugins[0].ID != "fake-agent" || plugins[1].ID != "fake-workspace" {
		t.Fatalf("plugins = %#v", plugins)
	}
	observed := onlyResource(t, service.Resources("fake-workspace", "coding_session"))
	if observed.Owner.Scope != model.OwnerExternal || observed.Control != model.ControlObserved {
		t.Fatalf("observed ownership/control = %#v / %q", observed.Owner, observed.Control)
	}
	if !slices.Equal(observed.Actions, []model.Capability{model.CapabilityOpenExternal}) {
		t.Fatalf("observed actions = %#v", observed.Actions)
	}
	if observed.ID == observed.Source.NativeID || observed.Source.PluginID != "fake-workspace" {
		t.Fatalf("canonical/source identity = %q / %#v", observed.ID, observed.Source)
	}
	if _, ok := observed.Extensions["fake-workspace"]; !ok {
		t.Fatalf("namespaced extensions = %#v", observed.Extensions)
	}

	created, err := service.Create(ctx, "fake-agent", plugin.CreateRequest{Kind: "agent_session", Title: "Test task"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Owner.Scope != model.OwnerATC || created.Control != model.ControlManaged || created.Status.Activity != model.ActivityIdle {
		t.Fatalf("created resource = %#v", created)
	}
	providerUpdate, err := agent.NeedsInput(created.Source.NativeID)
	if err != nil {
		t.Fatal(err)
	}
	needsInput, err := service.Update("fake-agent", providerUpdate)
	if err != nil {
		t.Fatal(err)
	}
	if needsInput.Status.Activity != model.ActivityNeedsInput {
		t.Fatalf("provider update = %#v", needsInput.Status)
	}
	responded, err := service.Act(ctx, created.ID, model.CapabilityRespond, plugin.ActionRequest{Text: "Proceed"})
	if err != nil {
		t.Fatal(err)
	}
	if responded.Resource.Status.Activity != model.ActivityWorking {
		t.Fatalf("responded status = %#v", responded.Resource.Status)
	}
	cancelled, err := service.Act(ctx, created.ID, model.CapabilityCancel, plugin.ActionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Resource.Status.Phase != model.PhaseEnded || cancelled.Resource.Status.Outcome != model.OutcomeCancelled || len(cancelled.Resource.Actions) != 0 {
		t.Fatalf("cancelled resource = %#v", cancelled.Resource)
	}
	if _, err := service.Act(ctx, created.ID, model.CapabilityCancel, plugin.ActionRequest{}); core.ErrorCode(err) != "action_unavailable" {
		t.Fatalf("second cancel error = %v", err)
	}
	if _, err := service.Act(ctx, observed.ID, model.CapabilityCancel, plugin.ActionRequest{}); core.ErrorCode(err) != "action_unavailable" {
		t.Fatalf("read-only cancel error = %v", err)
	}
	opened, err := service.Act(ctx, observed.ID, model.CapabilityOpenExternal, plugin.ActionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if opened.Link == nil || opened.Link.Rel != "external" {
		t.Fatalf("open result = %#v", opened)
	}

	events := service.EventsAfter(0)
	if len(events) != 7 {
		t.Fatalf("events = %d: %#v", len(events), events)
	}
	for index, event := range events {
		if event.Sequence != uint64(index+1) {
			t.Fatalf("event %d sequence = %d", index, event.Sequence)
		}
	}
	if events[3].Status.Activity != model.ActivityNeedsInput || events[4].Action != model.CapabilityRespond || events[5].Status.Outcome != model.OutcomeCancelled {
		t.Fatalf("lifecycle events = %#v", events)
	}
	if after := service.EventsAfter(5); len(after) != 2 || after[0].Sequence != 6 {
		t.Fatalf("cursor catch-up = %#v", after)
	}
}

func TestDelegatedExternalResourceCanExposeNarrowControl(t *testing.T) {
	ctx := context.Background()
	service, err := core.New(fakes.NewWorkspace())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RefreshAll(ctx); err != nil {
		t.Fatal(err)
	}
	terminal := onlyResource(t, service.Resources("", "terminal_session"))
	if terminal.Owner.Scope != model.OwnerExternal || terminal.Control != model.ControlDelegated {
		t.Fatalf("terminal ownership/control = %#v / %q", terminal.Owner, terminal.Control)
	}
	attached, err := service.Act(ctx, terminal.ID, model.CapabilityAttach, plugin.ActionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if attached.Link == nil || attached.Link.Rel != "attach" {
		t.Fatalf("attach result = %#v", attached)
	}
	stopped, err := service.Act(ctx, terminal.ID, model.CapabilityControl, plugin.ActionRequest{Command: "stop"})
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Resource.Status.Phase != model.PhaseEnded || slices.Contains(stopped.Resource.Actions, model.CapabilityAttach) {
		t.Fatalf("stopped terminal = %#v", stopped.Resource)
	}
	if _, err := service.Act(ctx, terminal.ID, model.CapabilityAttach, plugin.ActionRequest{}); core.ErrorCode(err) != "action_unavailable" {
		t.Fatalf("stopped attach error = %v", err)
	}
}

func TestRegistrationRejectsCapabilityClaimsWithoutImplementation(t *testing.T) {
	_, err := core.New(invalidPlugin{})
	if core.ErrorCode(err) != "invalid_plugin" {
		t.Fatalf("registration error = %v", err)
	}
}

type invalidPlugin struct{}

func (invalidPlugin) Descriptor() model.PluginDescriptor {
	return model.PluginDescriptor{
		ID: "invalid", Name: "Invalid",
		ResourceTypes: []model.ResourceType{{
			Kind: "thing", Capabilities: []model.Capability{model.CapabilityDiscover, model.CapabilityCancel},
		}},
	}
}

func (invalidPlugin) Discover(context.Context) ([]model.Resource, error) { return nil, nil }

func onlyResource(t *testing.T, resources []model.Resource) model.Resource {
	t.Helper()
	if len(resources) != 1 {
		t.Fatalf("resources = %#v", resources)
	}
	return resources[0]
}
