// Package core owns plugin registration, canonical resource identity,
// capability enforcement, normalized snapshots, and the ordered event log.
// Plugin-specific behavior stops at the narrow interfaces in package plugin.
package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/elevenideas/atc/experiments/plugin-orchestration/internal/model"
	"github.com/elevenideas/atc/experiments/plugin-orchestration/internal/plugin"
)

type Service struct {
	mu           sync.RWMutex
	integrations map[string]plugin.Integration
	descriptors  map[string]model.PluginDescriptor
	resources    map[string]model.Resource
	events       []model.Event
	now          func() time.Time
}

func New(integrations ...plugin.Integration) (*Service, error) {
	service := &Service{
		integrations: make(map[string]plugin.Integration),
		descriptors:  make(map[string]model.PluginDescriptor),
		resources:    make(map[string]model.Resource),
		now:          time.Now,
	}
	for _, integration := range integrations {
		if err := service.register(integration); err != nil {
			return nil, err
		}
	}
	return service, nil
}

func (s *Service) Plugins() []model.PluginDescriptor {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]model.PluginDescriptor, 0, len(s.descriptors))
	for _, descriptor := range s.descriptors {
		result = append(result, cloneDescriptor(descriptor))
	}
	slices.SortFunc(result, func(left, right model.PluginDescriptor) int {
		return strings.Compare(left.ID, right.ID)
	})
	return result
}

func (s *Service) Refresh(ctx context.Context, pluginID string) ([]model.Resource, error) {
	integration, _, err := s.integration(pluginID)
	if err != nil {
		return nil, err
	}
	discovered, err := integration.Discover(ctx)
	if err != nil {
		return nil, fmt.Errorf("discover %s: %w", pluginID, err)
	}
	result := make([]model.Resource, 0, len(discovered))
	for _, resource := range discovered {
		normalized, normalizeErr := s.normalize(pluginID, resource)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		stored := s.store(normalized, "resource.discovered", "")
		result = append(result, stored)
	}
	slices.SortFunc(result, compareResources)
	return result, nil
}

func (s *Service) RefreshAll(ctx context.Context) error {
	for _, descriptor := range s.Plugins() {
		if _, err := s.Refresh(ctx, descriptor.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) Resources(pluginID, kind string) []model.Resource {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]model.Resource, 0, len(s.resources))
	for _, resource := range s.resources {
		if pluginID != "" && resource.Source.PluginID != pluginID {
			continue
		}
		if kind != "" && resource.Kind != kind {
			continue
		}
		result = append(result, cloneResource(resource))
	}
	slices.SortFunc(result, compareResources)
	return result
}

func (s *Service) Resource(id string) (model.Resource, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	resource, ok := s.resources[id]
	if !ok {
		return model.Resource{}, model.NewError("resource_not_found", "resource not found")
	}
	return cloneResource(resource), nil
}

func (s *Service) Create(ctx context.Context, pluginID string, request plugin.CreateRequest) (model.Resource, error) {
	integration, descriptor, err := s.integration(pluginID)
	if err != nil {
		return model.Resource{}, err
	}
	if !descriptorSupports(descriptor, request.Kind, model.CapabilityCreate) {
		return model.Resource{}, model.NewError("capability_unavailable", "plugin cannot create this resource kind")
	}
	creator := integration.(plugin.Creator)
	created, err := creator.Create(ctx, request)
	if err != nil {
		return model.Resource{}, err
	}
	normalized, err := s.normalize(pluginID, created)
	if err != nil {
		return model.Resource{}, err
	}
	return s.store(normalized, "resource.created", model.CapabilityCreate), nil
}

// Update is the ingestion seam for a plugin-originated lifecycle observation.
// Push, polling, and protocol-specific event transport stay inside the plugin.
func (s *Service) Update(pluginID string, resource model.Resource) (model.Resource, error) {
	if _, _, err := s.integration(pluginID); err != nil {
		return model.Resource{}, err
	}
	normalized, err := s.normalize(pluginID, resource)
	if err != nil {
		return model.Resource{}, err
	}
	return s.store(normalized, "resource.updated", ""), nil
}

