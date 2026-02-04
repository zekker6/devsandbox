# Persistent Docker Containers Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Keep Docker containers stopped instead of removing them, enabling fast restarts (~1-2s vs ~5-10s).

**Architecture:** Add container lifecycle management to DockerIsolator. Check if container exists before creating. Use labels on containers/volumes for metadata. Update listing/pruning to handle containers.

**Tech Stack:** Go, Docker CLI, Labels for metadata

---

## Task 1: Add KeepContainer Config Option

**Files:**
- Modify: `internal/config/config.go:138-153`
- Modify: `internal/config/merge.go` (add merge for new field)
- Modify: `internal/isolator/docker.go:22-34`

**Step 1: Add KeepContainer to config.DockerConfig**

Edit `internal/config/config.go`, add after line 152:

```go
// DockerConfig contains Docker-specific sandbox settings.
type DockerConfig struct {
	// Image is the Docker image to use for the sandbox.
	// Defaults to the official devsandbox image.
	Image string `toml:"image"`

	// HideEnvFiles enables .env file hiding inside the container.
	// Defaults to true.
	HideEnvFiles *bool `toml:"hide_env_files"`

	// PullPolicy controls when to pull the image.
	// Values: "always", "missing" (default), "never"
	PullPolicy string `toml:"pull_policy"`

	// KeepContainer keeps the container after exit for fast restarts.
	// Defaults to true.
	KeepContainer *bool `toml:"keep_container"`

	// Resources contains container resource limits.
	Resources DockerResourcesConfig `toml:"resources"`
}
```

Add helper method after `IsHideEnvFilesEnabled`:

```go
// IsKeepContainerEnabled returns whether container persistence is enabled (defaults to true).
func (d DockerConfig) IsKeepContainerEnabled() bool {
	if d.KeepContainer == nil {
		return true
	}
	return *d.KeepContainer
}
```

**Step 2: Add KeepContainer to isolator.DockerConfig**

Edit `internal/isolator/docker.go`, update DockerConfig struct:

```go
// DockerConfig contains Docker-specific settings.
type DockerConfig struct {
	// Image is the Docker image to use.
	Image string
	// PullPolicy controls when to pull the image: "always", "missing", "never".
	PullPolicy string
	// HideEnvFiles enables .env file hiding in the container.
	HideEnvFiles bool
	// MemoryLimit is the memory limit (e.g., "4g").
	MemoryLimit string
	// CPULimit is the CPU limit (e.g., "2").
	CPULimit string
	// KeepContainer keeps the container after exit for fast restarts.
	KeepContainer bool
}
```

**Step 3: Add merge logic**

Edit `internal/config/merge.go`, add in the Docker merge section:

```go
if overlay.Sandbox.Docker.KeepContainer != nil {
	result.Sandbox.Docker.KeepContainer = overlay.Sandbox.Docker.KeepContainer
}
```

**Step 4: Run tests**

Run: `task test`
Expected: All tests pass

**Step 5: Commit**

```bash
git add internal/config/config.go internal/config/merge.go internal/isolator/docker.go
git commit -m "feat(docker): add keep_container config option"
```

---

## Task 2: Add Container Name and State Methods

**Files:**
- Modify: `internal/isolator/docker.go`
- Create: `internal/isolator/docker_test.go` (add tests)

**Step 1: Write test for containerName**

Add to `internal/isolator/docker_test.go`:

```go
func TestDockerIsolator_containerName(t *testing.T) {
	iso := NewDockerIsolator(DockerConfig{})

	// Same input should produce same output
	name1 := iso.containerName("/home/user/project")
	name2 := iso.containerName("/home/user/project")
	if name1 != name2 {
		t.Error("containerName should be deterministic")
	}

	// Different inputs should produce different outputs
	name3 := iso.containerName("/home/user/other")
	if name1 == name3 {
		t.Error("containerName should produce different names for different paths")
	}

	// Should have devsandbox prefix
	if !strings.HasPrefix(name1, "devsandbox-") {
		t.Errorf("Container name should have devsandbox prefix: %s", name1)
	}

	// Should include project name
	if !strings.Contains(name1, "project") {
		t.Errorf("Container name should include project name: %s", name1)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/isolator/... -run TestDockerIsolator_containerName -v`
Expected: FAIL - undefined: iso.containerName

**Step 3: Implement containerName**

Add to `internal/isolator/docker.go`:

