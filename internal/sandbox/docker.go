package sandbox

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// DockerVolumePrefix is the prefix used for devsandbox Docker volumes
const DockerVolumePrefix = "devsandbox-"

// ListDockerSandboxes returns metadata for all devsandbox Docker containers.
func ListDockerSandboxes() ([]*Metadata, error) {
	_, err := exec.LookPath("docker")
	if err != nil {
		return nil, nil
	}

	// List containers with devsandbox label
	cmd := exec.Command("docker", "ps", "-a",
		"--filter", "label=devsandbox=true",
		"--format", "{{json .}}")

	output, err := cmd.Output()
	if err != nil {
		return nil, nil
	}

	var sandboxes []*Metadata

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}

		var container struct {
			ID      string `json:"ID"`
			Names   string `json:"Names"`
			State   string `json:"State"`
			Status  string `json:"Status"`
			Labels  string `json:"Labels"`
			Created string `json:"CreatedAt"`
		}
		if err := json.Unmarshal([]byte(line), &container); err != nil {
			continue
		}

		// Parse labels
		labels := parseLabels(container.Labels)

		projectDir := labels["devsandbox.project_dir"]
		if projectDir == "" {
			projectDir = "(unknown)"
		}
		projectName := labels["devsandbox.project_name"]
		if projectName == "" {
			projectName = container.Names
		}

		// Parse creation time
		createdAt := time.Now()
		if t, err := time.Parse("2006-01-02 15:04:05 -0700 MST", container.Created); err == nil {
			createdAt = t
		}

		// Check if orphaned
		orphaned := false
		if projectDir != "(unknown)" {
			if _, err := os.Stat(projectDir); os.IsNotExist(err) {
				orphaned = true
			}
		}

		m := &Metadata{
			Name:        projectName,
			ProjectDir:  projectDir,
			CreatedAt:   createdAt,
			LastUsed:    createdAt,
			Shell:       ShellBash,
			Isolation:   IsolationDocker,
			SandboxRoot: container.Names, // Container name for removal
			State:       container.State,
			Orphaned:    orphaned,
		}

		sandboxes = append(sandboxes, m)
	}

	return sandboxes, nil
}

// parseLabels parses Docker label string into map.
func parseLabels(labelStr string) map[string]string {
	labels := make(map[string]string)
	for _, pair := range strings.Split(labelStr, ",") {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) == 2 {
			labels[parts[0]] = parts[1]
		}
	}
	return labels
}

// GetDockerVolumeSize returns the size of a Docker volume
func GetDockerVolumeSize(volumeName string) (int64, error) {
	// Docker doesn't provide easy volume size, use docker system df
	cmd := exec.Command("docker", "system", "df", "-v", "--format", "{{json .}}")
	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	// Parse and find the volume - this is complex, return 0 for now
	// Volume sizes are reported in `docker system df -v` but format is tricky
	_ = output
	return 0, nil
}

// RemoveDockerVolume removes a Docker volume
func RemoveDockerVolume(volumeName string) error {
	cmd := exec.Command("docker", "volume", "rm", volumeName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to remove volume %s: %s", volumeName, string(output))
	}
	return nil
}

// RemoveDockerContainer stops and removes a Docker container.
func RemoveDockerContainer(containerName string) error {
	// Stop if running
	stopCmd := exec.Command("docker", "stop", containerName)
	_ = stopCmd.Run() // Ignore error if already stopped

	// Remove container
	rmCmd := exec.Command("docker", "rm", containerName)
	output, err := rmCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to remove container %s: %s", containerName, string(output))
	}
	return nil
}

// RemoveSandboxByType removes a sandbox based on its isolation type
func RemoveSandboxByType(m *Metadata) error {
	if m.Isolation == IsolationDocker {
		return RemoveDockerContainer(m.SandboxRoot)
	}
	return RemoveSandbox(m.SandboxRoot)
}