func (s *Service) Act(ctx context.Context, id string, action model.Capability, request plugin.ActionRequest) (plugin.ActionResult, error) {
	resource, err := s.Resource(id)
	if err != nil {
		return plugin.ActionResult{}, err
	}
	if !slices.Contains(resource.Actions, action) {
		return plugin.ActionResult{}, model.NewError("action_unavailable", "action is not currently available for this resource")
	}
	integration, _, err := s.integration(resource.Source.PluginID)
	if err != nil {
		return plugin.ActionResult{}, err
	}

	var updated model.Resource
	var link *model.Link
	switch action {
	case model.CapabilityControl:
		updated, err = integration.(plugin.Controller).Control(ctx, resource.Source.NativeID, request.Command)
	case model.CapabilityRespond:
		updated, err = integration.(plugin.Responder).Respond(ctx, resource.Source.NativeID, request.Text)
	case model.CapabilityCancel:
		updated, err = integration.(plugin.Canceller).Cancel(ctx, resource.Source.NativeID)
	case model.CapabilityAttach:
		var value model.Link
		updated, value, err = integration.(plugin.Attacher).Attach(ctx, resource.Source.NativeID)
		link = &value
	case model.CapabilityOpenExternal:
		var value model.Link
		updated, value, err = integration.(plugin.ExternalOpener).OpenExternal(ctx, resource.Source.NativeID)
		link = &value
	default:
		return plugin.ActionResult{}, model.NewError("invalid_action", "capability is not a resource action")
	}
	if err != nil {
		return plugin.ActionResult{}, err
	}
	normalized, err := s.normalize(resource.Source.PluginID, updated)
	if err != nil {
		return plugin.ActionResult{}, err
	}
	stored := s.store(normalized, "resource.updated", action)
	return plugin.ActionResult{Resource: stored, Link: link}, nil
}

func (s *Service) EventsAfter(sequence uint64) []model.Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if sequence >= uint64(len(s.events)) {
		return []model.Event{}
	}
	return slices.Clone(s.events[sequence:])
}

func (s *Service) register(integration plugin.Integration) error {
	descriptor := integration.Descriptor()
	if descriptor.ID == "" || descriptor.Name == "" || len(descriptor.ResourceTypes) == 0 {
		return model.NewError("invalid_plugin", "plugin descriptor requires an id, name, and resource type")
	}
	if _, exists := s.integrations[descriptor.ID]; exists {
		return model.NewError("duplicate_plugin", "plugin id is already registered")
	}
	kinds := make(map[string]struct{})
	for _, resourceType := range descriptor.ResourceTypes {
		if resourceType.Kind == "" {
			return model.NewError("invalid_plugin", "plugin resource kind cannot be empty")
		}
		if _, exists := kinds[resourceType.Kind]; exists {
			return model.NewError("invalid_plugin", "plugin resource kinds must be unique")
		}
		kinds[resourceType.Kind] = struct{}{}
		seen := make(map[model.Capability]struct{})
		for _, capability := range resourceType.Capabilities {
			if _, exists := seen[capability]; exists {
				return model.NewError("invalid_plugin", "plugin capabilities must be unique per resource kind")
			}
			seen[capability] = struct{}{}
			if !implements(integration, capability) {
				return model.NewError("invalid_plugin", fmt.Sprintf("plugin advertises unsupported capability %q", capability))
			}
		}
	}
	s.integrations[descriptor.ID] = integration
	s.descriptors[descriptor.ID] = cloneDescriptor(descriptor)
	return nil
}

func (s *Service) integration(id string) (plugin.Integration, model.PluginDescriptor, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	integration, ok := s.integrations[id]
	if !ok {
		return nil, model.PluginDescriptor{}, model.NewError("plugin_not_found", "plugin not found")
	}
	return integration, s.descriptors[id], nil
}

