package main

import (
	"encoding/json"
	"fmt"
	"os"
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
			image := getConfiguredImage()

			// Check if docker is available
			if _, err := exec.LookPath("docker"); err != nil {
				return fmt.Errorf("docker CLI not found: %w", err)
			}

			// Get current image info (if exists)
			beforeInfo, err := getImageInfo(image)
			if err != nil {
				return fmt.Errorf("failed to inspect image: %w", err)
			}

			// Pull the image
			fmt.Printf("Pulling %s...\n", image)
			pullCmd := exec.Command("docker", "pull", image)
			pullCmd.Stdout = os.Stdout
			pullCmd.Stderr = os.Stderr
			if err := pullCmd.Run(); err != nil {
				return fmt.Errorf("failed to pull image: %w", err)
			}

			// Get new image info
			afterInfo, err := getImageInfo(image)
			if err != nil {
				return fmt.Errorf("failed to inspect pulled image: %w", err)
			}
			if afterInfo == nil {
				return fmt.Errorf("image not found after pull")
			}

			// Report result
			if beforeInfo == nil {
				fmt.Println("Downloaded: image was not present locally")
			} else if beforeInfo.ID == afterInfo.ID {
				fmt.Println("Already up to date")
			} else {
				age := time.Since(beforeInfo.CreatedAt)
				fmt.Printf("Updated: image was %s old\n", formatAge(age))
			}

			return nil
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
// Returns (nil, nil) if the image doesn't exist locally.
// Returns (nil, error) for other failures (e.g., docker daemon not running).
func getImageInfo(image string) (*ImageInfo, error) {
	cmd := exec.Command("docker", "image", "inspect", image,
		"--format", "{{json .}}")
	output, err := cmd.Output()
	if err != nil {
		// Docker returns exit code 1 when image is not found
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		// Other errors (daemon not running, permissions, etc.)
		return nil, err
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

// formatAge formats a duration as a human-readable age string.
func formatAge(d time.Duration) string {
	days := int(d.Hours() / 24)
	if days > 0 {
		if days == 1 {
			return "1 day"
		}
		return fmt.Sprintf("%d days", days)
	}
	hours := int(d.Hours())
	if hours > 0 {
		if hours == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", hours)
	}
	minutes := int(d.Minutes())
	if minutes > 1 {
		return fmt.Sprintf("%d minutes", minutes)
	}
	if minutes == 1 {
		return "1 minute"
	}
	return "less than a minute"
}
