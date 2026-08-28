package main

import (
	"fmt"
	"io"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/cli"
)

func newProjectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Group work into projects rooted at directories",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("usage: atc project <create|get|list|update|delete>")
		},
	}
	cmd.AddCommand(newProjectCreateCmd(), newProjectGetCmd(), newProjectListCmd(),
		newProjectUpdateCmd(), newProjectDeleteCmd())
	return cmd
}

func newProjectCreateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create [path]",
		Short: "Create a project rooted at a directory",
		Long: `Create a project rooted at path. Without a path, the project is rooted at
the git toplevel when inside a repository, at the current directory
otherwise. The server canonicalizes the directory (symlinks resolved) and
it is the project's identity: one project per real folder, immutable after
create. A relative path is resolved against this shell's directory; the
directory itself must exist on the server's machine.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			name, err := cmd.Flags().GetString("name")
			if err != nil {
				return err
			}
			var directory string
			if len(args) == 1 {
				// Absolutized client-side so a relative path means this
				// process's working directory, never the server's — but
				// only absolutized: existence and canonicalization are the
				// server's, whose filesystem the directory lives on.
				if directory, err = filepath.Abs(args[0]); err != nil {
					return err
				}
			} else if directory, err = cli.DefaultProjectDir(); err != nil {
				return err
			}
			project, err := client.CreateProject(cmd.Context(), api.ProjectCreateParams{
				Directory: directory, Name: name,
			})
			if err != nil {
				return err
			}
			printProject(cmd.OutOrStdout(), project)
			return nil
		}),
	}
	cmd.Flags().String("name", "", "display name (defaults to the directory basename)")
	return cmd
}

func newProjectGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Show one project",
		Args:  cobra.ExactArgs(1),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			project, err := client.Project(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			printProject(cmd.OutOrStdout(), project)
			return nil
		}),
	}
}

func newProjectListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List projects",
		Args:  cobra.NoArgs,
		RunE: runWithClient(func(cmd *cobra.Command, _ []string, client *api.Client, _ string) error {
			projects, err := client.Projects(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(projects) == 0 {
				_, err := fmt.Fprintln(out, "no projects")
				return err
			}
			w := tabwriter.NewWriter(out, 2, 8, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "ID\tNAME\tDIRECTORY")
			for _, project := range projects {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", project.ID, project.Name, project.Directory)
			}
			return w.Flush()
		}),
	}
}

func newProjectUpdateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update a project (name is the only mutable field)",
		Args:  cobra.ExactArgs(1),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			name, err := cmd.Flags().GetString("name")
			if err != nil {
				return err
			}
			project, err := client.UpdateProject(cmd.Context(), args[0], api.ProjectUpdateParams{Name: name})
			if err != nil {
				return err
			}
			printProject(cmd.OutOrStdout(), project)
			return nil
		}),
	}
	cmd.Flags().String("name", "", "new display name")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newProjectDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete an empty project",
		Long: `Delete a project. Refused while any terminal still belongs to it — delete
those first; there is no cascade.`,
		Args: cobra.ExactArgs(1),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			if err := client.DeleteProject(cmd.Context(), args[0]); err != nil {
				return err
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "deleted %s\n", args[0])
			return err
		}),
	}
}

func printProject(out io.Writer, project api.Project) {
	w := tabwriter.NewWriter(out, 2, 8, 2, ' ', 0)
	_, _ = fmt.Fprintf(w, "id\t%s\n", project.ID)
	_, _ = fmt.Fprintf(w, "name\t%s\n", project.Name)
	_, _ = fmt.Fprintf(w, "directory\t%s\n", project.Directory)
	_, _ = fmt.Fprintf(w, "created\t%s\n", project.CreatedAt.Format("2006-01-02 15:04:05 MST"))
	_ = w.Flush()
}
