package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/ids"
)

// The artifact command group (ATC-318): the published side — listing,
// inspecting, renaming, organizing, restoring, deleting, and retrieving
// source — over the API, and the authoring side (artifact_authoring.go):
// working copies, preview, build, and publish.

func newArtifactCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "artifact",
		Short: "Publish and manage documents served by ATC",
		Long: `Artifacts are published documents: each publication stores a complete
static build and its authoring source as an immutable version, served to
browsers from the document origin. The main link always shows the newest
version; every version keeps a permanent link.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var names []string
			for _, sub := range cmd.Commands() {
				names = append(names, sub.Name())
			}
			return fmt.Errorf("usage: atc artifact <%s>", strings.Join(names, "|"))
		},
	}
	cmd.AddCommand(newArtifactNewCmd(), newArtifactOpenCmd(), newArtifactCopiesCmd(), newArtifactDiscardCmd(),
		newArtifactCheckCmd(), newArtifactBuildCmd(), newArtifactPreviewCmd(), newArtifactPublishCmd(),
		newArtifactListCmd(), newArtifactGetCmd(), newArtifactVersionsCmd(), newArtifactUpdateCmd(),
		newArtifactDeleteCmd(), newArtifactRestoreCmd(), newArtifactSourceCmd())
	return cmd
}

func newArtifactListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List artifacts",
		Args:  cobra.NoArgs,
		RunE: runWithClient(func(cmd *cobra.Command, _ []string, client *api.Client, _ string) error {
			project, err := cmd.Flags().GetString("project")
			if err != nil {
				return err
			}
			artifacts, err := client.Artifacts(cmd.Context(), project)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(artifacts) == 0 {
				_, err := fmt.Fprintln(out, "no artifacts")
				return err
			}
			w := tabwriter.NewWriter(out, 2, 8, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "ID\tTITLE\tVERSION\tPROJECT\tURL")
			for _, artifact := range artifacts {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", artifact.ID, artifact.Title, artifact.CurrentVersion, artifact.ProjectID, artifact.URL)
			}
			return w.Flush()
		}),
	}
	cmd.Flags().String("project", "", "only artifacts assigned to this project")
	return cmd
}

func newArtifactGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Show one artifact",
		Args:  cobra.ExactArgs(1),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			artifact, err := client.Artifact(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			printArtifact(cmd.OutOrStdout(), artifact)
			return nil
		}),
	}
}

func newArtifactVersionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "versions <id>",
		Short: "List an artifact's versions, oldest first",
		Args:  cobra.ExactArgs(1),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			versions, err := client.ArtifactVersions(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 8, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "VERSION\tPUBLISHED\tTITLE\tRESTORED\tURL\tURL (TAILNET)")
			for _, version := range versions {
				restored := ""
				if version.RestoredFrom != 0 {
					restored = "from " + strconv.Itoa(version.RestoredFrom)
				}
				_, _ = fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", version.Number, version.PublishedAt.Local().Format("2006-01-02 15:04"), version.Title, restored, version.URL, version.TailnetURL)
			}
			return w.Flush()
		}),
	}
}

func newArtifactUpdateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Rename an artifact or change its project",
		Long: `Change the current title, assign the artifact to a project, or unassign it.
No link changes, and no stored version is touched: a fixed version link keeps
showing the title it was published with.`,
		Args: cobra.ExactArgs(1),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			flags := cmd.Flags()
			var params api.ArtifactUpdateParams
			if flags.Changed("title") {
				title, err := flags.GetString("title")
				if err != nil {
					return err
				}
				params.Title = api.Some(title)
			}
			if flags.Changed("project") {
				project, err := flags.GetString("project")
				if err != nil {
					return err
				}
				params.ProjectID = api.Some(project)
			}
			if noProject, err := flags.GetBool("no-project"); err != nil {
				return err
			} else if noProject {
				params.ProjectID = api.Clear[string]()
			}
			artifact, err := client.UpdateArtifact(cmd.Context(), args[0], params)
			if err != nil {
				return err
			}
			printArtifact(cmd.OutOrStdout(), artifact)
			return nil
		}),
	}
	cmd.Flags().String("title", "", "new current title")
	cmd.Flags().String("project", "", "project to assign the artifact to")
	cmd.Flags().Bool("no-project", false, "unassign the artifact from its project")
	cmd.MarkFlagsOneRequired("title", "project", "no-project")
	cmd.MarkFlagsMutuallyExclusive("project", "no-project")
	return cmd
}

func newArtifactDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete an artifact and its whole history",
		Long: `Delete an artifact: its metadata and every stored version, so its links stop
resolving. Working copies on authoring machines are untouched, and the id
cannot be republished.`,
		Args: cobra.ExactArgs(1),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			if err := client.DeleteArtifact(cmd.Context(), args[0]); err != nil {
				return err
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "deleted %s\n", args[0])
			return err
		}),
	}
}

func newArtifactRestoreCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restore <id> <version>",
		Short: "Republish an earlier version as the newest",
		Long: `Publish an earlier version's stored build and source as a new version, without