```go
// containerName generates a Docker container name for the sandbox.
// Format: devsandbox-<project>-<hash>
func (d *DockerIsolator) containerName(projectDir string) string {
	projectName := filepath.Base(projectDir)
	hash := sha256.Sum256([]byte(projectDir))
	return fmt.Sprintf("devsandbox-%s-%x", projectName, hash[:4])
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/isolator/... -run TestDockerIsolator_containerName -v`
Expected: PASS

**Step 5: Write test for getContainerState**

Add to `internal/isolator/docker_test.go`:

```go
func TestDockerIsolator_getContainerState(t *testing.T) {
	iso := NewDockerIsolator(DockerConfig{})

	// Non-existent container should return not exists
	exists, running := iso.getContainerState("nonexistent-container-xyz")
	if exists {
		t.Error("Non-existent container should not exist")
	}
	if running {
		t.Error("Non-existent container should not be running")
	}
}
```

**Step 6: Run test to verify it fails**

Run: `go test ./internal/isolator/... -run TestDockerIsolator_getContainerState -v`
Expected: FAIL - undefined: iso.getContainerState

**Step 7: Implement getContainerState**

Add to `internal/isolator/docker.go`:

```go
// ContainerState represents the state of a Docker container.
type ContainerState int

const (
	ContainerNotExists ContainerState = iota
	ContainerStopped
	ContainerRunning
)

// getContainerState checks if a container exists and its state.
func (d *DockerIsolator) getContainerState(name string) (exists bool, running bool) {
	cmd := exec.Command("docker", "inspect", "--format", "{{.State.Running}}", name)
	output, err := cmd.Output()
	if err != nil {
		return false, false // Container doesn't exist
	}

	isRunning := strings.TrimSpace(string(output)) == "true"
	return true, isRunning
}
```

**Step 8: Run test to verify it passes**

Run: `go test ./internal/isolator/... -run TestDockerIsolator_getContainerState -v`
Expected: PASS

**Step 9: Run all tests**

Run: `task test`
Expected: All tests pass

**Step 10: Commit**

```bash
git add internal/isolator/docker.go internal/isolator/docker_test.go
git commit -m "feat(docker): add container name and state methods"
```

---

## Task 3: Add Docker Labels

**Files:**
- Modify: `internal/isolator/docker.go`

**Step 1: Add label constants**

Add to `internal/isolator/docker.go` after the const block:

```go
// Docker labels for devsandbox containers and volumes
const (
	LabelDevsandbox   = "devsandbox"
	LabelProjectDir   = "devsandbox.project_dir"
	LabelProjectName  = "devsandbox.project_name"
	LabelCreatedAt    = "devsandbox.created_at"
)
```

**Step 2: Create helper for label args**

Add to `internal/isolator/docker.go`:

```go
// buildLabels returns Docker label arguments for a container/volume.
func (d *DockerIsolator) buildLabels(projectDir string) []string {
	projectName := filepath.Base(projectDir)
	return []string{
		"--label", LabelDevsandbox + "=true",
		"--label", LabelProjectDir + "=" + projectDir,
		"--label", LabelProjectName + "=" + projectName,
		"--label", LabelCreatedAt + "=" + time.Now().Format(time.RFC3339),
	}
}
```

Add import for "time" if not present.

**Step 3: Run tests**

Run: `task test`
Expected: All tests pass

**Step 4: Commit**

```bash
git add internal/isolator/docker.go
git commit -m "feat(docker): add label constants and helper"
```

---

## Task 4: Refactor Build() for Container Lifecycle

**Files:**
- Modify: `internal/isolator/docker.go`
- Modify: `cmd/devsandbox/main.go`

**Step 1: Add BuildResult type**

Add to `internal/isolator/docker.go`:

```go
// DockerAction represents what docker command to run.
type DockerAction int

const (
	DockerActionCreate DockerAction = iota // Create new container then start
	DockerActionStart                      // Start existing stopped container
	DockerActionExec                       // Exec into running container
)

// DockerBuildResult contains the command to execute.
type DockerBuildResult struct {
	Action     DockerAction
	BinaryPath string
	Args       []string
}
```

**Step 2: Create buildCreateArgs helper**

Extract container creation args into a helper (refactor existing Build logic):

