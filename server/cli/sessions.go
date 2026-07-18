package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/coder/websocket"
	"github.com/jeremytondo/atc/internal/api"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

const (
	outputText = "text"
	outputJSON = "json"
)

func sessionsCommand(lookup envLookup) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "Start and manage sessions",
	}
	cmd.AddCommand(sessionsStartCommand(lookup))
	cmd.AddCommand(sessionsListCommand(lookup))
	cmd.AddCommand(sessionsShowCommand(lookup))
	cmd.AddCommand(sessionsRenameCommand(lookup))
	cmd.AddCommand(sessionsAttachCommand(lookup))
	cmd.AddCommand(sessionsSendTextCommand(lookup))
	cmd.AddCommand(sessionsSendKeyCommand(lookup))
	cmd.AddCommand(sessionsDeleteCommand(lookup))
	return cmd
}

func sessionsStartCommand(lookup envLookup) *cobra.Command {
	var action, environment, prompt, name, workspaceID, output string
	var params []string

	cmd := &cobra.Command{
		Use:   "start --workspace <id> [--action <name>] [--env <name>] [--param key=value]... [--prompt <text>] [--name <name>]",
		Short: "Start a session in a workspace (no action starts the interactive shell)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutput(output); err != nil {
				return err
			}
			paramMap, err := parseParams(params)
			if err != nil {
				return err
			}
			request := map[string]any{
				"workspaceId": workspaceID,
				"environment": environment,
				"prompt":      prompt,
				"name":        name,
			}
			// action is optional: omitted, the server launches the Interactive
			// Shell, which accepts neither params nor a prompt. The keys are
			// left out entirely so the server sees a true omission.
			if action != "" {
				request["action"] = action
				request["params"] = paramMap
			}

			client, err := commandAPIClient(cmd, lookup)
			if err != nil {
				return err
			}
			body, err := client.post(cmd.Context(), "sessions/start", request)
			if err != nil {
				return err
			}
			if output == outputJSON {
				return writeRawJSON(cmd, body)
			}

			var resp api.SessionDetail
			if err := json.Unmarshal(body, &resp); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", resp.ID, resp.Status)
			return nil
		},
	}

	cmd.Flags().StringVar(&workspaceID, "workspace", "", "Workspace to start the session in (inherits the project's working directory)")
	cmd.Flags().StringVar(&action, "action", "", "Action to launch (default: the interactive shell)")
	cmd.Flags().StringVar(&environment, "env", "", "Environment to run in (default: host-login-shell)")
	cmd.Flags().StringArrayVar(&params, "param", nil, "Action parameter as key=value (repeatable)")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Initial prompt for the agent")
	cmd.Flags().StringVar(&name, "name", "", "Human-readable session name")
	cmd.Flags().StringVarP(&output, "output", "o", outputText, "Output format: text or json")
	_ = cmd.MarkFlagRequired("workspace")

	return cmd
}

func sessionsListCommand(lookup envLookup) *cobra.Command {
	var output, status, projectID, workspaceID string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List atc-managed sessions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutput(output); err != nil {
				return err
			}
			if err := validateSessionStatus(status); err != nil {
				return err
			}

			query := url.Values{}
			if status != "" {
				query.Set("status", status)
			}
			endpoint := "sessions"
			if workspaceID != "" {
				endpoint = workspaceEndpoint(workspaceID, "sessions")
			} else if projectID != "" {
				endpoint = projectEndpoint(projectID, "sessions")
			}

			client, err := commandAPIClient(cmd, lookup)
			if err != nil {
				return err
			}
			body, err := client.getQuery(cmd.Context(), endpoint, query)
			if err != nil {
				return err
			}
			if output == outputJSON {
				return writeRawJSON(cmd, body)
			}

			var resp api.SessionListResponse
			if err := json.Unmarshal(body, &resp); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			for _, s := range resp.Sessions {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\t%s\n", s.ID, s.Status, s.Action, s.Environment, s.WorkingDir, s.Name)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", outputText, "Output format: text or json")
	cmd.Flags().StringVar(&status, "status", "", "Filter sessions by status: live or ended")
	cmd.Flags().StringVar(&projectID, "project", "", "List only one project's sessions")
	cmd.Flags().StringVar(&workspaceID, "workspace", "", "List only one workspace's sessions")
	cmd.MarkFlagsMutuallyExclusive("project", "workspace")
	return cmd
}

