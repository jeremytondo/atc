package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/jeremytondo/atc/internal/api"
)

// The atc agent family (ATC-254): the catalog reads plus launch — the one
// human-facing way to start an agent TUI. Commands speak only the shared
// API client; the server owns resolution, probing, and composition.

func newAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "See and launch the coding agents ATC works with",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("usage: atc agent <list|get|launch>")
		},
	}
	cmd.AddCommand(newAgentListCmd(), newAgentGetCmd(), newAgentLaunchCmd())
	return cmd
}

func newAgentListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List agents with per-capability availability",
		Long: `List the agents this ATC server can work with. Availability is probed
against the server's machine on every call: an unavailable capability
names how to install the missing tool.`,
		Args: cobra.NoArgs,
		RunE: runWithClient(func(cmd *cobra.Command, _ []string, client *api.Client, _ string) error {
			agents, err := client.Agents(cmd.Context())
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 8, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "ID\tNAME\tCAPABILITIES")
			for _, agent := range agents {
				capabilities := make([]string, 0, len(agent.Capabilities))
				for _, capability := range agent.Capabilities {
					label := fmt.Sprintf("%s (%s)", capability.Capability, availabilityLabel(capability.Available))
					if !capability.Available {
						label = fmt.Sprintf("%s (unavailable; install: %s)", capability.Capability, capability.InstallHint)
					}
					capabilities = append(capabilities, label)
				}
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", agent.ID, agent.Name, strings.Join(capabilities, ", "))
			}
			return w.Flush()
		}),
	}
}

func newAgentGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Show one agent, including how to install it",
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

func newAgentLaunchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "launch <id>",
		Short: "Launch an agent TUI in a new terminal",
		Long: `Launch the agent's TUI in a managed terminal — the terminal lands in the
project owning the current directory (pass --project to pick one) and
starts in that project's directory. The session survives disconnects and
server restarts; attach with ` + "`atc terminal attach <id>`" + `, or pass
` + "`--attach`" + ` to attach immediately. The server resolves the launch
command; a missing agent binary is refused with its install hint before
anything is created.`,
		Args: cobra.ExactArgs(1),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, baseURL string) error {
			return createAndMaybeAttach(cmd, client, baseURL, func(ctx context.Context, projectID, name string) (api.Terminal, error) {
				return client.LaunchAgent(ctx, args[0], api.AgentLaunchParams{ProjectID: projectID, Name: name})
			})
		}),
	}
	cmd.Flags().String("name", "", "display name (defaults to the agent's display name)")
	cmd.Flags().String("project", "", "project the terminal belongs to (defaults to the project owning the current directory)")
	cmd.Flags().Bool("attach", false, "attach to the terminal after launch")
	return cmd
}

func availabilityLabel(available bool) string {
	if available {
		return "available"
	}
	return "unavailable"
}

func printAgent(out io.Writer, agent api.Agent) {
	w := tabwriter.NewWriter(out, 2, 8, 2, ' ', 0)
	_, _ = fmt.Fprintf(w, "id\t%s\n", agent.ID)
	_, _ = fmt.Fprintf(w, "name\t%s\n", agent.Name)
	for _, capability := range agent.Capabilities {
		_, _ = fmt.Fprintf(w, "%s\t%s\tinstall: %s\n", capability.Capability, availabilityLabel(capability.Available), capability.InstallHint)
	}
	_ = w.Flush()
}
