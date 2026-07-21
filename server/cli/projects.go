package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/spf13/cobra"
)

func projectsCommand(lookup envLookup) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "projects",
		Short: "Create and manage projects",
	}
	cmd.AddCommand(projectsCreateCommand(lookup))
	cmd.AddCommand(projectsListCommand(lookup))
	cmd.AddCommand(projectsShowCommand(lookup))
	cmd.AddCommand(projectsRenameCommand(lookup))
	cmd.AddCommand(projectsDeleteCommand(lookup))
	return cmd
}

func projectsCreateCommand(lookup envLookup) *cobra.Command {
	var name, dir, output string

	cmd := &cobra.Command{
		Use:   "create --name <name> --dir <path>",
		Short: "Create a project",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutput(output); err != nil {
				return err
			}
			resolvedDir, err := resolveDirArg(dir)
			if err != nil {
				return err
			}

			client, err := commandAPIClient(cmd, lookup)
			if err != nil {
				return err
			}
			body, err := client.post(cmd.Context(), "projects", map[string]any{
				"name":       name,
				"workingDir": resolvedDir,
			})
			if err != nil {
				return err
			}
			if output == outputJSON {
				return writeRawJSON(cmd, body)
			}

			var resp api.Project
			if err := json.Unmarshal(body, &resp); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", resp.ID, resp.Name)
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Project name")
	cmd.Flags().StringVar(&dir, "dir", "", "Project working directory")
	cmd.Flags().StringVarP(&output, "output", "o", outputText, "Output format: text or json")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("dir")

	return cmd
}

func projectsListCommand(lookup envLookup) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List projects",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutput(output); err != nil {
				return err
			}

			client, err := commandAPIClient(cmd, lookup)
			if err != nil {
				return err
			}
			body, err := client.get(cmd.Context(), "projects")
			if err != nil {
				return err
			}
			if output == outputJSON {
				return writeRawJSON(cmd, body)
			}

			var resp api.ProjectListResponse
			if err := json.Unmarshal(body, &resp); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			for _, p := range resp.Projects {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", p.ID, p.Name, p.WorkingDir)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", outputText, "Output format: text or json")
	return cmd
}

func projectsShowCommand(lookup envLookup) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show one project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutput(output); err != nil {
				return err
			}
			client, err := commandAPIClient(cmd, lookup)
			if err != nil {
				return err
			}
			body, err := client.get(cmd.Context(), projectEndpoint(args[0]))
			if err != nil {
				return err
			}
			if output == outputJSON {
				return writeRawJSON(cmd, body)
			}

			var resp api.Project
			if err := json.Unmarshal(body, &resp); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "id\t%s\n", resp.ID)
			fmt.Fprintf(out, "name\t%s\n", resp.Name)
			fmt.Fprintf(out, "workingDir\t%s\n", resp.WorkingDir)
			fmt.Fprintf(out, "createdAt\t%s\n", resp.CreatedAt)
			fmt.Fprintf(out, "updatedAt\t%s\n", resp.UpdatedAt)
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", outputText, "Output format: text or json")
	return cmd
}

func projectsRenameCommand(lookup envLookup) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "rename <id> <name>",
		Short: "Rename a project",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutput(output); err != nil {
				return err
			}
			client, err := commandAPIClient(cmd, lookup)
			if err != nil {
				return err
			}
			body, err := client.patch(cmd.Context(), projectEndpoint(args[0]), map[string]any{
				"name": args[1],
			})
			if err != nil {
				return err
			}
			if output == outputJSON {
				return writeRawJSON(cmd, body)
			}

			var resp api.Project
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

func projectsDeleteCommand(lookup envLookup) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a project with zero workspaces",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := commandAPIClient(cmd, lookup)
			if err != nil {
				return err
			}
			if _, err := client.delete(cmd.Context(), projectEndpoint(args[0])); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted project %s. %s\n", args[0], filesNotTouched)
			return nil
		},
	}
}

// resolveDirArg expands a --dir argument before it is sent to the API:
// relative paths resolve against the CLI's cwd, and a leading ~ or ~/ expands
// to the user's home directory. ~user and environment variables are not
// expanded.
func resolveDirArg(dir string) (string, error) {
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		if dir == "~" {
			return home, nil
		}
		return filepath.Join(home, dir[2:]), nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve directory %q: %w", dir, err)
	}
	return abs, nil
}

func projectEndpoint(id string, parts ...string) string {
	segments := append([]string{"projects", url.PathEscape(id)}, parts...)
	return strings.Join(segments, "/")
}
