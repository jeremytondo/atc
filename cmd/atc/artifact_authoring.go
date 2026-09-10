package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/authoring"
	"github.com/jeremytondo/atc/internal/cli"
	"github.com/jeremytondo/atc/internal/paths"
	"github.com/jeremytondo/atc/internal/version"
)

// The authoring side of `atc artifact` (ATC-318): working copies under
// the authoring directory, edited against the shipped platform with a
// private Node runtime provisioned on first use, previewed and built
// locally, and published through the API. The publishing server may be
// remote; everything here happens on the authoring machine. The commands
// an agent drives (new, open, copies, publish) take --json for a stable
// output; tool output always goes to stderr.

// newAuthoringService is a seam so tests provision a fake runtime.
var newAuthoringService = authoring.New

// newAuthoring builds the authoring service over the authoring directory,
// with tool output on the command's stderr.
func newAuthoring(cmd *cobra.Command) (*authoring.Service, error) {
	root, err := paths.AuthoringDir()
	if err != nil {
		return nil, err
	}
	return newAuthoringService(authoring.Options{Root: root, Version: version.String(), Output: cmd.ErrOrStderr()})
}

func newArtifactNewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "new",
		Short: "Create a working copy to author a document in",
		Long: `Create a working copy: a complete project on the shared platform whose
src/document is yours to edit. It starts from the starter document, from a
worked example (--example), or from a published version's source (--from), in
which case publishing revises that artifact. The first use installs the
private Node runtime and the platform's dependencies; later copies reuse them.

Working copies live under the authoring directory, outside any repository,
and remain until discarded.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			flags := cmd.Flags()
			opts := authoring.CreateOptions{}
			var err error
			if opts.Title, err = flags.GetString("title"); err != nil {
				return err
			}
			if opts.Example, err = flags.GetString("example"); err != nil {
				return err
			}
			from, err := flags.GetString("from")
			if err != nil {
				return err
			}
			service, err := newAuthoring(cmd)
			if err != nil {
				return err
			}
			if from != "" {
				client, _, err := cli.NewClient()
				if err != nil {
					return err
				}
				source, artifact, number, err := retrieveSource(cmd.Context(), client, from)
				if err != nil {
					return err
				}
				defer func() { _ = source.Close() }()
				opts.Source, opts.Artifact, opts.BaseVersion = source, artifact.ID, number
				if opts.Title == "" {
					opts.Title = artifact.Title
				}
			}
			copy, err := service.Create(cmd.Context(), opts)
			if err != nil {
				return err
			}
			return printCopy(cmd, copy)
		},
	}
	cmd.Flags().String("title", "", "document title: the reader's heading (defaults to the example's; changeable at publish)")
	cmd.Flags().String("example", "", "start from a worked example: "+strings.Join(authoring.ExampleNames(), ", "))
	cmd.Flags().String("from", "", "start from a published version's source: <artifact-id>[@<version>]")
	cmd.Flags().Bool("json", false, "print the working copy as JSON")
	cmd.MarkFlagsMutuallyExclusive("example", "from")
	return cmd
}

// retrieveSource downloads the source of an artifact version named as
// <id>[@<version>], defaulting to the current version, and reports the
// artifact and the version the copy is based on.
func retrieveSource(ctx context.Context, client *api.Client, ref string) (io.ReadCloser, api.Artifact, int, error) {
	id, versionRef, _ := strings.Cut(ref, "@")
	artifact, err := client.Artifact(ctx, id)
	if err != nil {
		return nil, api.Artifact{}, 0, err
	}
	number := artifact.CurrentVersion
	if versionRef != "" {
		if number, err = versionArg(versionRef); err != nil {
			return nil, api.Artifact{}, 0, err
		}
	}
	source, err := client.ArtifactSource(ctx, id, number)
	if err != nil {
		return nil, api.Artifact{}, 0, err
	}
	return source, artifact, number, nil
}

func newArtifactOpenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "open <copy>",
		Short: "Reopen a working copy on the current platform",
		Long: `Bring a working copy to the shipped platform (refreshing the installed
platform first if ATC was upgraded) and print where it lives. <copy> is a
working copy id or a path inside one.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := newAuthoring(cmd)
			if err != nil {
				return err
			}
			copy, err := service.Open(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return printCopy(cmd, copy)
		},
	}
	cmd.Flags().Bool("json", false, "print the working copy as JSON")
	return cmd
}

func newArtifactCopiesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "copies",
		Short: "List working copies",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			service, err := newAuthoring(cmd)
			if err != nil {
				return err
			}
			copies, err := service.List()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
				return printJSON(out, copyViews(copies))
			}
			if len(copies) == 0 {
				_, err := fmt.Fprintln(out, "no working copies")
				return err
			}
			w := tabwriter.NewWriter(out, 2, 8, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "ID\tTITLE\tARTIFACT\tBASE\tPENDING\tDIRECTORY")
			for _, copy := range copies {
				base := ""
				if copy.BaseVersion != 0 {
					base = strconv.Itoa(copy.BaseVersion)
				}
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", copy.ID, copy.Title, copy.Artifact, base, copy.Pending, copy.Dir)
			}
			return w.Flush()
		},
	}
	cmd.Flags().Bool("json", false, "print the working copies as JSON")
	return cmd
}

func newArtifactDiscardCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "discard <copy>",
		Short: "Remove a working copy",
		Long:  `Remove a working copy. Published versions are unaffected.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := newAuthoring(cmd)
			if err != nil {
				return err
			}
			copy, err := service.Get(args[0])
			if err != nil {
				return err
			}
			if err := service.Discard(copy.ID); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "discarded %s\n", copy.ID)
			return err
		},
	}
}

func newArtifactCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check <copy>",
		Short: "Type-check a working copy against the current platform",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := newAuthoring(cmd)
			if err != nil {
				return err
			}
			if err := service.Check(cmd.Context(), args[0]); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "ok")
			return err
		},
	}
}

func newArtifactBuildCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "build <copy>",
		Short: "Type-check and build a working copy",
		Long: `Type-check and build a snapshot of the working copy, producing the static
site and the two archives a publication uploads. Tool output goes to stderr.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := newAuthoring(cmd)
			if err != nil {
				return err
			}
			built, err := service.Build(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "built %s\n", built.Dir)
			return err
		},
	}
}

func newArtifactPreviewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "preview <copy>",
		Short: "Serve a working copy locally with live reload",
		Long: `Run the platform's development server on the working copy until interrupted.
The URL is printed; edits under src/document reload in the browser. Preview is
local to this machine; publish to share.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			port, err := cmd.Flags().GetInt("port")
			if err != nil {
				return err
			}
			service, err := newAuthoring(cmd)
			if err != nil {
				return err
			}
			err = service.Preview(cmd.Context(), args[0], port)
			if cmd.Context().Err() != nil {
				return nil
			}
			return err
		},
	}
	cmd.Flags().Int("port", 0, "port to serve on (default: the platform's choice)")
	return cmd
}

func newArtifactPublishCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "publish <copy>",
		Short: "Build a working copy and publish it",
		Long: `Build the working copy and publish the result: a new artifact the first
