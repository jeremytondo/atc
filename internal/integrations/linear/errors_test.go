package linear

import (
	"errors"
	"fmt"

	"github.com/jeremytondo/atc/internal/integrations"
	"github.com/jeremytondo/atc/internal/projects"
	"github.com/jeremytondo/atc/internal/threads"
)

var (
	errStorage        = errors.New("database is locked")
	errProjectUnknown = projects.ErrNotFound
	errNotConnected   = fmt.Errorf("%w: T3 Code is unavailable: not running", integrations.ErrNotConnected)
)

var errNotFound = threads.ErrNotFound