func sessionsShowCommand(lookup envLookup) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show one session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutput(output); err != nil {
				return err
			}
			client, err := commandAPIClient(cmd, lookup)
			if err != nil {
				return err
			}
			body, err := client.get(cmd.Context(), sessionEndpoint(args[0]))
			if err != nil {
				return err
			}
			if output == outputJSON {
				return writeRawJSON(cmd, body)
			}

			var resp api.SessionDetail
			if err := json.Unmarshal(body, &resp); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			return writeSessionDetailText(cmd, resp)
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", outputText, "Output format: text or json")
	return cmd
}

func sessionsRenameCommand(lookup envLookup) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "rename <id> <name>",
		Short: "Rename a session",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutput(output); err != nil {
				return err
			}
			client, err := commandAPIClient(cmd, lookup)
			if err != nil {
				return err
			}
			body, err := client.patch(cmd.Context(), sessionEndpoint(args[0]), map[string]any{
				"name": args[1],
			})
			if err != nil {
				return err
			}
			if output == outputJSON {
				return writeRawJSON(cmd, body)
			}

			var resp api.SessionDetail
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

func sessionsAttachCommand(lookup envLookup) *cobra.Command {
	return &cobra.Command{
		Use:   "attach <id>",
		Short: "Attach the current terminal to a live session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := commandAPIClient(cmd, lookup)
			if err != nil {
				return err
			}
			conn, err := client.dialAttach(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			defer conn.Close(websocket.StatusNormalClosure, "")
			return bridgeAttach(cmd.Context(), conn, os.Stdin, os.Stdout, os.Stdin.Fd(), os.Stdout.Fd())
		},
	}
}

func sessionsSendTextCommand(lookup envLookup) *cobra.Command {
	return resourceActionCommand(lookup, "send-text <id> <text>", "Inject text into a live session without submitting it", 2, func(id string, args []string) (string, any) {
		return sessionEndpoint(id, "send-text"), map[string]string{"text": args[1]}
	})
}

func sessionsSendKeyCommand(lookup envLookup) *cobra.Command {
	return resourceActionCommand(lookup, "send-key <id> <key>", "Inject a control key into a live session", 2, func(id string, args []string) (string, any) {
		return sessionEndpoint(id, "send-key"), map[string]string{"key": args[1]}
	})
}

func sessionsDeleteCommand(lookup envLookup) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a session (ends Live processes; removes Live or Ended records)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := commandAPIClient(cmd, lookup)
			if err != nil {
				return err
			}
			if _, err := client.delete(cmd.Context(), sessionEndpoint(args[0])); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted session %s. %s\n", args[0], filesNotTouched)
			return nil
		},
	}
}

func resourceActionCommand(lookup envLookup, use, short string, argCount int, build func(id string, args []string) (string, any)) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(argCount),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			endpoint, body := build(id, args)

			client, err := commandAPIClient(cmd, lookup)
			if err != nil {
				return err
			}
			if _, err := client.post(cmd.Context(), endpoint, body); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), id)
			return nil
		},
	}
}

// parseParams turns repeated key=value flags into a parameter map. Values are
// sent as strings; the service validates and types them per the action's spec.
func parseParams(pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(pairs))
	for _, p := range pairs {
		key, value, ok := strings.Cut(p, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid --param %q: expected key=value", p)
		}
		out[key] = value
	}
	return out, nil
}

func commandAPIClient(cmd *cobra.Command, lookup envLookup) (*apiClient, error) {
	cfg, err := resolveConfig(cmd, lookup)
	if err != nil {
		return nil, err
	}
	socketPath := resolvePaths(cfg, lookup).SocketPath
	return newAPIClientWithToken(socketPath, cfg.Auth.Token), nil
}

func validateOutput(output string) error {
	switch output {
	case outputText, outputJSON:
		return nil
	default:
		return fmt.Errorf("invalid output %q: want text or json", output)
	}
}

func validateSessionStatus(status string) error {
	switch status {
	case "", "live", "ended":
		return nil
	default:
		return fmt.Errorf("invalid session status %q: want live or ended", status)
	}
}

func writeRawJSON(cmd *cobra.Command, body []byte) error {
	if _, err := cmd.OutOrStdout().Write(body); err != nil {
		return err
	}
	if len(body) == 0 || body[len(body)-1] != '\n' {
		_, err := fmt.Fprintln(cmd.OutOrStdout())
		return err
	}
	return nil
}