```go
// buildCreateArgs builds arguments for docker create.
func (d *DockerIsolator) buildCreateArgs(cfg *Config, containerName string) ([]string, error) {
	args := []string{"create"}

	// Container name
	args = append(args, "--name", containerName)

	// Interactive mode
	if cfg.Interactive {
		args = append(args, "-it")
	}

	args = append(args, "--hostname", "sandbox")

	// Pull policy
	args = append(args, "--pull", d.config.PullPolicy)

	// Labels
	args = append(args, d.buildLabels(cfg.ProjectDir)...)

	// User mapping
	args = append(args,
		"-e", fmt.Sprintf("HOST_UID=%d", os.Getuid()),
		"-e", fmt.Sprintf("HOST_GID=%d", os.Getgid()),
	)

	// Working directory
	args = append(args, "-w", cfg.ProjectDir)

	// Project mount
	args = append(args, "-v", cfg.ProjectDir+":"+cfg.ProjectDir)

	// Sandbox home
	if runtime.GOOS == "darwin" {
		volumeName := d.sandboxVolumeName(cfg.SandboxHome)
		args = append(args, "-v", volumeName+":/home/sandboxuser")
	} else {
		args = append(args, "-v", cfg.SandboxHome+":/home/sandboxuser")
	}

	// Tool bindings
	toolMounts, toolEnvVars := d.getToolBindings(cfg)
	for _, mount := range toolMounts {
		args = append(args, "-v", mount)
	}
	for _, env := range toolEnvVars {
		args = append(args, "-e", env)
	}

	// Standard environment variables
	args = append(args, "-e", "DEVSANDBOX=1")
	if cfg.ProjectDir != "" {
		projectName := filepath.Base(cfg.ProjectDir)
		args = append(args, "-e", "DEVSANDBOX_PROJECT="+projectName)
		args = append(args, "-e", "PROJECT_DIR="+cfg.ProjectDir)
	}
	args = append(args, "-e", "GOTOOLCHAIN=local")

	// XDG directories
	args = append(args, "-e", "XDG_CONFIG_HOME=/home/sandboxuser/.config")
	args = append(args, "-e", "XDG_DATA_HOME=/home/sandboxuser/.local/share")
	args = append(args, "-e", "XDG_CACHE_HOME=/home/sandboxuser/.cache")

	// User environment variables
	for k, v := range cfg.Environment {
		args = append(args, "-e", k+"="+v)
	}

	// .env hiding
	if cfg.HideEnvFiles {
		args = append(args, "-e", "HIDE_ENV_FILES=true")
	}

	// Linux host mapping
	if runtime.GOOS == "linux" {
		args = append(args, "--add-host", "host.docker.internal:host-gateway")
	}

	// Proxy mode
	if cfg.ProxyEnabled {
		args = append(args, "-e", "PROXY_MODE=true")
		args = append(args, "-e", "DEVSANDBOX_PROXY=1")
		proxyHost := cfg.ProxyHost
		if proxyHost == "" {
			proxyHost = d.proxyHost()
		}
		args = append(args, "-e", fmt.Sprintf("PROXY_HOST=%s", proxyHost))
		args = append(args, "-e", fmt.Sprintf("PROXY_PORT=%d", cfg.ProxyPort))
	}

	// Resource limits
	if d.config.MemoryLimit != "" {
		args = append(args, "--memory", d.config.MemoryLimit)
	}
	if d.config.CPULimit != "" {
		args = append(args, "--cpus", d.config.CPULimit)
	}

	// Read-only bindings
	for _, b := range cfg.Bindings {
		if _, err := os.Stat(b.Source); os.IsNotExist(err) {
			if b.Optional {
				continue
			}
			return nil, fmt.Errorf("binding source does not exist: %s", b.Source)
		}
		mount := b.Source + ":" + b.Dest
		if b.ReadOnly {
			mount += ":ro"
		}
		args = append(args, "-v", mount)
	}

	// Image
	args = append(args, d.config.Image)

	// Command
	if len(cfg.Command) > 0 {
		args = append(args, cfg.Command...)
	} else {
		args = append(args, cfg.Shell)
	}

	return args, nil
}
```

**Step 3: Update Build() method**

Replace the existing `Build()` method:

```go
// Build constructs the docker command based on container state.
func (d *DockerIsolator) Build(ctx context.Context, cfg *Config) (*DockerBuildResult, error) {
	dockerPath, err := exec.LookPath("docker")
	if err != nil {
		return nil, fmt.Errorf("docker CLI not found: %w", err)
	}

	containerName := d.containerName(cfg.ProjectDir)

	// Check if we should keep containers
	if !d.config.KeepContainer {
		// Old behavior: always create fresh container with --rm
		args, err := d.buildCreateArgs(cfg, containerName+"-tmp")
		if err != nil {
			return nil, err
		}
		// Replace "create" with "run --rm"
		args[0] = "run"
		args = append([]string{"run", "--rm"}, args[2:]...) // Skip "create" and "--name"
		return &DockerBuildResult{
			Action:     DockerActionCreate,
			BinaryPath: dockerPath,
			Args:       args,
		}, nil
	}

	// Check container state
	exists, running := d.getContainerState(containerName)

	if running {
		// Container is running - exec into it
		args := []string{"exec"}
		if cfg.Interactive {
			args = append(args, "-it")
		}
		args = append(args, containerName)
		if len(cfg.Command) > 0 {
			args = append(args, cfg.Command...)
		} else {
			args = append(args, cfg.Shell)
		}
		return &DockerBuildResult{
			Action:     DockerActionExec,
			BinaryPath: dockerPath,
			Args:       args,
		}, nil
	}

	if exists {
		// Container exists but stopped - start it
		args := []string{"start", "-ai", containerName}
		return &DockerBuildResult{
			Action:     DockerActionStart,
			BinaryPath: dockerPath,
			Args:       args,
		}, nil
	}

	// Container doesn't exist - create it
	args, err := d.buildCreateArgs(cfg, containerName)
	if err != nil {
		return nil, err
	}
	return &DockerBuildResult{
		Action:     DockerActionCreate,
		BinaryPath: dockerPath,
		Args:       args,
	}, nil
}
```

**Step 4: Update main.go runDockerSandbox**

Update `cmd/devsandbox/main.go` `runDockerSandbox` function:

```go
// runDockerSandbox executes the sandbox using Docker isolation.
func runDockerSandbox(cfg *sandbox.Config, iso *isolator.DockerIsolator, args []string) error {
	// Build isolator config
	isoCfg := &isolator.Config{
		ProjectDir:     cfg.ProjectDir,
		SandboxHome:    cfg.SandboxHome,
		HomeDir:        cfg.HomeDir,
		Shell:          string(cfg.Shell),
		ShellPath:      cfg.ShellPath,
		Command:        args,
		Interactive:    term.IsTerminal(int(os.Stdin.Fd())),
		ProxyEnabled:   cfg.ProxyEnabled,
		ProxyPort:      cfg.ProxyPort,
		Environment:    make(map[string]string),
		HideEnvFiles:   true,
		ToolsConfig:    cfg.ToolsConfig,
		OverlayEnabled: cfg.OverlayEnabled,
	}

	// Build command
	result, err := iso.Build(context.Background(), isoCfg)
	if err != nil {
		return err
	}

	// Handle different actions
	switch result.Action {
	case isolator.DockerActionCreate:
		if strings.Contains(strings.Join(result.Args, " "), "--rm") {
			// Fresh container with --rm, just run
			cmd := exec.Command(result.BinaryPath, result.Args...)
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			return cmd.Run()
		}

		// Create container first
		createCmd := exec.Command(result.BinaryPath, result.Args...)
		createCmd.Stdout = os.Stdout
		createCmd.Stderr = os.Stderr
		if err := createCmd.Run(); err != nil {
			return fmt.Errorf("failed to create container: %w", err)
		}

		// Then start it
		containerName := result.Args[2] // After "create" "--name"
		startCmd := exec.Command(result.BinaryPath, "start", "-ai", containerName)
		startCmd.Stdin = os.Stdin
		startCmd.Stdout = os.Stdout
		startCmd.Stderr = os.Stderr
		return startCmd.Run()

	case isolator.DockerActionStart, isolator.DockerActionExec:
		cmd := exec.Command(result.BinaryPath, result.Args...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	return nil
}
```

**Step 5: Update existing tests**

Update `TestDockerIsolator_Build_BasicArgs` in `internal/isolator/docker_test.go` to work with new return type:

