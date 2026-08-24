// Package plugin defines capability-specific seams. The core never receives a
// generic plugin command or plugin-specific payload; an integration implements
// only the operations it advertises for its resource kinds.
package plugin

import (
	"context"

	"github.com/elevenideas/atc/experiments/plugin-orchestration/internal/model"
)

type Integration interface {
	Descriptor() model.PluginDescriptor
	Discover(context.Context) ([]model.Resource, error)
}

type Creator interface {
	Create(context.Context, CreateRequest) (model.Resource, error)
}

type Controller interface {
	Control(context.Context, string, string) (model.Resource, error)
}

type Responder interface {
	Respond(context.Context, string, string) (model.Resource, error)
}

type Canceller interface {
	Cancel(context.Context, string) (model.Resource, error)
}

type Attacher interface {
	Attach(context.Context, string) (model.Resource, model.Link, error)
}

type ExternalOpener interface {
	OpenExternal(context.Context, string) (model.Resource, model.Link, error)
}

type CreateRequest struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
}

type ActionRequest struct {
	Text    string `json:"text,omitempty"`
	Command string `json:"command,omitempty"`
}

type ActionResult struct {
	Resource model.Resource `json:"resource"`
	Link     *model.Link    `json:"link,omitempty"`
}
