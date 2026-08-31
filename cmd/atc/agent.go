package main

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/jeremytondo/atc/internal/api"
)

// The atc agent family (ATC-254): the catalog reads. Starting an agent is
// `atc thread new` (ATC-282) — conversations are the front door, agents
// the catalog behind it. Commands speak only the shared API client; the
// server owns resolution and probing.

func newAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "See the coding agents ATC works with",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("usage: atc agent <list|get>")
		},
	}
	cmd.AddCommand(newAgentListCmd(), newAgentGetCmd())
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