func writeSessionDetailText(cmd *cobra.Command, s api.SessionDetail) error {
	params := "{}"
	if len(s.Params) > 0 {
		encoded, err := json.Marshal(s.Params)
		if err != nil {
			return fmt.Errorf("encode params: %w", err)
		}
		params = string(encoded)
	}

	// An empty action is the Interactive Shell; show that instead of a blank.
	action := s.Action
	if action == "" {
		action = "(interactive shell)"
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "id\t%s\n", s.ID)
	fmt.Fprintf(out, "status\t%s\n", s.Status)
	fmt.Fprintf(out, "action\t%s\n", action)
	fmt.Fprintf(out, "environment\t%s\n", s.Environment)
	fmt.Fprintf(out, "workingDir\t%s\n", s.WorkingDir)
	if s.Workspace != nil {
		fmt.Fprintf(out, "workspace\t%s\n", s.Workspace.ID)
		fmt.Fprintf(out, "workspaceName\t%s\n", s.Workspace.Name)
	}
	if s.Project != nil {
		fmt.Fprintf(out, "project\t%s\n", s.Project.ID)
		fmt.Fprintf(out, "projectName\t%s\n", s.Project.Name)
	}
	if s.Name != "" {
		fmt.Fprintf(out, "name\t%s\n", s.Name)
	}
	if s.Prompt != "" {
		fmt.Fprintf(out, "prompt\t%s\n", s.Prompt)
	}
	fmt.Fprintf(out, "params\t%s\n", params)
	fmt.Fprintf(out, "createdAt\t%s\n", s.CreatedAt)
	fmt.Fprintf(out, "updatedAt\t%s\n", s.UpdatedAt)
	return nil
}

func sessionEndpoint(id string, parts ...string) string {
	segments := append([]string{"sessions", url.PathEscape(id)}, parts...)
	return strings.Join(segments, "/")
}

type wsWrite struct {
	typ  websocket.MessageType
	data []byte
}

func bridgeAttach(ctx context.Context, conn *websocket.Conn, input io.Reader, output io.Writer, inputFD, outputFD uintptr) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if term.IsTerminal(int(inputFD)) {
		state, err := term.MakeRaw(int(inputFD))
		if err != nil {
			return fmt.Errorf("set terminal raw mode: %w", err)
		}
		defer term.Restore(int(inputFD), state)
	}

	writes := make(chan wsWrite, 16)
	errCh := make(chan error, 3)

	go func() {
		defer cancel()
		for {
			select {
			case msg := <-writes:
				if err := conn.Write(ctx, msg.typ, msg.data); err != nil {
					errCh <- err
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		defer cancel()
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				errCh <- normalizeAttachClose(err)
				return
			}
			if typ == websocket.MessageBinary {
				if _, err := output.Write(data); err != nil {
					errCh <- err
					return
				}
			}
		}
	}()

	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := input.Read(buf)
			if n > 0 {
				data := append([]byte(nil), buf[:n]...)
				select {
				case writes <- wsWrite{typ: websocket.MessageBinary, data: data}:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				if err == io.EOF {
					errCh <- nil
					return
				}
				errCh <- err
				return
			}
		}
	}()

	stopResize := notifyAttachResize(ctx, writes, outputFD)
	defer stopResize()

	select {
	case err := <-errCh:
		cancel()
		return err
	case <-ctx.Done():
		return nil
	}
}

func notifyAttachResize(ctx context.Context, writes chan<- wsWrite, outputFD uintptr) func() {
	send := func() {
		cols, rows, err := term.GetSize(int(outputFD))
		if err != nil || cols <= 0 || rows <= 0 {
			return
		}
		payload, err := json.Marshal(map[string]any{
			"type": "resize",
			"cols": cols,
			"rows": rows,
		})
		if err != nil {
			return
		}
		select {
		case writes <- wsWrite{typ: websocket.MessageText, data: payload}:
		case <-ctx.Done():
		default:
		}
	}
	send()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGWINCH)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-signals:
				send()
			case <-stop:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	return func() {
		signal.Stop(signals)
		close(stop)
		<-done
	}
}

func normalizeAttachClose(err error) error {
	if err == nil {
		return nil
	}
	var closeErr websocket.CloseError
	if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
		return nil
	}
	if errors.As(err, &closeErr) && closeErr.Code == websocket.StatusNormalClosure {
		return nil
	}
	return err
}
