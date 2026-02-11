package main

import (
	"fmt"
	"os"
	"os/exec"

	"devsandbox/internal/config"
	"devsandbox/internal/isolator"

	"github.com/spf13/cobra"
)

func newImageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Manage Docker images",
		Long:  "Commands for managing the Docker image used by devsandbox.",
	}

	cmd.AddCommand(newImageBuildCmd())

	return cmd
}

func newImageBuildCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "build",
		Short:   "Build the Docker image from Dockerfile",
		Long:    "Build the sandbox Docker image from the configured Dockerfile.",
		Example: `  devsandbox image build`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := exec.LookPath("docker"); err != nil {
				return fmt.Errorf("docker CLI not found: %w", err)
			}

			cfg, _, _, err := config.LoadConfig()
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			projectDir, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("failed to get working directory: %w", err)
			}

			dockerCfg := isolator.DockerConfig{
				Dockerfile: cfg.Sandbox.Docker.Dockerfile,
				ConfigDir:  config.ConfigDir(),
			}
			iso := isolator.NewDockerIsolator(dockerCfg)

			imageTag, err := iso.ResolveAndBuild(cmd.Context(), projectDir)
			if err != nil {
				return err
			}

			fmt.Printf("Successfully built image: %s\n", imageTag)
			return nil
		},
	}

	return cmd
}
