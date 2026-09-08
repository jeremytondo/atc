package main

import (
	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/jeremytondo/atc/internal/service"
)

// newBootstrapCmd is the hidden plumbing an `atc --remote <target>` launch
// runs on the remote over ssh (ATC-316): start the supervised server if
// needed, wait for its tailnet exposure, and print exactly one JSON object
// on stdout. Lifecycle chatter goes to stderr so stdout stays the
// protocol; errors return through Cobra on stderr with a non-zero exit.
func newBootstrapCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "__bootstrap",
		Hidden: true,
		Short:  "Internal: report this machine's server to a remote picker",
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := lifecycleOptions(cmd)
			if err != nil {
				return err
			}
			opts.Stdout = cmd.ErrOrStderr()
			bootstrap, err := service.Bootstrap(cmd.Context(), opts)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(bootstrap)
		},
	}
}