rebuilding it. Earlier versions and their links are unchanged. The restoration
is based on the current version; --base names a different one and fails if it
is no longer current, as any publication would.`,
		Args: cobra.ExactArgs(2),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			number, err := versionArg(args[1])
			if err != nil {
				return err
			}
			flags := cmd.Flags()
			params := api.ArtifactPublishParams{RestoreFrom: number, PublicationID: ids.NewLong("pub-")}
			if params.Title, err = flags.GetString("title"); err != nil {
				return err
			}
			if flags.Changed("base") {
				if params.BaseVersion, err = flags.GetInt("base"); err != nil {
					return err
				}
			} else {
				artifact, err := client.Artifact(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				params.BaseVersion = artifact.CurrentVersion
			}
			result, err := client.PublishArtifactVersion(cmd.Context(), args[0], params, nil, nil)
			if err != nil {
				var problem *api.Problem
				if errors.As(err, &problem) && problem.Code == api.CodeArtifactBaseStale {
					return fmt.Errorf("%w\nsomeone published since that base; check atc artifact versions %s and restore again with --base <current>", err, args[0])
				}
				return err
			}
			printPublication(cmd.OutOrStdout(), result)
			return nil
		}),
	}
	cmd.Flags().String("title", "", "title for the restored version (defaults to the version's own)")
	cmd.Flags().Int("base", 0, "version the restoration is based on (defaults to the current one)")
	return cmd
}

func newArtifactSourceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "source <id> [<version>]",
		Short: "Download a version's source snapshot",
		Long: `Download the authoring snapshot a version was published from, as a gzip tar:
the document source, the platform source it used, build configuration,
dependency manifest, and lockfile. The current version unless one is named.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			number := 0
			if len(args) == 2 {
				var err error
				if number, err = versionArg(args[1]); err != nil {
					return err
				}
			} else {
				artifact, err := client.Artifact(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				number = artifact.CurrentVersion
			}
			output, err := cmd.Flags().GetString("output")
			if err != nil {
				return err
			}
			if output == "" {
				output = fmt.Sprintf("%s-v%d-source.tar.gz", args[0], number)
			}
			source, err := client.ArtifactSource(cmd.Context(), args[0], number)
			if err != nil {
				return err
			}
			defer func() { _ = source.Close() }()
			if output == "-" {
				_, err := io.Copy(cmd.OutOrStdout(), source)
				return err
			}
			file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return err
			}
			if _, err := io.Copy(file, source); err != nil {
				// A partial download would block the retry (the file is
				// created exclusively).
				_ = file.Close()
				_ = os.Remove(output)
				return err
			}
			if err := file.Close(); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", output)
			return err
		}),
	}
	cmd.Flags().StringP("output", "o", "", "file to write (default <id>-v<version>-source.tar.gz; - for stdout)")
	return cmd
}

func versionArg(raw string) (int, error) {
	number, err := strconv.Atoi(strings.TrimPrefix(raw, "v"))
	if err != nil || number < 1 {
		return 0, fmt.Errorf("version %q is not a version number", raw)
	}
	return number, nil
}

func printArtifact(out io.Writer, artifact api.Artifact) {
	w := tabwriter.NewWriter(out, 2, 8, 2, ' ', 0)
	_, _ = fmt.Fprintf(w, "id\t%s\n", artifact.ID)
	_, _ = fmt.Fprintf(w, "title\t%s\n", artifact.Title)
	_, _ = fmt.Fprintf(w, "version\t%d\n", artifact.CurrentVersion)
	if artifact.ProjectID != "" {
		_, _ = fmt.Fprintf(w, "project\t%s\n", artifact.ProjectID)
	}
	_, _ = fmt.Fprintf(w, "url\t%s\n", artifact.URL)
	if artifact.TailnetURL != "" {
		_, _ = fmt.Fprintf(w, "url (tailnet)\t%s\n", artifact.TailnetURL)
	}
	_, _ = fmt.Fprintf(w, "created\t%s\n", artifact.CreatedAt.Local().Format("2006-01-02 15:04:05 MST"))
	_, _ = fmt.Fprintf(w, "updated\t%s\n", artifact.UpdatedAt.Local().Format("2006-01-02 15:04:05 MST"))
	_ = w.Flush()
}

// printPublication reports a publication: the version it committed and
// the links to hand back.
func printPublication(out io.Writer, result api.ArtifactPublication) {
	_, _ = fmt.Fprintf(out, "published %s version %d\n", result.Artifact.ID, result.Version.Number)
	w := tabwriter.NewWriter(out, 2, 8, 2, ' ', 0)
	_, _ = fmt.Fprintf(w, "title\t%s\n", result.Version.Title)
	_, _ = fmt.Fprintf(w, "latest\t%s\n", result.Artifact.URL)
	if result.Artifact.TailnetURL != "" {
		_, _ = fmt.Fprintf(w, "latest (tailnet)\t%s\n", result.Artifact.TailnetURL)
	}
	_, _ = fmt.Fprintf(w, "version\t%s\n", result.Version.URL)
	if result.Version.TailnetURL != "" {
		_, _ = fmt.Fprintf(w, "version (tailnet)\t%s\n", result.Version.TailnetURL)
	}
	_ = w.Flush()
}
