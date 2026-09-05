package main

import (
	"github.com/spf13/cobra"

	"github.com/jeremytondo/atc/internal/service"
	"github.com/jeremytondo/atc/internal/webhooks/receiver"
)

// newWebhookReceiverCmd is the hidden receiver subcommand (ATC-306): the
// restricted process the server runs to terminate public webhook traffic.
// Its arguments come from the server; the flags exist so the receiver
// stays one binary with one command tree.
func newWebhookReceiverCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    receiver.Command,
		Hidden: true,
		Short:  "Internal: restricted webhook receiver",
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			flags := cmd.Flags()
			var opts receiver.Options
			var err error
			if opts.ChannelConns, err = flags.GetInt("channel-conns"); err != nil {
				return err
			}
			if opts.DenyPort, err = flags.GetInt("deny-port"); err != nil {
				return err
			}
			if opts.ProbePath, err = flags.GetString("probe"); err != nil {
				return err
			}
			if opts.Restricted, err = flags.GetBool("restricted"); err != nil {
				return err
			}
			if opts.ABI, err = flags.GetInt("abi"); err != nil {
				return err
			}
			if code := receiver.Main(opts, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()); code != 0 {
				return &service.ExitError{Code: code}
			}
			return nil
		},
	}
	cmd.Flags().Int("channel-conns", 0, "number of inherited channel connections to the server")
	cmd.Flags().Int("deny-port", 0, "loopback port the receiver must be unable to reach or bind")
	cmd.Flags().String("probe", "", "credential file the receiver must be unable to read")
	cmd.Flags().Bool("restricted", false, "second stage: already restricted")
	cmd.Flags().Int("abi", 0, "Landlock ABI the first stage enforced")
	_ = cmd.MarkFlagRequired("channel-conns")
	return cmd
}