func (s *Service) normalize(pluginID string, resource model.Resource) (model.Resource, error) {
	_, descriptor, err := s.integration(pluginID)
	if err != nil {
		return model.Resource{}, err
	}
	if resource.Source.NativeID == "" || resource.Kind == "" || resource.Title == "" {
		return model.Resource{}, model.NewError("invalid_resource", "resource requires native id, kind, and title")
	}
	resourceType, ok := findResourceType(descriptor, resource.Kind)
	if !ok {
		return model.Resource{}, model.NewError("invalid_resource", "plugin returned an undeclared resource kind")
	}
	if resource.Owner.Scope != model.OwnerATC && resource.Owner.Scope != model.OwnerExternal {
		return model.Resource{}, model.NewError("invalid_resource", "resource has an invalid owner scope")
	}
	if resource.Owner.ID == "" {
		return model.Resource{}, model.NewError("invalid_resource", "resource owner id cannot be empty")
	}
	if resource.Control != model.ControlObserved && resource.Control != model.ControlDelegated && resource.Control != model.ControlManaged {
		return model.Resource{}, model.NewError("invalid_resource", "resource has an invalid control mode")
	}
	if err := validateStatus(resource.Status); err != nil {
		return model.Resource{}, err
	}
	seenActions := make(map[model.Capability]struct{})
	for _, action := range resource.Actions {
		if action == model.CapabilityDiscover || action == model.CapabilityCreate {
			return model.Resource{}, model.NewError("invalid_resource", "discover and create are plugin capabilities, not resource actions")
		}
		if !slices.Contains(resourceType.Capabilities, action) {
			return model.Resource{}, model.NewError("invalid_resource", "resource action was not advertised by its plugin")
		}
		if _, exists := seenActions[action]; exists {
			return model.Resource{}, model.NewError("invalid_resource", "resource actions must be unique")
		}
		seenActions[action] = struct{}{}
	}
	if resource.Control == model.ControlObserved {
		for _, action := range resource.Actions {
			if action != model.CapabilityOpenExternal {
				return model.Resource{}, model.NewError("invalid_resource", "observed resources cannot advertise mutating actions")
			}
		}
	}
	for namespace := range resource.Extensions {
		if namespace != pluginID {
			return model.Resource{}, model.NewError("invalid_resource", "plugin extensions must use the plugin id as their namespace")
		}
	}

	resource.ID = canonicalID(pluginID, resource.Source.NativeID)
	resource.Source.PluginID = pluginID
	resource.Actions = slices.Clone(resource.Actions)
	slices.Sort(resource.Actions)
	resource.Links = slices.Clone(resource.Links)
	resource.Extensions = cloneExtensions(resource.Extensions)
	return resource, nil
}

func (s *Service) store(resource model.Resource, eventType string, action model.Capability) model.Resource {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	existing, exists := s.resources[resource.ID]
	if resource.Status.UpdatedAt.IsZero() {
		if exists && sameStatus(resource.Status, existing.Status) {
			resource.Status.UpdatedAt = existing.Status.UpdatedAt
		} else {
			resource.Status.UpdatedAt = now
		}
	}
	resource.UpdatedAt = now
	if exists {
		resource.CreatedAt = existing.CreatedAt
	} else {
		resource.CreatedAt = now
	}
	if exists && action == "" && sameResource(resource, existing) {
		return cloneResource(existing)
	}
	s.resources[resource.ID] = resource
	event := model.Event{
		Sequence: uint64(len(s.events) + 1), Type: eventType,
		ResourceID: resource.ID, PluginID: resource.Source.PluginID,
		Action: action, Status: resource.Status, CreatedAt: now,
	}
	s.events = append(s.events, event)
	return cloneResource(resource)
}

func sameStatus(left, right model.Status) bool {
	left.UpdatedAt = time.Time{}
	right.UpdatedAt = time.Time{}
	return left == right
}

func sameResource(left, right model.Resource) bool {
	left.CreatedAt, left.UpdatedAt, left.Status.UpdatedAt = time.Time{}, time.Time{}, time.Time{}
	right.CreatedAt, right.UpdatedAt, right.Status.UpdatedAt = time.Time{}, time.Time{}, time.Time{}
	return reflect.DeepEqual(left, right)
}

