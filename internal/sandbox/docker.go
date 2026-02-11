package sandbox

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"strconv"
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
		return nil, fmt.Errorf("failed to list docker containers: %w", err)
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

// parseLabels parses Docker label string (format: "key1=val1,key2=val2") into map.
// Note: This format is ambiguous if values contain commas. Our labels
// (devsandbox.*) use simple values that don't contain commas.
func parseLabels(labelStr string) map[string]string {
	labels := make(map[string]string)
	if labelStr == "" {
		return labels
	}
	for _, pair := range strings.Split(labelStr, ",") {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) == 2 {
			labels[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return labels
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

// RemoveDockerContainer stops and removes a Docker container,
// along with any associated per-session network and optionally volumes.
func RemoveDockerContainer(containerName string, removeVolumes bool) error {
	// Read network label BEFORE removing the container
	removeDockerNetworks(containerName)

	// Remove volumes BEFORE container removal (need container to exist for inspect)
	if removeVolumes {
		if err := removeContainerVolumes(containerName); err != nil {
			log.Printf("warning: failed to remove volumes for %s: %v", containerName, err)
		}
	}

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

// removeDockerNetworks removes the per-session Docker network for a container.
// Reads the network name from the container's labels.
func removeDockerNetworks(containerName string) {
	cmd := exec.Command("docker", "inspect", "--format",
		`{{index .Config.Labels "devsandbox.network_name"}}`, containerName)
	output, err := cmd.Output()
	if err != nil {
		return // Container may already be removed
	}
	networkName := strings.TrimSpace(string(output))
	if networkName == "" {
		return
	}

	rmNet := exec.Command("docker", "network", "rm", networkName)
	if rmOutput, err := rmNet.CombinedOutput(); err != nil {
		outStr := string(rmOutput)
		if !strings.Contains(outStr, "not found") && !strings.Contains(outStr, "No such") {
			log.Printf("warning: failed to remove network %s: %s", networkName, outStr)
		}
	}
}

// removeContainerVolumes inspects a container's mounts and removes
// devsandbox-prefixed volumes, skipping the shared cache volume.
func removeContainerVolumes(containerName string) error {
	volumes := GetContainerVolumes(containerName)
	if len(volumes) == 0 {
		return nil
	}

	var errs []string
	for _, vol := range volumes {
		if !strings.HasPrefix(vol, DockerVolumePrefix) {
			continue
		}
		// Skip the shared cache volume
		if vol == "devsandbox-cache" {
			continue
		}
		if err := RemoveDockerVolume(vol); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", vol, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("failed to remove volumes: %s", strings.Join(errs, "; "))
	}
	return nil
}

// GetContainerVolumes returns the names of Docker volumes mounted by a container.
func GetContainerVolumes(containerName string) []string {
	cmd := exec.Command("docker", "inspect", "--format",
		`{{ range .Mounts }}{{ if eq .Type "volume" }}{{ println .Name }}{{ end }}{{ end }}`,
		containerName)
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	var volumes []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			volumes = append(volumes, line)
		}
	}
	return volumes
}

// GetDockerVolumeSizes returns a map of devsandbox volume names to their sizes in bytes.
// Parses the output of `docker system df -v`. Returns an empty map on any failure.
func GetDockerVolumeSizes() map[string]int64 {
	sizes := make(map[string]int64)

	cmd := exec.Command("docker", "system", "df", "-v")
	output, err := cmd.Output()
	if err != nil {
		return sizes
	}

	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	inVolumes := false
	headerSkipped := false

	for scanner.Scan() {
		line := scanner.Text()

		// Look for the volumes section
		if strings.HasPrefix(line, "Local Volumes space usage:") {
			inVolumes = true
			headerSkipped = false
			continue
		}

		// Detect the end of volumes section (next section or empty line after data)
		if inVolumes && (strings.HasPrefix(line, "Build cache") || strings.HasPrefix(line, "Images space") || strings.HasPrefix(line, "Containers space")) {
			break
		}

		if !inVolumes {
			continue
		}

		// Skip the column header line
		if !headerSkipped {
			if strings.Contains(line, "VOLUME NAME") || strings.Contains(line, "LINKS") {
				headerSkipped = true
			}
			continue
		}

		// Skip empty lines
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// Parse volume line: VOLUME_NAME  LINKS  SIZE
		fields := strings.Fields(trimmed)
		if len(fields) < 3 {
			continue
		}

		volName := fields[0]
		if !strings.HasPrefix(volName, DockerVolumePrefix) {
			continue
		}

		sizeStr := fields[len(fields)-1]
		sizes[volName] = parseDockerSize(sizeStr)
	}

	return sizes
}

// parseDockerSize converts a Docker size string (e.g., "1.5GB", "250MB", "45.2kB", "0B")
// to bytes. Returns 0 for invalid or empty input.
func parseDockerSize(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}

	// Find where the numeric part ends and the unit begins
	unitIdx := -1
	for i, c := range s {
		if c != '.' && (c < '0' || c > '9') {
			unitIdx = i
			break
		}
	}

	if unitIdx <= 0 {
		// No unit found or empty numeric part
		return 0
	}

	numStr := s[:unitIdx]
	unit := s[unitIdx:]

	num, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0
	}

	var multiplier float64
	switch strings.ToUpper(unit) {
	case "B":
		multiplier = 1
	case "KB":
		multiplier = 1000
	case "MB":
		multiplier = 1000 * 1000
	case "GB":
		multiplier = 1000 * 1000 * 1000
	case "TB":
		multiplier = 1000 * 1000 * 1000 * 1000
	default:
		return 0
	}

	return int64(math.Round(num * multiplier))
}

// RemoveSandboxByType removes a sandbox based on its isolation type.
// When removeVolumes is true, Docker sandbox volumes are also removed.
func RemoveSandboxByType(m *Metadata, removeVolumes bool) error {
	if m.Isolation == IsolationDocker {
		return RemoveDockerContainer(m.SandboxRoot, removeVolumes)
	}
	return RemoveSandbox(m.SandboxRoot)
}
