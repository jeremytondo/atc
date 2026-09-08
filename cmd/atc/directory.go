package main

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/jeremytondo/atc/internal/api"
)

// The atc directory family (ATC-316): the read-only browser over the
// server's filesystem that the picker's Create Space flow uses. List is
// the only verb — there is no directory resource to create or change.

func newDirectoryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "directory",
		Short: "Browse directories on the server's machine",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("usage: atc directory <list>")
		},
	}
	cmd.AddCommand(newDirectoryListCmd())
	return cmd
}

func newDirectoryListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list [path]",
		Short: "List a directory's subdirectories on the server's machine",
		Long: `List the immediate subdirectories of an absolute path on the server's
machine — the server user's home directory when omitted. Directories only,
hidden names excluded, symlinks included when they resolve to a directory.
The listing stops at the server's cap and says so.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			var path string
			if len(args) == 1 {
				path = args[0]
			}
			list, err := client.Directories(cmd.Context(), path)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 8, 2, ' ', 0)
			_, _ = fmt.Fprintf(w, "path\t%s\n", printable(list.Path))
			if list.Parent != nil {
				_, _ = fmt.Fprintf(w, "parent\t%s\n", printable(*list.Parent))
			}
			_, _ = fmt.Fprintln(w, "NAME\tPATH")
			for _, entry := range list.Entries {
				_, _ = fmt.Fprintf(w, "%s\t%s\n", printable(entry.Name), printable(entry.Path))
			}
			if list.Truncated {
				_, _ = fmt.Fprintln(w, "(listing truncated at the server's cap)")
			}
			return w.Flush()
		}),
	}
}

// printable keeps a filesystem-chosen name from carrying control bytes
// into the caller's terminal or breaking the table.
func printable(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '\uFFFD'
		}
		return r
	}, value)
}
