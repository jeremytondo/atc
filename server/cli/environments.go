package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

type environmentsListResponse struct {
	Environments []environmentItem `json:"environments"`
}

type environmentItem struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Label   string `json:"label,omitempty"`
	Default bool   `json:"default,omitempty"`
}

func environmentsCommand(lookup envLookup) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "environments",
		Short: "Discover Atelier Code launch environments",
	}
	cmd.AddCommand(environmentsListCommand(lookup))
	return cmd
}

func environmentsListCommand(lookup envLookup) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List configured environments",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutput(output); err != nil {
				return err
			}

			client, err := commandAPIClient(cmd, lookup)
			if err != nil {
				return err
			}
			body, err := client.get(cmd.Context(), "environments")
			if err != nil {
				return err
			}
			if output == outputJSON {
				return writeRawJSON(cmd, body)
			}

			var resp environmentsListResponse
			if err := json.Unmarshal(body, &resp); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			for _, environment := range resp.Environments {
				defaultMarker := ""
				if environment.Default {
					defaultMarker = "default"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", environment.Name, environment.Kind, defaultMarker, environment.Label)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", outputText, "Output format: text or json")
	return cmd
}
