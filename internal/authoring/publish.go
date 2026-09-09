package authoring

// Publication. A publish builds the copy, then freezes exactly what it
// will send — the two archives and the parameters — as the copy's
// pending package under .atc/pending, and uploads that. A repeat after
// an uncertain outcome resends the same package byte for byte, so the
// server's replay of the retry identity describes what was actually
// uploaded; edits made in the meantime become the next publication once
// the pending one is resolved.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/ids"
)

const pendingDir = "pending"

// Publisher is the API the publication rides: api.Client in production.
type Publisher interface {
	PublishArtifact(ctx context.Context, params api.ArtifactPublishParams, build, source io.Reader) (api.ArtifactPublication, error)
	PublishArtifactVersion(ctx context.Context, id string, params api.ArtifactPublishParams, build, source io.Reader) (api.ArtifactPublication, error)
}

// ErrInvalidPublication marks parameters the server would refuse; they
// are checked before anything is built.
var ErrInvalidPublication = errors.New("invalid publication")

// validate applies the API's own bounds up front, so a title too long or
// one link too many fails before a build, not after one.
func (o PublishOptions) validate() error {
	switch {
	case len(o.Title) > 200:
		return fmt.Errorf("%w: the title exceeds 200 characters", ErrInvalidPublication)
	case len(o.Provenance.ThreadID) > 100:
		return fmt.Errorf("%w: --thread exceeds 100 characters", ErrInvalidPublication)
	case len(o.Provenance.Revision) > 200:
		return fmt.Errorf("%w: --revision exceeds 200 characters", ErrInvalidPublication)
	case len(o.Provenance.Links) > 20:
		return fmt.Errorf("%w: more than 20 links", ErrInvalidPublication)
	}
	return nil
}

// PublishOptions shape one publication.
type PublishOptions struct {
	// Title replaces the copy's title when set.
	Title string
	// Base overrides the copy's recorded base version — after a conflict,
	// once the author has reconciled with the current version.
	Base       int
	Provenance api.ArtifactProvenance
}

// Publication is a publish's outcome: the server's result and whether
// it resolved a pending publication from an earlier attempt rather than
// publishing the copy as it is now.
type Publication struct {
	api.ArtifactPublication
	Resumed bool `json:"resumed"`
}

// pending is the frozen package: the parameters, beside build.tar.gz and
// source.tar.gz, and the artifact the upload targets ("" for a new one).
type pending struct {
	Target string                    `json:"target"`
	Params api.ArtifactPublishParams `json:"params"`
}

// Publish builds the copy and uploads the result: a new artifact for a
// copy without a target, a new version of its artifact otherwise. A
// pending publication from an earlier attempt is resent first, as it
// was, and reported with Resumed set; publish again for later edits. A
// confirmed result makes the committed version the copy's new base.
func (s *Service) Publish(ctx context.Context, publisher Publisher, ref string, opts PublishOptions) (Publication, error) {
	if err := opts.validate(); err != nil {
		return Publication{}, err
	}
	copy, inst, release, err := s.prepare(ctx, ref)
	if err != nil {
		return Publication{}, err
	}
	defer release()
	resumed := copy.Pending != ""
	if !resumed {
		if opts.Title != "" && opts.Title != copy.Title {
			copy.Title = opts.Title
			if err := writeRecord(copy); err != nil {
				return Publication{}, err
			}
		}
		built, err := s.buildPrepared(ctx, copy, inst, true)
		if err != nil {
			return Publication{}, err
		}
		params := api.ArtifactPublishParams{
			Title: copy.Title, PublicationID: ids.NewLong("pub-"), Platform: s.PlatformID(),
			BaseVersion: copy.BaseVersion, Provenance: opts.Provenance,
		}
		if opts.Base > 0 {
			params.BaseVersion = opts.Base
		}
		if err := freeze(copy.Dir, pending{Target: copy.Artifact, Params: params}, built); err != nil {
			return Publication{}, err
		}
	} else {
		_, _ = fmt.Fprintf(s.output, "resending the pending publication %s as it was; this invocation's flags and any edits since apply to the next publish\n", copy.Pending)
	}
	pkg, err := readPending(copy.Dir)
	if err != nil {
		return Publication{}, err
	}
	dir := filepath.Join(copy.Dir, scratchDir, pendingDir)
	build, err := os.Open(filepath.Join(dir, "build.tar.gz"))
	if err != nil {
		return Publication{}, err
	}
	defer func() { _ = build.Close() }()
	source, err := os.Open(filepath.Join(dir, "source.tar.gz"))
	if err != nil {
		return Publication{}, err
	}
	defer func() { _ = source.Close() }()
	var result api.ArtifactPublication
	if pkg.Target == "" {
		result, err = publisher.PublishArtifact(ctx, pkg.Params, build, source)
	} else {
		result, err = publisher.PublishArtifactVersion(ctx, pkg.Target, pkg.Params, build, source)
	}
	if err != nil {
		var problem *api.Problem
		if errors.As(err, &problem) && refused(problem.Status) {
			// A definite refusal: the package has no result to return, so
			// a corrected publication is a new one.
			_ = os.RemoveAll(dir)
		}
		return Publication{}, err
	}
	copy.Artifact, copy.BaseVersion = result.Artifact.ID, result.Version.Number
	if err := errors.Join(writeRecord(copy), os.RemoveAll(dir)); err != nil {
		return Publication{ArtifactPublication: result, Resumed: resumed}, fmt.Errorf("published, but the working copy's record was not updated: %w", err)
	}
	return Publication{ArtifactPublication: result, Resumed: resumed}, nil
}

// refused reports the statuses that mean the server judged the package
// and will judge it the same way again; anything else (auth, rate
// limits, timeouts) leaves the package pending for a retry.
func refused(status int) bool {
	switch status {
	case http.StatusNotFound, http.StatusConflict, http.StatusGone, http.StatusUnprocessableEntity:
		return true
	}
	return false
}

// freeze moves a build's archives and the parameters into the pending
// package, replacing any earlier one.
func freeze(copyDir string, pkg pending, built Build) error {
	dir := filepath.Join(copyDir, scratchDir, pendingDir)
	staging := dir + ".next"
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(pkg, "", "  ")
	if err != nil {
		return err
	}
	// In order, stopping at the first failure: the previous package is
	// removed only once the new one is complete.
	for _, step := range []func() error{
		func() error { return os.WriteFile(filepath.Join(staging, "params.json"), encoded, 0o600) },
		func() error { return os.Rename(built.BuildArchive, filepath.Join(staging, "build.tar.gz")) },
		func() error { return os.Rename(built.SourceArchive, filepath.Join(staging, "source.tar.gz")) },
		func() error { return os.RemoveAll(dir) },
	} {
		if err := step(); err != nil {
			return errors.Join(err, os.RemoveAll(staging))
		}
	}
	return os.Rename(staging, dir)
}

func readPending(copyDir string) (pending, error) {
	data, err := os.ReadFile(filepath.Join(copyDir, scratchDir, pendingDir, "params.json"))
	if err != nil {
		return pending{}, err
	}
	var pkg pending
	if err := json.Unmarshal(data, &pkg); err != nil {
		return pending{}, err
	}
	return pkg, nil
}
