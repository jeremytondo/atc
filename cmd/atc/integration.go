package main

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/jeremytondo/atc/internal/api"
)

// The atc integration family (ATC-294): the read-only catalog of the
// external systems ATC integrates with — each with its Apps, agents, capabilities, and
// health. Launching an App is `atc terminal create --app`. Commands speak
// only the shared API client; the server owns resolution and probing.

func newIntegrationCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "integration",
		Short: "List ATC's integrations and the apps they offer",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("usage: atc integration <list|get>")
		},
	}
	cmd.AddCommand(newIntegrationListCmd(), newIntegrationGetCmd())
	return cmd
}

func newIntegrationListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List integrations with their apps and availability",
		Long: `List the integrations this ATC server is built with. Availability is
evidence from the server's machine, probed on every call: whether the
tool's executable resolves, or whether its connection is up. Apps are
launched with ` + "`atc terminal create --app <integration/app>`" + `.`,
		Args: cobra.NoArgs,
		RunE: runWithClient(func(cmd *cobra.Command, _ []string, client *api.Client, _ string) error {
			integrations, err := client.Integrations(cmd.Context())
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 8, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "ID\tNAME\tAVAILABLE\tAPPS\tAGENTS")
			for _, integration := range integrations {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", integration.ID, integration.Name,
					availabilityLabel(integration.Available), strings.Join(appIDs(integration), ", "), strings.Join(agentIDs(integration), ", "))
			}
			return w.Flush()
		}),
	}
}

func newIntegrationGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Show one integration",
		Args:  cobra.ExactArgs(1),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			integration, err := client.Integration(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			printIntegration(cmd.OutOrStdout(), integration)
			return nil
		}),
	}
}

func availabilityLabel(available bool) string {
	if available {
		return "yes"
	}
	return "no"
}

func appIDs(integration api.Integration) []string {
	ids := make([]string, 0, len(integration.Apps))
	for _, app := range integration.Apps {
		ids = append(ids, app.ID)
	}
	return ids
}

func agentIDs(integration api.Integration) []string {
	ids := make([]string, 0, len(integration.Agents))
	for _, agent := range integration.Agents {
		ids = append(ids, agent.ID)
	}
	return ids
}

func printIntegration(out io.Writer, integration api.Integration) {
	w := tabwriter.NewWriter(out, 2, 8, 2, ' ', 0)
	_, _ = fmt.Fprintf(w, "id\t%s\n", integration.ID)
	_, _ = fmt.Fprintf(w, "name\t%s\n", integration.Name)
	_, _ = fmt.Fprintf(w, "available\t%s\n", availabilityLabel(integration.Available))
	if integration.InstallHint != "" {
		_, _ = fmt.Fprintf(w, "install\t%s\n", integration.InstallHint)
	}
	if integration.Connection != nil {
		_, _ = fmt.Fprintf(w, "connection\t%s (%s)\n", integration.Connection.State, integration.Connection.Detail)
		_, _ = fmt.Fprintf(w, "since\t%s\n", integration.Connection.Since.Format("2006-01-02 15:04:05 MST"))
	}
	capabilities := make([]string, 0, len(integration.Capabilities))
	for _, capability := range integration.Capabilities {
		capabilities = append(capabilities, string(capability))
	}
	_, _ = fmt.Fprintf(w, "capabilities\t%s\n", strings.Join(capabilities, ", "))
	for _, agent := range integration.Agents {
		_, _ = fmt.Fprintf(w, "agent\t%s (%s)\n", agent.ID, agent.Name)
	}
	for _, app := range integration.Apps {
		interactions := make([]string, 0, len(app.Interactions))
		for _, interaction := range app.Interactions {
			interactions = append(interactions, string(interaction))
		}
		line := fmt.Sprintf("%s (%s): %s", app.ID, app.Name, strings.Join(interactions, ", "))
		if app.Available != nil {
			line += "; available " + availabilityLabel(*app.Available)
		}
		_, _ = fmt.Fprintf(w, "app\t%s\n", line)
	}
	_ = w.Flush()
}
