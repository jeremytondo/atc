package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

type actionsListResponse struct {
	Actions []actionItem `json:"actions"`
}

type actionItem struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Label string `json:"label,omitempty"`
}

func actionsCommand(lookup envLookup) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "actions",
		Short: "Discover launchable atc actions",
	}
	cmd.AddCommand(actionsListCommand(lookup))
	return cmd
}

func actionsListCommand(lookup envLookup) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List configured actions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutput(output); err != nil {
				return err
			}

			client, err := commandAPIClient(cmd, lookup)
			if err != nil {
				return err
			}
			body, err := client.get(cmd.Context(), "actions")
			if err != nil {
				return err
			}
			if output == outputJSON {
				return writeRawJSON(cmd, body)
			}

			var resp actionsListResponse
			if err := json.Unmarshal(body, &resp); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			for _, action := range resp.Actions {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", action.Name, action.Type, action.Label)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", outputText, "Output format: text or json")
	return cmd
}