time, a new version of its artifact afterwards. Publishing on a base that is no
longer current fails with a conflict naming the current version; reconcile
(see the current version's source) and publish again with --base.

Provenance is recorded with the version: --thread names the ATC thread the
document was authored in, --revision the repository revision it describes
(defaults to the current directory's git HEAD when there is one), --link a
related issue or reference (repeatable).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cmd.Flags()
			var opts authoring.PublishOptions
			var err error
			if opts.Title, err = flags.GetString("title"); err != nil {
				return err
			}
			if opts.Base, err = flags.GetInt("base"); err != nil {
				return err
			}
			if opts.Provenance.ThreadID, err = flags.GetString("thread"); err != nil {
				return err
			}
			if opts.Provenance.Revision, err = flags.GetString("revision"); err != nil {
				return err
			}
			if opts.Provenance.Links, err = flags.GetStringArray("link"); err != nil {
				return err
			}
			if !flags.Changed("revision") {
				opts.Provenance.Revision = gitRevision(cmd.Context())
			}
			service, err := newAuthoring(cmd)
			if err != nil {
				return err
			}
			client, _, err := cli.NewClient()
			if err != nil {
				return err
			}
			result, err := service.Publish(cmd.Context(), client, args[0], opts)
			if err != nil {
				var problem *api.Problem
				if errors.As(err, &problem) && problem.Code == api.CodeArtifactBaseStale {
					return fmt.Errorf("%w\nsomeone published since this copy's base; review the current version (atc artifact versions <id>, atc artifact source <id>) and publish again with --base <current>", err)
				}
				return err
			}
			if asJSON, _ := flags.GetBool("json"); asJSON {
				return printJSON(cmd.OutOrStdout(), result)
			}
			if result.Resumed {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "resent a pending publication; this invocation's flags and edits made since apply to the next publish")
			}
			printPublication(cmd.OutOrStdout(), result.ArtifactPublication)
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "print the publication as JSON")
	cmd.Flags().String("title", "", "title to publish under (updates the working copy's)")
	cmd.Flags().Int("base", 0, "version this publication is based on (default: the copy's recorded base)")
	cmd.Flags().String("thread", "", "ATC thread the document was authored in")
	cmd.Flags().String("revision", "", "repository revision the document describes (default: git HEAD of the current directory)")
	cmd.Flags().StringArray("link", nil, "related link (repeatable)")
	return cmd
}

// gitRevision is the current directory's git HEAD, or "" outside a
// repository or without git.
func gitRevision(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "git", "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// copyView is a working copy as the --json output shows it.
type copyView struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Directory   string `json:"directory"`
	Document    string `json:"document"`
	Artifact    string `json:"artifact,omitempty"`
	BaseVersion int    `json:"baseVersion,omitempty"`
	Example     string `json:"example,omitempty"`
	Pending     string `json:"pendingPublication,omitempty"`
}

func copyViews(copies []authoring.Copy) []copyView {
	views := make([]copyView, 0, len(copies))
	for _, copy := range copies {
		views = append(views, copyView{
			ID: copy.ID, Title: copy.Title, Directory: copy.Dir, Document: copy.Document,
			Artifact: copy.Artifact, BaseVersion: copy.BaseVersion, Example: copy.Example, Pending: copy.Pending,
		})
	}
	return views
}

func printJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func printCopy(cmd *cobra.Command, copy authoring.Copy) error {
	out := cmd.OutOrStdout()
	if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
		return printJSON(out, copyViews([]authoring.Copy{copy})[0])
	}
	w := tabwriter.NewWriter(out, 2, 8, 2, ' ', 0)
	_, _ = fmt.Fprintf(w, "copy\t%s\n", copy.ID)
	_, _ = fmt.Fprintf(w, "title\t%s\n", copy.Title)
	_, _ = fmt.Fprintf(w, "directory\t%s\n", copy.Dir)
	_, _ = fmt.Fprintf(w, "document\t%s\n", copy.Document)
	if copy.Artifact != "" {
		_, _ = fmt.Fprintf(w, "artifact\t%s (based on version %d)\n", copy.Artifact, copy.BaseVersion)
	}
	if copy.Example != "" {
		_, _ = fmt.Fprintf(w, "example\t%s\n", copy.Example)
	}
	if copy.Pending != "" {
		_, _ = fmt.Fprintf(w, "pending\t%s (publish resends it)\n", copy.Pending)
	}
	return w.Flush()
}
