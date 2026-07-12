package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/spf13/cobra"
)

// filesNotTouched is the ADR 0008 statement destructive commands print:
// deletion removes atc metadata only and never touches files on disk.
const filesNotTouched = "Files on disk were not touched."

func workspacesCommand(lookup envLookup) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workspaces",
		Short: "Create and manage workspaces",
	}
	cmd.AddCommand(workspacesCreateCommand(lookup))
	cmd.AddCommand(workspacesListCommand(lookup))
	cmd.AddCommand(workspacesShowCommand(lookup))
	cmd.AddCommand(workspacesRenameCommand(lookup))
	cmd.AddCommand(workspacesArchiveCommand(lookup))
	cmd.AddCommand(workspacesUnarchiveCommand(lookup))
	cmd.AddCommand(workspacesDeleteCommand(lookup))
	return cmd
}

func workspacesCreateCommand(lookup envLookup) *cobra.Command {
	var projectID, name, output string

	cmd := &cobra.Command{
		Use:   "create --project <id> --name <name>",
		Short: "Create a workspace in a project",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutput(output); err != nil {
				return err
			}
			client, err := commandAPIClient(cmd, lookup)
			if err != nil {
				return err
			}
			body, err := client.post(cmd.Context(), "workspaces", map[string]any{
				"projectId": projectID,
				"name":      name,
			})
			if err != nil {
				return err
			}
			if output == outputJSON {
				return writeRawJSON(cmd, body)
			}

			var resp api.Workspace
			if err := json.Unmarshal(body, &resp); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", resp.ID, resp.Name)
			return nil
		},
	}

	cmd.Flags().StringVar(&projectID, "project", "", "Project the workspace belongs to")
	cmd.Flags().StringVar(&name, "name", "", "Workspace name")
	cmd.Flags().StringVarP(&output, "output", "o", outputText, "Output format: text or json")
	_ = cmd.MarkFlagRequired("project")
	_ = cmd.MarkFlagRequired("name")

	return cmd
}

func workspacesListCommand(lookup envLookup) *cobra.Command {
	var output, projectID string
	var includeArchived bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List workspaces",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutput(output); err != nil {
				return err
			}

			query := url.Values{}
			if projectID != "" {
				query.Set("projectId", projectID)
			}
			if includeArchived {
				query.Set("includeArchived", "true")
			}

			client, err := commandAPIClient(cmd, lookup)
			if err != nil {
				return err
			}
			body, err := client.getQuery(cmd.Context(), "workspaces", query)
			if err != nil {
				return err
			}
			if output == outputJSON {
				return writeRawJSON(cmd, body)
			}

			var resp api.WorkspaceListResponse
			if err := json.Unmarshal(body, &resp); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			for _, ws := range resp.Workspaces {
				archivedAt := ""
				if ws.ArchivedAt != nil {
					archivedAt = *ws.ArchivedAt
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", ws.ID, ws.Name, ws.ProjectID, archivedAt)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", outputText, "Output format: text or json")
	cmd.Flags().StringVar(&projectID, "project", "", "List only one project's workspaces")
	cmd.Flags().BoolVar(&includeArchived, "include-archived", false, "Include archived workspaces")
	return cmd
}

func workspacesShowCommand(lookup envLookup) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show one workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutput(output); err != nil {
				return err
			}
			client, err := commandAPIClient(cmd, lookup)
			if err != nil {
				return err
			}
			body, err := client.get(cmd.Context(), workspaceEndpoint(args[0]))
			if err != nil {
				return err
			}
			if output == outputJSON {
				return writeRawJSON(cmd, body)
			}

			var resp api.Workspace
			if err := json.Unmarshal(body, &resp); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "id\t%s\n", resp.ID)
			fmt.Fprintf(out, "name\t%s\n", resp.Name)
			fmt.Fprintf(out, "projectId\t%s\n", resp.ProjectID)
			fmt.Fprintf(out, "createdAt\t%s\n", resp.CreatedAt)
			fmt.Fprintf(out, "updatedAt\t%s\n", resp.UpdatedAt)
			if resp.ArchivedAt != nil {
				fmt.Fprintf(out, "archivedAt\t%s\n", *resp.ArchivedAt)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", outputText, "Output format: text or json")
	return cmd
}

func workspacesRenameCommand(lookup envLookup) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "rename <id> <name>",
		Short: "Rename a workspace",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutput(output); err != nil {
				return err
			}
			client, err := commandAPIClient(cmd, lookup)
			if err != nil {
				return err
			}
			body, err := client.patch(cmd.Context(), workspaceEndpoint(args[0]), map[string]any{
				"name": args[1],
			})
			if err != nil {
				return err
			}
			if output == outputJSON {
				return writeRawJSON(cmd, body)
			}

			var resp api.Workspace
			if err := json.Unmarshal(body, &resp); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", resp.ID, resp.Name)
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", outputText, "Output format: text or json")
	return cmd
}

func workspacesArchiveCommand(lookup envLookup) *cobra.Command {
	return resourceActionCommand(lookup, "archive <id>", "Archive a workspace with no active sessions", 1, func(id string, args []string) (string, any) {
		return workspaceEndpoint(id, "archive"), struct{}{}
	})
}

func workspacesUnarchiveCommand(lookup envLookup) *cobra.Command {
	return resourceActionCommand(lookup, "unarchive <id>", "Unarchive a workspace", 1, func(id string, args []string) (string, any) {
		return workspaceEndpoint(id, "unarchive"), struct{}{}
	})
}

func workspacesDeleteCommand(lookup envLookup) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a workspace: stops its active sessions and removes its session metadata",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := commandAPIClient(cmd, lookup)
			if err != nil {
				return err
			}
			if _, err := client.delete(cmd.Context(), workspaceEndpoint(args[0])); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted workspace %s. %s\n", args[0], filesNotTouched)
			return nil
		},
	}
}

func workspaceEndpoint(id string, parts ...string) string {
	segments := append([]string{"workspaces", url.PathEscape(id)}, parts...)
	return strings.Join(segments, "/")
}
