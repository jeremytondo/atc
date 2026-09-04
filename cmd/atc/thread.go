package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/jeremytondo/atc/internal/api"
)

// The atc thread family: the front door to agent conversations (ATC-282)
// — open puts you in front of any conversation, live or dormant — create
// starts one with a prompt in an integration's own program (ATC-289) —
// plus the reads and the two mutations over observed conversations
// (ATC-255). Conversations started in an ATC terminal app (`atc terminal
// create --app`) are observed into existence from their first prompt.
// archive/unarchive are thin sugar over PATCH.

func newThreadCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "thread",
		Short: "Start, open, and manage agent conversations",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("usage: atc thread <create|open|list|get|update|archive|unarchive|delete>")
		},
	}
	cmd.AddCommand(newThreadCreateCmd(), newThreadOpenCmd(), newThreadListCmd(), newThreadGetCmd(),
		newThreadUpdateCmd(), newThreadArchiveCmd(), newThreadUnarchiveCmd(), newThreadDeleteCmd())
	return cmd
}

func newThreadCreateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create --integration <id> --agent <id> --project <id> --model <name> [prompt]",
		Short: "Start a conversation with a prompt in an integration's program",
		Long: `Start a new conversation with its first prompt in the named integration's
own program (t3code), for the given agent, in the given project. The prompt is
the positional argument; when it is absent or "-", it is read from standard
input.

The model name and any --option pairs are passed to the integration untouched:
ATC keeps no model catalog, and a value the program rejects shows up afterwards
as the thread's status and detail. The command returns as soon as the program
has committed the thread and its first turn, printing the thread as
` + "`atc thread get`" + ` does; the program's own events drive it from there.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			flags := cmd.Flags()
			params := api.ThreadCreateParams{}
			var err error
			if params.IntegrationID, err = flags.GetString("integration"); err != nil {
				return err
			}
			if params.Agent, err = flags.GetString("agent"); err != nil {
				return err
			}
			if params.ProjectID, err = flags.GetString("project"); err != nil {
				return err
			}
			if params.Model, err = flags.GetString("model"); err != nil {
				return err
			}
			options, err := flags.GetStringArray("option")
			if err != nil {
				return err
			}
			for _, option := range options {
				id, value, ok := strings.Cut(option, "=")
				if !ok || id == "" {
					return fmt.Errorf("--option %q is not id=value", option)
				}
				params.Options = append(params.Options, api.ThreadOption{ID: id, Value: value})
			}
			if len(args) == 1 && args[0] != "-" {
				params.Prompt = args[0]
			} else {
				prompt, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return fmt.Errorf("reading the prompt from stdin: %w", err)
				}
				params.Prompt = string(prompt)
			}
			if strings.TrimSpace(params.Prompt) == "" {
				return fmt.Errorf("the prompt is empty; pass it as an argument or on stdin")
			}
			thread, err := client.CreateThread(cmd.Context(), params)
			if err != nil {
				return err
			}
			printThread(cmd.OutOrStdout(), thread)
			return nil
		}),
	}
	cmd.Flags().String("integration", "", "integration that runs the work (t3code)")
	cmd.Flags().String("agent", "", "agent id the integration lists (codex, claudeAgent, ...)")
	cmd.Flags().String("project", "", "project the conversation runs in")
	cmd.Flags().String("model", "", "model identifier, passed through untouched")
	cmd.Flags().StringArray("option", nil, "provider option as id=value, passed through untouched (repeatable)")
	for _, flag := range []string{"integration", "agent", "project", "model"} {
		_ = cmd.MarkFlagRequired(flag)
	}
	return cmd
}

func newThreadOpenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "open <id>",
		Short: "Open a conversation, live or dormant, and attach",
		Long: `Open a conversation in its running terminal, or resume it in a new one.
This is shorthand for ` + "`atc terminal create --thread <id>`" + `.

Placement and attachment follow ` + "`atc terminal create`" + `. Use --detach to
leave the terminal running in the background. Archived threads are unarchived
automatically.

Conversations not started by an ATC terminal app cannot be opened here; use the
links shown by ` + "`atc thread get <id>`" + `.`,
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
		Long: `Set a thread's title or project. Titles set here are not overwritten by
later observation. Use --remove-project to leave the thread unassigned.`,
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
		Long: `Archive a thread so it is hidden from lists unless requested. Restore it
with unarchive. A thread currently open in a terminal cannot be archived.`,
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
		Long: `Delete ATC's thread record without deleting the provider's conversation.
The conversation remains resumable in the provider's tooling. A thread currently
open in a terminal cannot be deleted.`,
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
	if thread.StatusDetail != "" {
		_, _ = fmt.Fprintf(w, "status detail\t%s\n", thread.StatusDetail)
	}
	if turn := thread.LatestTurn; turn != nil {
		_, _ = fmt.Fprintf(w, "latest turn\t%s\n", turn.ID)
		_, _ = fmt.Fprintf(w, "turn state\t%s\n", turn.State)
		_, _ = fmt.Fprintf(w, "turn started\t%s\n", turn.StartedAt.Format("2006-01-02 15:04:05 MST"))
		if turn.CompletedAt != nil {
			_, _ = fmt.Fprintf(w, "turn completed\t%s\n", turn.CompletedAt.Format("2006-01-02 15:04:05 MST"))
		}
		if turn.Error != "" {
			_, _ = fmt.Fprintf(w, "turn error\t%s\n", turn.Error)
		}
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
