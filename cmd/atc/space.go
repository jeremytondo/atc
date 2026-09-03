package main

import (
	"fmt"
	"io"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/jeremytondo/atc/internal/api"
)

// The atc space family (ATC-296): the containers terminals belong to.
// Flat, like gh and docker nouns — never nested under terminal.

func newSpaceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "space",
		Short: "Group live terminals into spaces",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("usage: atc space <create|get|list|update|delete>")
		},
	}
	cmd.AddCommand(newSpaceCreateCmd(), newSpaceGetCmd(), newSpaceListCmd(), newSpaceUpdateCmd(), newSpaceDeleteCmd())
	return cmd
}

func newSpaceCreateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create [path]",
		Short: "Create a space rooted at a directory",
		Long: `Create a space whose terminals start in path — the server user's home
directory when omitted. The server canonicalizes the directory (symlinks
resolved); it must exist on the server's machine. Spaces may share a
directory. A relative path is resolved against this shell's directory.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			name, err := cmd.Flags().GetString("name")
			if err != nil {
				return err
			}
			var directory string
			if len(args) == 1 {
				if directory, err = filepath.Abs(args[0]); err != nil {
					return err
				}
			}
			space, err := client.CreateSpace(cmd.Context(), api.SpaceCreateParams{Directory: directory, Name: name})
			if err != nil {
				return err
			}
			printSpace(cmd.OutOrStdout(), space)
			return nil
		}),
	}
	cmd.Flags().String("name", "", "display name (defaults to the directory basename)")
	return cmd
}

func newSpaceGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Show one space",
		Args:  cobra.ExactArgs(1),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			space, err := client.Space(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			printSpace(cmd.OutOrStdout(), space)
			return nil
		}),
	}
}

func newSpaceListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List spaces",
		Args:  cobra.NoArgs,
		RunE: runWithClient(func(cmd *cobra.Command, _ []string, client *api.Client, _ string) error {
			spaces, err := client.Spaces(cmd.Context())
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 8, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "ID\tNAME\tDIRECTORY")
			for _, space := range spaces {
				name := space.Name
				if space.IsDefault {
					name += " (default)"
				}
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", space.ID, name, space.Directory)
			}
			return w.Flush()
		}),
	}
}

func newSpaceUpdateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Rename a space or change its directory",
		Long: `Rename a space or point it at another directory. A directory change
applies to terminals created afterwards; existing terminals keep theirs.
The Default space cannot be changed.`,
		Args: cobra.ExactArgs(1),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			flags := cmd.Flags()
			var params api.SpaceUpdateParams
			if flags.Changed("name") {
				name, err := flags.GetString("name")
				if err != nil {
					return err
				}
				params.Name = api.Some(name)
			}
			if flags.Changed("directory") {
				directory, err := flags.GetString("directory")
				if err != nil {
					return err
				}
				if directory, err = filepath.Abs(directory); err != nil {
					return err
				}
				params.Directory = api.Some(directory)
			}
			space, err := client.UpdateSpace(cmd.Context(), args[0], params)
			if err != nil {
				return err
			}
			printSpace(cmd.OutOrStdout(), space)
			return nil
		}),
	}
	cmd.Flags().String("name", "", "new display name")
	cmd.Flags().String("directory", "", "new directory for later terminals")
	cmd.MarkFlagsOneRequired("name", "directory")
	return cmd
}

func newSpaceDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a space and every terminal in it",
		Long: `Delete a space. Every terminal in it is deleted the normal way — session
stopped best-effort, record removed; its threads survive. There is no
confirmation: be sure. The Default space cannot be deleted.`,
		Args: cobra.ExactArgs(1),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			if err := client.DeleteSpace(cmd.Context(), args[0]); err != nil {
				return err
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "deleted %s\n", args[0])
			return err
		}),
	}
}

func printSpace(out io.Writer, space api.Space) {
	w := tabwriter.NewWriter(out, 2, 8, 2, ' ', 0)
	_, _ = fmt.Fprintf(w, "id\t%s\n", space.ID)
	_, _ = fmt.Fprintf(w, "name\t%s\n", space.Name)
	_, _ = fmt.Fprintf(w, "directory\t%s\n", space.Directory)
	if space.IsDefault {
		_, _ = fmt.Fprintf(w, "default\tyes\n")
	}
	_, _ = fmt.Fprintf(w, "created\t%s\n", space.CreatedAt.Format("2006-01-02 15:04:05 MST"))
	_ = w.Flush()
}
