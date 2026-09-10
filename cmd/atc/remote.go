package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jeremytondo/atc/internal/config"
	"github.com/jeremytondo/atc/internal/paths"
	"github.com/jeremytondo/atc/internal/service"
)

// newRemoteCmd is the hidden plumbing an `atc --remote <target>` launch
// runs on the target over ssh (ATC-316, ATC-325): inspect reports the
// facts guided setup decides on, configure applies the one approved
// configuration change, and bootstrap starts or restarts the supervised
// server, waits for its tailnet exposure, and prints the connection
// material. Each prints at most one JSON object on stdout; lifecycle
// chatter goes to stderr so stdout stays the protocol; errors return
// through Cobra on stderr with a non-zero exit.
func newRemoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "__remote",
		Hidden: true,
		Short:  "Internal: the remote side of atc --remote",
		Args:   cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("usage: atc __remote <inspect|configure|bootstrap>")
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "inspect",
		Short: "Internal: report this executable, the server, and the tailnet",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := lifecycleOptions(cmd)
			if err != nil {
				return err
			}
			opts.Stdout = cmd.ErrOrStderr()
			inspection, err := service.Inspect(cmd.Context(), opts)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(inspection)
		},
	})
	configure := &cobra.Command{
		Use:   "configure",
		Short: "Internal: apply an approved configuration change",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			enable, err := cmd.Flags().GetBool("tailscale")
			if err != nil {
				return err
			}
			if !enable {
				return fmt.Errorf("nothing to configure: pass --tailscale")
			}
			configPath, err := paths.ConfigFile()
			if err != nil {
				return err
			}
			return config.EnableTailscale(configPath)
		},
	}
	configure.Flags().Bool("tailscale", false, "set tailscale = true in config.toml, keeping everything else")
	bootstrap := &cobra.Command{
		Use:   "bootstrap",
		Short: "Internal: report this machine's server to a remote picker",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := exposureLifecycleOptions(cmd)
			if err != nil {
				return err
			}
			restart, err := cmd.Flags().GetBool("restart")
			if err != nil {
				return err
			}
			opts.Stdout = cmd.ErrOrStderr()
			bootstrap, err := service.Bootstrap(cmd.Context(), opts, restart)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(bootstrap)
		},
	}
	// The exposure flags carry the same tri-state semantics as start and
	// restart; --restart is the approved bounce.
	addExposureFlags(bootstrap)
	bootstrap.Flags().Bool("restart", false, "restart a running server (approved by the person launching)")
	cmd.AddCommand(configure, bootstrap)
	return cmd
}
