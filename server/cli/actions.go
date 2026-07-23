package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/spf13/cobra"
)

type actionsListResponse struct {
	Actions []api.Action `json:"actions"`
}

func actionsCommand(lookup envLookup) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "actions",
		Short: "Create and administer launch Actions",
	}
	cmd.AddCommand(actionsListCommand(lookup))
	cmd.AddCommand(actionsShowCommand(lookup))
	cmd.AddCommand(actionsCreateCommand(lookup))
	cmd.AddCommand(actionsUpdateCommand(lookup))
	cmd.AddCommand(actionEnabledCommand(lookup, true))
	cmd.AddCommand(actionEnabledCommand(lookup, false))
	cmd.AddCommand(actionsDeleteCommand(lookup))
	return cmd
}

func actionsListCommand(lookup envLookup) *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all Actions",
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
			return writeActionsText(cmd, resp.Actions, true)
		},
	}
	addOutputFlag(cmd, &output)
	return cmd
}

func actionsShowCommand(lookup envLookup) *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show one Action",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return getAndWriteAction(cmd, lookup, args[0], output)
		},
	}
	addOutputFlag(cmd, &output)
	return cmd
}

func actionsCreateCommand(lookup envLookup) *cobra.Command {
	var name, command, description, output string
	var actionArgs []string
	var agent, disabled bool
	cmd := &cobra.Command{
		Use:   "create --name <name> --command <command>",
		Short: "Create an Action",
		Example: `  atc actions create --name Claude --command claude --agent
  atc actions create --name Neovim --command nvim
  atc actions create --name "Dev server" --command npm --arg run --arg dev
  atc actions create --name "Make watcher" --command zsh --arg=-lc --arg "make watch"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutput(output); err != nil {
				return err
			}
			request := map[string]any{
				"name":    name,
				"command": command,
			}
			if cmd.Flags().Changed("description") {
				request["description"] = description
			}
			if len(actionArgs) > 0 {
				request["args"] = actionArgs
			}
			if agent {
				request["isAgent"] = true
			}
			if disabled {
				request["enabled"] = false
			}
			client, err := commandAPIClient(cmd, lookup)
			if err != nil {
				return err
			}
			body, err := client.post(cmd.Context(), "actions", request)
			if err != nil {
				return err
			}
			return writeActionBody(cmd, body, output)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "User-facing Action name")
	cmd.Flags().StringVar(&command, "command", "", "Executable command")
	cmd.Flags().StringVar(&description, "description", "", "Optional description")
	cmd.Flags().StringArrayVar(&actionArgs, "arg", nil, "Literal command argument (repeatable)")
	cmd.Flags().BoolVar(&agent, "agent", false, "Classify the Action as an agent")
	cmd.Flags().BoolVar(&disabled, "disabled", false, "Create the Action disabled")
	addOutputFlag(cmd, &output)
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("command")
	return cmd
}

func actionsUpdateCommand(lookup envLookup) *cobra.Command {
	var name, command, description, output string
	var actionArgs []string
	var clearDescription, clearArgs, agent, enabled bool
	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Partially update an Action",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutput(output); err != nil {
				return err
			}
			request := map[string]any{}
			if cmd.Flags().Changed("name") {
				request["name"] = name
			}
			if cmd.Flags().Changed("command") {
				request["command"] = command
			}
			if cmd.Flags().Changed("description") {
				request["description"] = description
			}
			if clearDescription {
				request["description"] = nil
			}
			if cmd.Flags().Changed("arg") {
				request["args"] = actionArgs
			}
			if clearArgs {
				request["args"] = []string{}
			}
			if cmd.Flags().Changed("agent") {
				request["isAgent"] = agent
			}
			if cmd.Flags().Changed("enabled") {
				request["enabled"] = enabled
			}
			client, err := commandAPIClient(cmd, lookup)
			if err != nil {
				return err
			}
			body, err := client.patch(cmd.Context(), actionEndpoint(args[0]), request)
			if err != nil {
				return err
			}
			return writeActionBody(cmd, body, output)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "New user-facing name")
	cmd.Flags().StringVar(&command, "command", "", "New executable command")
	cmd.Flags().StringVar(&description, "description", "", "Set the description")
	cmd.Flags().BoolVar(&clearDescription, "clear-description", false, "Clear the description")
	cmd.Flags().StringArrayVar(&actionArgs, "arg", nil, "Replace args with literal argument(s)")
	cmd.Flags().BoolVar(&clearArgs, "clear-args", false, "Clear all arguments")
	cmd.Flags().BoolVar(&agent, "agent", false, "Set agent classification (use --agent=false to clear)")
	cmd.Flags().BoolVar(&enabled, "enabled", false, "Set enabled state (use --enabled=false to disable)")
	cmd.MarkFlagsMutuallyExclusive("description", "clear-description")
	cmd.MarkFlagsMutuallyExclusive("arg", "clear-args")
	addOutputFlag(cmd, &output)
	return cmd
}

func actionEnabledCommand(lookup envLookup, enabled bool) *cobra.Command {
	verb := "disable"
	if enabled {
		verb = "enable"
	}
	var output string
	cmd := &cobra.Command{
		Use:   verb + " <id>",
		Short: strings.ToUpper(verb[:1]) + verb[1:] + " an Action",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutput(output); err != nil {
				return err
			}
			client, err := commandAPIClient(cmd, lookup)
			if err != nil {
				return err
			}
			body, err := client.patch(cmd.Context(), actionEndpoint(args[0]), map[string]bool{"enabled": enabled})
			if err != nil {
				return err
			}
			return writeActionBody(cmd, body, output)
		},
	}
	addOutputFlag(cmd, &output)
	return cmd
}

func actionsDeleteCommand(lookup envLookup) *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Permanently delete an Action",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutput(output); err != nil {
				return err
			}
			client, err := commandAPIClient(cmd, lookup)
			if err != nil {
				return err
			}
			body, err := client.get(cmd.Context(), actionEndpoint(args[0]))
			if err != nil {
				return err
			}
			if _, err := client.delete(cmd.Context(), actionEndpoint(args[0])); err != nil {
				return err
			}
			return writeActionBody(cmd, body, output)
		},
	}
	addOutputFlag(cmd, &output)
	return cmd
}

func getAndWriteAction(cmd *cobra.Command, lookup envLookup, id, output string) error {
	if err := validateOutput(output); err != nil {
		return err
	}
	client, err := commandAPIClient(cmd, lookup)
	if err != nil {
		return err
	}
	body, err := client.get(cmd.Context(), actionEndpoint(id))
	if err != nil {
		return err
	}
	return writeActionBody(cmd, body, output)
}

func writeActionBody(cmd *cobra.Command, body []byte, output string) error {
	if output == outputJSON {
		return writeRawJSON(cmd, body)
	}
	var action api.Action
	if err := json.Unmarshal(body, &action); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return writeActionsText(cmd, []api.Action{action}, false)
}

func writeActionsText(cmd *cobra.Command, actions []api.Action, header bool) error {
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	if header {
		fmt.Fprintln(tw, "ID\tNAME\tENABLED\tAGENT\tCOMMAND\tDESCRIPTION")
	}
	for _, action := range actions {
		command := strings.Join(append([]string{action.Command}, action.Args...), " ")
		fmt.Fprintf(tw, "%s\t%s\t%t\t%t\t%s\t%s\n", action.ID, action.Name, action.Enabled, action.IsAgent, command, action.Description)
	}
	return tw.Flush()
}

func addOutputFlag(cmd *cobra.Command, output *string) {
	cmd.Flags().StringVarP(output, "output", "o", outputText, "Output format: text or json")
}

func actionEndpoint(id string) string {
	return "actions/" + url.PathEscape(id)
}
