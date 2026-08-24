// Package model defines the deliberately small, integration-neutral surface
// exposed by the prototype. Ownership, control, and current actions stay
// separate because none can be inferred safely from either of the others.
package model

import (
	"encoding/json"
	"time"
)

type Capability string

const (
	CapabilityDiscover     Capability = "discover"
	CapabilityCreate       Capability = "create"
	CapabilityControl      Capability = "control"
	CapabilityRespond      Capability = "respond"
	CapabilityCancel       Capability = "cancel"
	CapabilityAttach       Capability = "attach"
	CapabilityOpenExternal Capability = "open_external"
)

type ResourceType struct {
	Kind         string       `json:"kind"`
	Capabilities []Capability `json:"capabilities"`
}

type PluginDescriptor struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	ResourceTypes []ResourceType `json:"resourceTypes"`
}

type Source struct {
	PluginID string `json:"pluginId"`
	NativeID string `json:"nativeId"`
}

type OwnerScope string

const (
	OwnerATC      OwnerScope = "atc"
	OwnerExternal OwnerScope = "external"
)

type Owner struct {
	Scope OwnerScope `json:"scope"`
	ID    string     `json:"id"`
	Label string     `json:"label,omitempty"`
}

type ControlMode string

const (
	ControlObserved  ControlMode = "observed"
	ControlDelegated ControlMode = "delegated"
	ControlManaged   ControlMode = "managed"
)

type Phase string

const (
	PhaseStarting    Phase = "starting"
	PhaseActive      Phase = "active"
	PhaseEnded       Phase = "ended"
	PhaseUnavailable Phase = "unavailable"
	PhaseUnknown     Phase = "unknown"
)

type Activity string

const (
	ActivityIdle       Activity = "idle"
	ActivityWorking    Activity = "working"
	ActivityNeedsInput Activity = "needs_input"
	ActivityUnknown    Activity = "unknown"
)

type Outcome string

const (
	OutcomeSucceeded Outcome = "succeeded"
	OutcomeFailed    Outcome = "failed"
	OutcomeCancelled Outcome = "cancelled"
)

type Status struct {
	Phase     Phase     `json:"phase"`
	Activity  Activity  `json:"activity,omitempty"`
	Outcome   Outcome   `json:"outcome,omitempty"`
	Detail    string    `json:"detail,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Link struct {
	Rel   string `json:"rel"`
	Label string `json:"label"`
	URL   string `json:"url"`
}

type Resource struct {
	ID         string                     `json:"id"`
	Kind       string                     `json:"kind"`
	Title      string                     `json:"title"`
	Source     Source                     `json:"source"`
	Owner      Owner                      `json:"owner"`
	Control    ControlMode                `json:"control"`
	Actions    []Capability               `json:"actions"`
	Status     Status                     `json:"status"`
	Links      []Link                     `json:"links,omitempty"`
	Extensions map[string]json.RawMessage `json:"extensions,omitempty"`
	CreatedAt  time.Time                  `json:"createdAt"`
	UpdatedAt  time.Time                  `json:"updatedAt"`
}

type Event struct {
	Sequence   uint64     `json:"sequence"`
	Type       string     `json:"type"`
	ResourceID string     `json:"resourceId"`
	PluginID   string     `json:"pluginId"`
	Action     Capability `json:"action,omitempty"`
	Status     Status     `json:"status"`
	CreatedAt  time.Time  `json:"createdAt"`
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

func NewError(code, message string) *Error {
	return &Error{Code: code, Message: message}
}