```go
func TestDockerIsolator_Build_BasicArgs(t *testing.T) {
	iso := NewDockerIsolator(DockerConfig{Image: "test-image:latest", KeepContainer: false})

	cfg := &Config{
		ProjectDir:  "/tmp/test-project",
		SandboxHome: "/tmp/test-sandbox",
		HomeDir:     "/home/testuser",
		Shell:       "/bin/bash",
		Environment: map[string]string{"FOO": "bar"},
	}

	result, err := iso.Build(context.Background(), cfg)
	if err != nil {
		_, lookErr := exec.LookPath("docker")
		if lookErr != nil {
			t.Skip("Docker not installed")
		}
		t.Fatalf("Build failed: %v", err)
	}

	argsStr := strings.Join(result.Args, " ")

	if !strings.Contains(argsStr, "test-image:latest") {
		t.Error("Build args missing image")
	}
	if !strings.Contains(argsStr, "-w /tmp/test-project") {
		t.Error("Build args missing working directory")
	}
	if !strings.Contains(argsStr, "FOO=bar") {
		t.Error("Build args missing environment variable")
	}
}
```

**Step 6: Run tests**

Run: `task test`
Expected: All tests pass (may need to update more tests)

**Step 7: Run lint**

Run: `task lint`
Expected: No issues

**Step 8: Commit**

```bash
git add internal/isolator/docker.go cmd/devsandbox/main.go internal/isolator/docker_test.go
git commit -m "feat(docker): implement container lifecycle management"
```

---

## Task 5: Add --no-keep CLI Flag

**Files:**
- Modify: `cmd/devsandbox/main.go`

**Step 1: Add flag variable**

In `runCommand()`, add with other flag variables:

```go
var noKeepContainer bool
```

**Step 2: Add flag definition**

After other flag definitions:

```go
cmd.Flags().BoolVar(&noKeepContainer, "no-keep", false, "Don't keep Docker container after exit (fresh container each run)")
```

**Step 3: Apply flag override**

After CLI flag overrides section, add:

```go
// --no-keep overrides config
if cmd.Flags().Changed("no-keep") && noKeepContainer {
	// Will be passed to isolator
}
```

**Step 4: Pass to isolator**

When creating the DockerIsolator, check the flag:

```go
keepContainer := appCfg.Sandbox.Docker.IsKeepContainerEnabled()
if cmd.Flags().Changed("no-keep") && noKeepContainer {
	keepContainer = false
}

iso = isolator.NewDockerIsolator(isolator.DockerConfig{
	Image:         appCfg.Sandbox.Docker.Image,
	PullPolicy:    appCfg.Sandbox.Docker.PullPolicy,
	HideEnvFiles:  appCfg.Sandbox.Docker.IsHideEnvFilesEnabled(),
	MemoryLimit:   appCfg.Sandbox.Docker.Resources.Memory,
	CPULimit:      appCfg.Sandbox.Docker.Resources.CPUs,
	KeepContainer: keepContainer,
})
```

**Step 5: Run tests**

Run: `task test`
Expected: All tests pass

**Step 6: Commit**

```bash
git add cmd/devsandbox/main.go
git commit -m "feat(cli): add --no-keep flag for fresh containers"
```

---

## Task 6: Update Sandbox Listing for Containers

**Files:**
- Modify: `internal/sandbox/docker.go`
- Modify: `internal/sandbox/metadata.go`

**Step 1: Add State field to Metadata**

Edit `internal/sandbox/metadata.go`, add to Metadata struct:

```go
// Metadata stores information about a sandbox instance
type Metadata struct {
	Name       string        `json:"name"`
	ProjectDir string        `json:"project_dir"`
	CreatedAt  time.Time     `json:"created_at"`
	LastUsed   time.Time     `json:"last_used"`
	Shell      Shell         `json:"shell"`
	Isolation  IsolationType `json:"isolation,omitempty"`
	// Computed fields (not persisted)
	SandboxRoot string `json:"-"`
	SizeBytes   int64  `json:"-"`
	Orphaned    bool   `json:"-"`
	State       string `json:"-"` // For Docker: "running", "stopped", "exited"
}
```

**Step 2: Rewrite ListDockerSandboxes to list containers**

Replace `ListDockerSandboxes` in `internal/sandbox/docker.go`:

```go
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
```

**Step 3: Update RemoveSandboxByType to handle containers**

```go
// RemoveSandboxByType removes a sandbox based on its isolation type.
func RemoveSandboxByType(m *Metadata) error {
	if m.Isolation == IsolationDocker {
		return RemoveDockerContainer(m.SandboxRoot)
	}
	return RemoveSandbox(m.SandboxRoot)
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
```

**Step 4: Add os import if missing**

Add `"os"` to imports in `internal/sandbox/docker.go`.

**Step 5: Run tests**

