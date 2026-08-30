package main

import (
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/jeremytondo/atc/internal/api"
)

// The atc thread family (ATC-255): reads and the two mutations over
// observed agent conversations. There is deliberately no create or open —
// threads are observed into existence inside agent TUIs, and resume
// happens inside the TUI via /resume. archive/unarchive are thin sugar
// over PATCH.

func newThreadCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "thread",
		Short: "See and manage observed agent conversations",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("usage: atc thread <list|get|update|archive|unarchive|delete>")
		},
	}
	cmd.AddCommand(newThreadListCmd(), newThreadGetCmd(), newThreadUpdateCmd(),
		newThreadArchiveCmd(), newThreadUnarchiveCmd(), newThreadDeleteCmd())
	return cmd
}

func newThreadListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List threads, most recent activity first",
		Long: `List every observed agent conversation (pass --project to scope to one
project). Archived threads are hidden unless --archived.`,
		Args: cobra.NoArgs,
		RunE: runWithClient(func(cmd *cobra.Command, _ []string, client *api.Client, _ string) error {
			flags := cmd.Flags()
			archived, err := flags.GetBool("archived")
			if err != nil {
				return err
			}
			project, err := flags.GetString("project")
			if err != nil {
				return err
			}
			threads, err := client.Threads(cmd.Context(), project, "", archived)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(threads) == 0 {
				_, err := fmt.Fprintln(out, "no threads")
				return err
			}
			// Most recent activity first: evidence time when there is any,
			// else the record's last update.
			sort.SliceStable(threads, func(i, j int) bool {
				return activityTime(threads[i]).After(activityTime(threads[j]))
			})
			w := tabwriter.NewWriter(out, 2, 8, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "ID\tSTATUS\tAGENT\tTERMINAL\tTITLE")
			for _, thread := range threads {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
					thread.ID, threadStatusLabel(thread), thread.Agent, thread.TerminalID, thread.Title)
			}
			return w.Flush()
		}),
	}
	cmd.Flags().String("project", "", "only threads belonging to this project")
	cmd.Flags().Bool("archived", false, "include archived threads")
	return cmd
}

func newThreadGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Show one thread",
		Args:  cobra.ExactArgs(1),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			thread, err := client.Thread(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			printThread(cmd.OutOrStdout(), thread)
			return nil
		}),
	}
}

func newThreadUpdateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update a thread (title is the editable field)",
		Long: `Set a thread's title. A title set here is yours: observation never
overwrites it afterwards.`,
		Args: cobra.ExactArgs(1),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			title, err := cmd.Flags().GetString("title")
			if err != nil {
				return err
			}
			thread, err := client.UpdateThread(cmd.Context(), args[0], api.ThreadUpdateParams{Title: &title})
			if err != nil {
				return err
			}
			printThread(cmd.OutOrStdout(), thread)
			return nil
		}),
	}
	cmd.Flags().String("title", "", "new display title")
	_ = cmd.MarkFlagRequired("title")
	return cmd
}

func newThreadArchiveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "archive <id>",
		Short: "Archive a thread (reversible; hides it from lists)",
		Long: `Archive a thread — sugar over PATCH of archived. Archived threads are
hidden from lists unless requested and restored with unarchive. A thread
a terminal currently has open is refused.`,
		Args: cobra.ExactArgs(1),
		RunE: setThreadArchived(true),
	}
}

func newThreadUnarchiveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unarchive <id>",
		Short: "Restore an archived thread",
		Args:  cobra.ExactArgs(1),
		RunE:  setThreadArchived(false),
	}
}

func setThreadArchived(archived bool) func(*cobra.Command, []string) error {
	return runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
		thread, err := client.UpdateThread(cmd.Context(), args[0], api.ThreadUpdateParams{Archived: &archived})
		if err != nil {
			return err
		}
		printThread(cmd.OutOrStdout(), thread)
		return nil
	})
}

func newThreadDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete ATC's record of a conversation",
		Long: `Delete the thread record and its identity mapping. The provider-side
conversation is never touched — it stays resumable inside the agent's own
tooling, and resuming it later creates a fresh record. A thread a terminal
currently has open is refused.`,
		Args: cobra.ExactArgs(1),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			if err := client.DeleteThread(cmd.Context(), args[0]); err != nil {
				return err
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "deleted %s\n", args[0])
			return err
		}),
	}
}

func threadStatusLabel(thread api.Thread) string {
	if thread.Archived {
		return string(thread.Status) + " (archived)"
	}
	return string(thread.Status)
}

func activityTime(thread api.Thread) time.Time {
	if thread.LastEvidenceAt != nil {
		return *thread.LastEvidenceAt
	}
	return thread.UpdatedAt
}

func printThread(out io.Writer, thread api.Thread) {
	w := tabwriter.NewWriter(out, 2, 8, 2, ' ', 0)
	_, _ = fmt.Fprintf(w, "id\t%s\n", thread.ID)
	_, _ = fmt.Fprintf(w, "agent\t%s\n", thread.Agent)
	_, _ = fmt.Fprintf(w, "status\t%s\n", threadStatusLabel(thread))
	if thread.Title != "" {
		_, _ = fmt.Fprintf(w, "title\t%s\n", thread.Title)
	}
	_, _ = fmt.Fprintf(w, "project\t%s\n", thread.ProjectID)
	if thread.TerminalID != "" {
		_, _ = fmt.Fprintf(w, "terminal\t%s\n", thread.TerminalID)
	}
	if thread.Model != "" {
		_, _ = fmt.Fprintf(w, "model\t%s\n", thread.Model)
	}
	if thread.Effort != "" {
		_, _ = fmt.Fprintf(w, "effort\t%s\n", thread.Effort)
	}
	if thread.Cwd != "" {
		_, _ = fmt.Fprintf(w, "cwd\t%s\n", thread.Cwd)
	}
	if thread.PermissionMode != "" {
		_, _ = fmt.Fprintf(w, "permission mode\t%s\n", thread.PermissionMode)
	}
	if thread.LastError != "" {
		_, _ = fmt.Fprintf(w, "last error\t%s\n", thread.LastError)
	}
	if thread.LastEvidenceAt != nil {
		_, _ = fmt.Fprintf(w, "last evidence\t%s\n", thread.LastEvidenceAt.Format("2006-01-02 15:04:05 MST"))
	}
	_, _ = fmt.Fprintf(w, "created\t%s\n", thread.CreatedAt.Format("2006-01-02 15:04:05 MST"))
	_ = w.Flush()
}
