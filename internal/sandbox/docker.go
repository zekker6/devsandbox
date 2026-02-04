package sandbox

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DockerVolumePrefix is the prefix used for devsandbox Docker volumes
const DockerVolumePrefix = "devsandbox-"

// dockerVolume represents a Docker volume from `docker volume ls`
type dockerVolume struct {
	Name       string            `json:"Name"`
	CreatedAt  string            `json:"CreatedAt"`
	Labels     map[string]string `json:"Labels"`
	Mountpoint string            `json:"Mountpoint"`
}

// ListDockerSandboxes returns metadata for all devsandbox Docker volumes
func ListDockerSandboxes() ([]*Metadata, error) {
	// Check if docker is available
	_, err := exec.LookPath("docker")
	if err != nil {
		return nil, nil // Docker not installed, return empty
	}

	// List volumes with devsandbox prefix
	cmd := exec.Command("docker", "volume", "ls",
		"--filter", "name="+DockerVolumePrefix,
		"--format", "{{json .}}")

	output, err := cmd.Output()
	if err != nil {
		// Docker daemon not running or other error
		return nil, nil
	}

	var sandboxes []*Metadata

	// Parse JSON lines output
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}

		var vol dockerVolume
		if err := json.Unmarshal([]byte(line), &vol); err != nil {
			continue
		}

		// Get volume details for creation time
		createdAt := time.Now() // Default
		inspectCmd := exec.Command("docker", "volume", "inspect", vol.Name,
			"--format", "{{.CreatedAt}}")
		if inspectOutput, err := inspectCmd.Output(); err == nil {
			if t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(inspectOutput))); err == nil {
				createdAt = t
			}
		}

		// Extract project info from labels if available
		projectDir := "(docker volume)"
		projectName := vol.Name
		if dir, ok := vol.Labels["devsandbox.project_dir"]; ok {
			projectDir = dir
		}
		if name, ok := vol.Labels["devsandbox.project_name"]; ok {
			projectName = name
		}

		m := &Metadata{
			Name:        projectName,
			ProjectDir:  projectDir,
			CreatedAt:   createdAt,
			LastUsed:    createdAt, // Docker volumes don't track last used
			Shell:       ShellBash,
			Isolation:   IsolationDocker,
			SandboxRoot: vol.Name, // Use volume name as identifier
		}

		sandboxes = append(sandboxes, m)
	}

	return sandboxes, nil
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

// RemoveSandboxByType removes a sandbox based on its isolation type
func RemoveSandboxByType(m *Metadata) error {
	if m.Isolation == IsolationDocker {
		return RemoveDockerVolume(m.SandboxRoot)
	}
	return RemoveSandbox(m.SandboxRoot)
}