Run: `task test`
Expected: All tests pass

**Step 6: Commit**

```bash
git add internal/sandbox/docker.go internal/sandbox/metadata.go
git commit -m "feat(sandboxes): list Docker containers with state"
```

---

## Task 7: Update Sandboxes List Command

**Files:**
- Modify: `cmd/devsandbox/sandboxes.go`

**Step 1: Update printTable to show State**

Update the `printTable` function:

```go
func printTable(sandboxes []*sandbox.Metadata, showSize bool) error {
	table := tablewriter.NewWriter(os.Stdout)

	if showSize {
		table.Header("NAME", "TYPE", "STATE", "PROJECT DIR", "CREATED", "LAST USED", "SIZE", "STATUS")
	} else {
		table.Header("NAME", "TYPE", "STATE", "PROJECT DIR", "CREATED", "LAST USED", "STATUS")
	}

	for _, s := range sandboxes {
		status := ""
		if s.Orphaned {
			status = "orphaned"
		}

		projectDir := s.ProjectDir
		if len(projectDir) > 40 {
			projectDir = "..." + projectDir[len(projectDir)-37:]
		}

		isoType := string(s.Isolation)
		if isoType == "" {
			isoType = "bwrap"
		}

		state := "-"
		if s.Isolation == sandbox.IsolationDocker {
			state = s.State
			if state == "" {
				state = "unknown"
			}
		}

		sizeStr := sandbox.FormatSize(s.SizeBytes)
		if s.Isolation == sandbox.IsolationDocker {
			sizeStr = "-"
		}

		if showSize {
			_ = table.Append(
				s.Name,
				isoType,
				state,
				projectDir,
				s.CreatedAt.Format("2006-01-02"),
				s.LastUsed.Format("2006-01-02"),
				sizeStr,
				status,
			)
		} else {
			_ = table.Append(
				s.Name,
				isoType,
				state,
				projectDir,
				s.CreatedAt.Format("2006-01-02"),
				s.LastUsed.Format("2006-01-02"),
				status,
			)
		}
	}

	return table.Render()
}
```

**Step 2: Run tests**

Run: `task test`
Expected: All tests pass

**Step 3: Commit**

```bash
git add cmd/devsandbox/sandboxes.go
git commit -m "feat(sandboxes): show container state in listing"
```

---

## Task 8: Update Documentation

**Files:**
- Modify: `docs/configuration.md`

**Step 1: Add keep_container documentation**

Add to the Docker configuration section:

```markdown
### Container Persistence

By default, devsandbox keeps Docker containers after exit for fast restarts:

```toml
[sandbox.docker]
keep_container = true  # default
```

Benefits:
- Subsequent starts take ~1-2s instead of ~5-10s
- Installed packages and tools persist
- Shell history preserved

To disable (use fresh container each time):

```toml
[sandbox.docker]
keep_container = false
```

Or use the CLI flag for a one-off fresh container:

```bash
devsandbox --no-keep
```

### Managing Containers

List all sandboxes (including Docker containers):

```bash
devsandbox sandboxes list
```

Remove orphaned/stale containers:

```bash
devsandbox sandboxes prune
```
```

**Step 2: Commit**

```bash
git add docs/configuration.md
git commit -m "docs: add container persistence documentation"
```

---

## Task 9: Final Integration Test

**Step 1: Build**

Run: `task build`
Expected: Binary builds successfully

**Step 2: Manual test - fresh container**

```bash
./bin/devsandbox --isolation docker --no-keep
# Should create fresh container, remove on exit
```

**Step 3: Manual test - persistent container**

```bash
./bin/devsandbox --isolation docker
# First run: creates container
# Exit and run again - should be fast (~1-2s)
```

**Step 4: Manual test - list**

```bash
./bin/devsandbox sandboxes list
# Should show container with state (stopped/running)
```

**Step 5: Run full test suite**

Run: `task test && task lint`
Expected: All pass

**Step 6: Final commit**

```bash
git add -A
git commit -m "feat(docker): complete persistent container implementation"
```

---

## Summary

Tasks completed:
1. Added `keep_container` config option
2. Added container name and state methods
3. Added Docker labels for metadata
4. Refactored Build() for container lifecycle
5. Added `--no-keep` CLI flag
6. Updated sandbox listing for containers
7. Updated list command to show state
8. Updated documentation
9. Integration testing

Total: 9 tasks, ~45 minutes estimated
