package main

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/jeremytondo/atc/internal/api"
)

// The atc agent family (ATC-254, ATC-285): the catalog reads — the
// launchable agents, and the adapters behind them with their health.
// Starting an agent is `atc thread new` (ATC-282) — conversations are the
// front door, agents the catalog behind it. Commands speak only the
// shared API client; the server owns resolution and probing.

func newAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "See the coding agents ATC works with",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("usage: atc agent <list|get|adapter>")
		},
	}
	cmd.AddCommand(newAgentListCmd(), newAgentGetCmd(), newAgentAdapterCmd())
	return cmd
}

func newAgentListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List agents with their availability and adapters",
		Long: `List the agents this ATC server knows. An agent is available when some
adapter can launch it right now, probed against the server's machine on
every call; see ` + "`atc agent adapter list`" + ` for why one is not.`,
		Args: cobra.NoArgs,
		RunE: runWithClient(func(cmd *cobra.Command, _ []string, client *api.Client, _ string) error {
			agents, err := client.Agents(cmd.Context())
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 8, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "ID\tNAME\tAVAILABLE\tADAPTERS")
			for _, agent := range agents {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", agent.ID, agent.Name, availabilityLabel(agent.Available), strings.Join(agent.Adapters, ", "))
			}
			return w.Flush()
		}),
	}
}

func newAgentGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Show one agent",
		Args:  cobra.ExactArgs(1),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			agent, err := client.Agent(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			printAgent(cmd.OutOrStdout(), agent)
			return nil
		}),
	}
}

func newAgentAdapterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "adapter",
		Short: "See the adapters that produce threads",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("usage: atc agent adapter <list|get>")
		},
	}
	cmd.AddCommand(newAgentAdapterListCmd(), newAgentAdapterGetCmd())
	return cmd
}

func newAgentAdapterListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List adapters with their availability",
		Long: `List the adapters this ATC server produces threads through: launchers of
local agent TUIs, which name how to install a missing tool, and observers
of external programs such as T3 Code, which report their connection.`,
		Args: cobra.NoArgs,
		RunE: runWithClient(func(cmd *cobra.Command, _ []string, client *api.Client, _ string) error {
			adapters, err := client.AgentAdapters(cmd.Context())
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 8, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "ID\tNAME\tAVAILABLE\tAGENTS\tDETAIL")
			for _, adapter := range adapters {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", adapter.ID, adapter.Name, availabilityLabel(adapter.Available),
					strings.Join(adapter.Agents, ", "), adapterDetail(adapter))
			}
			return w.Flush()
		}),
	}
}

func newAgentAdapterGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Show one adapter, including its connection or install hint",
		Args:  cobra.ExactArgs(1),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			adapter, err := client.AgentAdapter(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			printAdapter(cmd.OutOrStdout(), adapter)
			return nil
		}),
	}
}

func availabilityLabel(available bool) string {
	if available {
		return "available"
	}
	return "unavailable"
}

// adapterDetail is the one-line explanation of an adapter's state: the
// connection for an observer, the install hint for an unavailable
// launcher.
func adapterDetail(adapter api.AgentAdapter) string {
	if adapter.Connection != nil {
		return string(adapter.Connection.State) + ": " + adapter.Connection.Detail
	}
	if !adapter.Available {
		return "install: " + adapter.InstallHint
	}
	return ""
}

func printAgent(out io.Writer, agent api.Agent) {
	w := tabwriter.NewWriter(out, 2, 8, 2, ' ', 0)
	_, _ = fmt.Fprintf(w, "id\t%s\n", agent.ID)
	_, _ = fmt.Fprintf(w, "name\t%s\n", agent.Name)
	_, _ = fmt.Fprintf(w, "available\t%s\n", availabilityLabel(agent.Available))
	_, _ = fmt.Fprintf(w, "adapters\t%s\n", strings.Join(agent.Adapters, ", "))
	_ = w.Flush()
}

func printAdapter(out io.Writer, adapter api.AgentAdapter) {
	w := tabwriter.NewWriter(out, 2, 8, 2, ' ', 0)
	_, _ = fmt.Fprintf(w, "id\t%s\n", adapter.ID)
	_, _ = fmt.Fprintf(w, "name\t%s\n", adapter.Name)
	_, _ = fmt.Fprintf(w, "available\t%s\n", availabilityLabel(adapter.Available))
	_, _ = fmt.Fprintf(w, "agents\t%s\n", strings.Join(adapter.Agents, ", "))
	if adapter.InstallHint != "" {
		_, _ = fmt.Fprintf(w, "install\t%s\n", adapter.InstallHint)
	}
	if adapter.Connection != nil {
		_, _ = fmt.Fprintf(w, "connection\t%s\n", adapter.Connection.State)
		_, _ = fmt.Fprintf(w, "since\t%s\n", adapter.Connection.Since.Local().Format("2006-01-02 15:04:05 MST"))
		_, _ = fmt.Fprintf(w, "detail\t%s\n", adapter.Connection.Detail)
	}
	_ = w.Flush()
}