func implements(integration plugin.Integration, capability model.Capability) bool {
	switch capability {
	case model.CapabilityDiscover:
		return true
	case model.CapabilityCreate:
		_, ok := integration.(plugin.Creator)
		return ok
	case model.CapabilityControl:
		_, ok := integration.(plugin.Controller)
		return ok
	case model.CapabilityRespond:
		_, ok := integration.(plugin.Responder)
		return ok
	case model.CapabilityCancel:
		_, ok := integration.(plugin.Canceller)
		return ok
	case model.CapabilityAttach:
		_, ok := integration.(plugin.Attacher)
		return ok
	case model.CapabilityOpenExternal:
		_, ok := integration.(plugin.ExternalOpener)
		return ok
	default:
		return false
	}
}

func descriptorSupports(descriptor model.PluginDescriptor, kind string, capability model.Capability) bool {
	resourceType, ok := findResourceType(descriptor, kind)
	return ok && slices.Contains(resourceType.Capabilities, capability)
}

func findResourceType(descriptor model.PluginDescriptor, kind string) (model.ResourceType, bool) {
	for _, resourceType := range descriptor.ResourceTypes {
		if resourceType.Kind == kind {
			return resourceType, true
		}
	}
	return model.ResourceType{}, false
}

func validateStatus(status model.Status) error {
	validPhase := status.Phase == model.PhaseStarting || status.Phase == model.PhaseActive || status.Phase == model.PhaseEnded || status.Phase == model.PhaseUnavailable || status.Phase == model.PhaseUnknown
	if !validPhase {
		return model.NewError("invalid_resource", "resource has an invalid status phase")
	}
	validActivity := status.Activity == "" || status.Activity == model.ActivityIdle || status.Activity == model.ActivityWorking || status.Activity == model.ActivityNeedsInput || status.Activity == model.ActivityUnknown
	if !validActivity {
		return model.NewError("invalid_resource", "resource has an invalid status activity")
	}
	validOutcome := status.Outcome == "" || status.Outcome == model.OutcomeSucceeded || status.Outcome == model.OutcomeFailed || status.Outcome == model.OutcomeCancelled
	if !validOutcome {
		return model.NewError("invalid_resource", "resource has an invalid status outcome")
	}
	if status.Phase == model.PhaseEnded && status.Outcome == "" {
		return model.NewError("invalid_resource", "ended resources require an outcome")
	}
	if status.Phase != model.PhaseEnded && status.Outcome != "" {
		return model.NewError("invalid_resource", "only ended resources may have an outcome")
	}
	return nil
}

func canonicalID(pluginID, nativeID string) string {
	return pluginID + ":" + base64.RawURLEncoding.EncodeToString([]byte(nativeID))
}

func compareResources(left, right model.Resource) int {
	return strings.Compare(left.ID, right.ID)
}

func cloneDescriptor(descriptor model.PluginDescriptor) model.PluginDescriptor {
	descriptor.ResourceTypes = slices.Clone(descriptor.ResourceTypes)
	for index := range descriptor.ResourceTypes {
		descriptor.ResourceTypes[index].Capabilities = slices.Clone(descriptor.ResourceTypes[index].Capabilities)
	}
	return descriptor
}

func cloneResource(resource model.Resource) model.Resource {
	resource.Actions = slices.Clone(resource.Actions)
	resource.Links = slices.Clone(resource.Links)
	resource.Extensions = cloneExtensions(resource.Extensions)
	return resource
}

func cloneExtensions(extensions map[string]json.RawMessage) map[string]json.RawMessage {
	if extensions == nil {
		return nil
	}
	cloned := make(map[string]json.RawMessage, len(extensions))
	for namespace, value := range extensions {
		cloned[namespace] = slices.Clone(value)
	}
	return cloned
}

func ErrorCode(err error) string {
	var domainError *model.Error
	if errors.As(err, &domainError) {
		return domainError.Code
	}
	return "internal_error"
}
