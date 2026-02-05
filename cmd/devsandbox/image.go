package main

import (
	"encoding/json"
	"os/exec"
	"time"

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

// ImageInfo contains metadata about a Docker image.
type ImageInfo struct {
	ID        string    // Image digest
	CreatedAt time.Time // When the image was created
}

// getImageInfo returns info about a local Docker image.
// Returns nil if the image doesn't exist locally.
func getImageInfo(image string) (*ImageInfo, error) {
	cmd := exec.Command("docker", "image", "inspect", image,
		"--format", "{{json .}}")
	output, err := cmd.Output()
	if err != nil {
		// Image doesn't exist locally
		return nil, nil
	}

	// Parse the first element of the array
	var inspectData []struct {
		ID      string    `json:"Id"`
		Created time.Time `json:"Created"`
	}
	if err := json.Unmarshal(output, &inspectData); err != nil {
		return nil, err
	}
	if len(inspectData) == 0 {
		return nil, nil
	}

	return &ImageInfo{
		ID:        inspectData[0].ID,
		CreatedAt: inspectData[0].Created,
	}, nil
}

// getConfiguredImage returns the Docker image from config or the default.
func getConfiguredImage() string {
	cfg, _, _, err := config.LoadConfig()
	if err != nil {
		return isolator.DefaultImage
	}
	if cfg.Sandbox.Docker.Image != "" {
		return cfg.Sandbox.Docker.Image
	}
	return isolator.DefaultImage
}
