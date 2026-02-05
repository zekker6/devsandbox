package main

import (
	"github.com/spf13/cobra"
)

func newImageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Manage Docker images",
		Long:  "Commands for managing the Docker image used by devsandbox.",
	}

	cmd.AddCommand(newImagePullCmd())

	return cmd
}

func newImagePullCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "pull",
		Short:   "Pull the latest Docker image",
		Long:    "Pull the configured Docker image and report what changed.",
		Example: `  devsandbox image pull`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil // Placeholder
		},
	}

	return cmd
}
