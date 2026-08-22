// Package terminal is the complete zmx-specific boundary for the experiment.
// Callers deal in session names, command argv, bytes, and reachability; zmx
// invocation, inventory parsing, PTY creation, and attach auto-create hazards
// stay here.
package terminal

import (
	"context"
	"io"
	"os"
)

type Session struct {
	Name      string `json:"name"`
	Reachable bool   `json:"reachable"`
	PID       int    `json:"pid,omitempty"`
}

type CreateOptions struct {
	Name    string
	CWD     string
	Command []string
	Env     map[string]string
}

type Terminal interface {
	Create(context.Context, CreateOptions) error
	List(context.Context) ([]Session, error)
	Send(context.Context, string, []byte) error
	History(context.Context, string) ([]byte, error)
	Attach(context.Context, string, *os.File, *os.File, io.Writer) error
	Kill(context.Context, string) error
}
