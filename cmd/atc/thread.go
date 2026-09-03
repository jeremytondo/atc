package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/jeremytondo/atc/internal/api"
)

// The atc thread family: the front door to agent conversations (ATC-282)
// — open puts you in front of any conversation, live or dormant — plus
// the reads and the two mutations over observed conversations (ATC-255).
// There is no create: a thread record exists from the conversation's
// first prompt, observed inside the app that started it (`atc terminal
// create --app`). archive/unarchive are thin sugar over PATCH.

func newThreadCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "thread",
		Short: "Open and manage agent conversations",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("usage: atc thread <open|list|get|update|archive|unarchive|delete>")
		},
	}
	cmd.AddCommand(newThreadOpenCmd(), newThreadListCmd(), newThreadGetCmd(),
		newThreadUpdateCmd(), newThreadArchiveCmd(), newThreadUnarchiveCmd(), newThreadDeleteCmd())
	return cmd
}

func newThreadOpenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "open <id>",
		Short: "Open a conversation, live or dormant, and attach",
		Long: `Put your shell in front of the conversation — sugar for
` + "`atc terminal create --thread <id>`" + `. The server picks exactly one
terminal: the running terminal showing the thread, else its last terminal
if that is still running, else a new terminal resuming the exact
conversation in the app that started it, placed like any new terminal
(--space, --directory; your current directory against a local server).
Concurrent opens of one thread land in the same terminal, so a
conversation never has two writers through ATC. An archived thread is
unarchived. A thread that was not started in an ATC terminal app (one T3
Code owns) is refused: open it through the links ` + "`atc thread get`" + `
shows. Pass --detach to print the terminal instead of attaching; where
attaching is impossible (no TTY, or the server is on another machine) the
terminal is still printed.`,
		Args: cobra.ExactArgs(1),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, baseURL string) error {
			params, err := placementParams(cmd, baseURL)
			if err != nil {
				return err
			}
			params.ThreadID = args[0]
			return runAndMaybeAttach(cmd, baseURL, func(ctx context.Context) (api.Terminal, error) {
				return client.CreateTerminal(ctx, params)
			})
		}),
	}
	addPlacementFlags(cmd)
	return cmd
}

func newThreadListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List threads, most recent activity first",
		Long: `List every observed agent conversation (pass --project to scope to one
project, --terminal to the thread a terminal last held). Archived threads
are hidden unless --archived.`,
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
			terminal, err := flags.GetString("terminal")
			if err != nil {
				return err
			}
			threads, err := client.Threads(cmd.Context(), project, terminal, archived)
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
			_, _ = fmt.Fprintln(w, "ID\tSTATUS\tINTEGRATION\tAGENT\tTERMINAL\tTITLE")
			for _, thread := range threads {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
					thread.ID, threadStatusLabel(thread), thread.IntegrationID, thread.AgentID, thread.TerminalID, thread.Title)
			}
			return w.Flush()
		}),
	}
	cmd.Flags().String("project", "", "only threads belonging to this project")
	cmd.Flags().String("terminal", "", "only the thread this terminal last held")
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
		Short: "Update a thread's title or project",
		Long: `Set a thread's title, or move it between projects. A title set here is
yours: observation never overwrites it afterwards. --project assigns any
project, whatever directory the thread originated in; --remove-project
leaves it unassigned until a project is created or moved to contain its
initial directory.`,
		Args: cobra.ExactArgs(1),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			flags := cmd.Flags()
			var params api.ThreadUpdateParams
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
				if project == "" {
					return fmt.Errorf("--project needs a project id; use --remove-project to unassign")
				}
				params.ProjectID = api.Some(project)
			}
			if flags.Changed("remove-project") {
				if remove, err := flags.GetBool("remove-project"); err != nil {
					return err
				} else if !remove {
					return fmt.Errorf("--remove-project=false changes nothing; omit the flag")
				}
				params.ProjectID = api.Clear[string]()
			}
			thread, err := client.UpdateThread(cmd.Context(), args[0], params)
			if err != nil {
				return err
			}
			printThread(cmd.OutOrStdout(), thread)
			return nil
		}),
	}
	cmd.Flags().String("title", "", "new display title")
	cmd.Flags().String("project", "", "project to assign the thread to")
	cmd.Flags().Bool("remove-project", false, "leave the thread unassigned")
	cmd.MarkFlagsMutuallyExclusive("project", "remove-project")
	cmd.MarkFlagsOneRequired("title", "project", "remove-project")
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
		thread, err := client.UpdateThread(cmd.Context(), args[0], api.ThreadUpdateParams{Archived: api.Some(archived)})
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
	_, _ = fmt.Fprintf(w, "integration\t%s\n", thread.IntegrationID)
	if thread.AppID != "" {
		_, _ = fmt.Fprintf(w, "app\t%s\n", thread.AppID)
	}
	if thread.AgentID != "" {
		_, _ = fmt.Fprintf(w, "agent\t%s\n", thread.AgentID)
	}
	_, _ = fmt.Fprintf(w, "status\t%s\n", threadStatusLabel(thread))
	if thread.Title != "" {
		_, _ = fmt.Fprintf(w, "title\t%s\n", thread.Title)
	}
	if thread.ProjectID != "" {
		_, _ = fmt.Fprintf(w, "project\t%s\n", thread.ProjectID)
	}
	if thread.InitialDirectory != "" {
		_, _ = fmt.Fprintf(w, "initial directory\t%s\n", thread.InitialDirectory)
	}
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
	if thread.Links != nil {
		_, _ = fmt.Fprintf(w, "web\t%s\n", thread.Links.Web)
		_, _ = fmt.Fprintf(w, "app\t%s\n", thread.Links.App)
	}
	_, _ = fmt.Fprintf(w, "created\t%s\n", thread.CreatedAt.Format("2006-01-02 15:04:05 MST"))
	_ = w.Flush()
}
