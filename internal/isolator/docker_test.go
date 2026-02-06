package isolator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerIsolator_Name(t *testing.T) {
	iso := NewDockerIsolator(DockerConfig{})
	if iso.Name() != BackendDocker {
		t.Errorf("Name() = %s, want %s", iso.Name(), BackendDocker)
	}
}

func TestDockerIsolator_DefaultConfig(t *testing.T) {
	iso := NewDockerIsolator(DockerConfig{})
	if iso.config.Dockerfile != "" {
		t.Errorf("Default Dockerfile = %s, want empty", iso.config.Dockerfile)
	}
	if iso.config.ConfigDir != "" {
		t.Errorf("Default ConfigDir = %s, want empty", iso.config.ConfigDir)
	}
}

func TestDockerIsolator_CustomConfig(t *testing.T) {
	cfg := DockerConfig{
		Dockerfile:   "/custom/Dockerfile",
		ConfigDir:    "/custom/config",
		HideEnvFiles: true,
		MemoryLimit:  "2g",
		CPULimit:     "1.5",
	}
	iso := NewDockerIsolator(cfg)

	if iso.config.Dockerfile != "/custom/Dockerfile" {
		t.Errorf("Dockerfile = %s, want /custom/Dockerfile", iso.config.Dockerfile)
	}
	if iso.config.ConfigDir != "/custom/config" {
		t.Errorf("ConfigDir = %s, want /custom/config", iso.config.ConfigDir)
	}
	if !iso.config.HideEnvFiles {
		t.Error("HideEnvFiles should be true")
	}
	if iso.config.MemoryLimit != "2g" {
		t.Errorf("MemoryLimit = %s, want 2g", iso.config.MemoryLimit)
	}
	if iso.config.CPULimit != "1.5" {
		t.Errorf("CPULimit = %s, want 1.5", iso.config.CPULimit)
	}
}

func TestDockerIsolator_Available(t *testing.T) {
	iso := NewDockerIsolator(DockerConfig{})
	err := iso.Available()

	// Check if docker is actually installed
	_, lookErr := exec.LookPath("docker")
	if lookErr != nil {
		if err == nil {
			t.Error("Available() should return error when docker not installed")
		}
		return
	}
	// Note: Even if docker CLI exists, daemon might not be running
	// So we can't assert success here - just check the error message is helpful
	if err != nil && !strings.Contains(err.Error(), "Docker") {
		t.Errorf("Error message should mention Docker: %v", err)
	}
}

func TestDockerIsolator_Build_BasicArgs(t *testing.T) {
	_, lookErr := exec.LookPath("docker")
	if lookErr != nil {
		t.Skip("Docker not installed")
	}

	configDir := t.TempDir()
	iso := NewDockerIsolator(DockerConfig{ConfigDir: configDir, KeepContainer: false})

	cfg := &Config{
		ProjectDir:  "/tmp/test-project",
		SandboxHome: "/tmp/test-sandbox",
		HomeDir:     "/home/testuser",
		Shell:       "/bin/bash",
		Environment: map[string]string{"FOO": "bar"},
	}

	result, err := iso.BuildDocker(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Verify action is DockerActionRun for KeepContainer=false
	if result.Action != DockerActionRun {
		t.Errorf("Expected DockerActionRun, got %v", result.Action)
	}

	// Verify key arguments are present
	argsStr := strings.Join(result.Args, " ")

	if !strings.Contains(argsStr, "devsandbox:local") {
		t.Error("Build args missing image tag")
	}
	// Working directory should match project directory (PWD consistency)
	if !strings.Contains(argsStr, "-w /tmp/test-project") {
		t.Error("Build args missing working directory matching project dir")
	}
	if !strings.Contains(argsStr, "FOO=bar") {
		t.Error("Build args missing environment variable")
	}
	if !strings.Contains(argsStr, "HOST_UID=") {
		t.Error("Build args missing HOST_UID")
	}
	if !strings.Contains(argsStr, "HOST_GID=") {
		t.Error("Build args missing HOST_GID")
	}
	// Should have --rm for non-persistent containers
	if !strings.Contains(argsStr, "--rm") {
		t.Error("Build args missing --rm for non-persistent container")
	}
}

func TestDockerIsolator_Build_WithProxy(t *testing.T) {
	_, lookErr := exec.LookPath("docker")
	if lookErr != nil {
		t.Skip("Docker not installed")
	}

	configDir := t.TempDir()
	iso := NewDockerIsolator(DockerConfig{ConfigDir: configDir, KeepContainer: false})

	cfg := &Config{
		ProjectDir:   "/tmp/test-project",
		SandboxHome:  "/tmp/test-sandbox",
		HomeDir:      "/home/testuser",
		Shell:        "/bin/bash",
		ProxyEnabled: true,
		ProxyPort:    8080,
	}

	result, err := iso.BuildDocker(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	argsStr := strings.Join(result.Args, " ")

	if !strings.Contains(argsStr, "PROXY_MODE=true") {
		t.Error("Build args missing PROXY_MODE")
	}
	if !strings.Contains(argsStr, "PROXY_HOST=") {
		t.Error("Build args missing PROXY_HOST")
	}
	if !strings.Contains(argsStr, "PROXY_PORT=8080") {
		t.Error("Build args missing PROXY_PORT")
	}
}

func TestDockerIsolator_Build_WithResourceLimits(t *testing.T) {
	_, lookErr := exec.LookPath("docker")
	if lookErr != nil {
		t.Skip("Docker not installed")
	}

	configDir := t.TempDir()
	iso := NewDockerIsolator(DockerConfig{
		ConfigDir:     configDir,
		MemoryLimit:   "4g",
		CPULimit:      "2",
		KeepContainer: false,
	})

	cfg := &Config{
		ProjectDir:  "/tmp/test-project",
		SandboxHome: "/tmp/test-sandbox",
		HomeDir:     "/home/testuser",
		Shell:       "/bin/bash",
	}

	result, err := iso.BuildDocker(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	argsStr := strings.Join(result.Args, " ")

	if !strings.Contains(argsStr, "--memory 4g") {
		t.Error("Build args missing memory limit")
	}
	if !strings.Contains(argsStr, "--cpus 2") {
		t.Error("Build args missing CPU limit")
	}
}

func TestDockerIsolator_Build_WithHideEnvFiles(t *testing.T) {
	_, lookErr := exec.LookPath("docker")
	if lookErr != nil {
		t.Skip("Docker not installed")
	}

	configDir := t.TempDir()
	iso := NewDockerIsolator(DockerConfig{ConfigDir: configDir, KeepContainer: false})

	cfg := &Config{
		ProjectDir:   "/tmp/test-project",
		SandboxHome:  "/tmp/test-sandbox",
		HomeDir:      "/home/testuser",
		Shell:        "/bin/bash",
		HideEnvFiles: true,
	}

	result, err := iso.BuildDocker(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	argsStr := strings.Join(result.Args, " ")

	if !strings.Contains(argsStr, "HIDE_ENV_FILES=true") {
		t.Error("Build args missing HIDE_ENV_FILES")
	}
}

func TestDockerIsolator_Build_WithCommand(t *testing.T) {
	_, lookErr := exec.LookPath("docker")
	if lookErr != nil {
		t.Skip("Docker not installed")
	}

	configDir := t.TempDir()
	iso := NewDockerIsolator(DockerConfig{ConfigDir: configDir, KeepContainer: false})

	cfg := &Config{
		ProjectDir:  "/tmp/test-project",
		SandboxHome: "/tmp/test-sandbox",
		HomeDir:     "/home/testuser",
		Shell:       "/bin/bash",
		Command:     []string{"echo", "hello"},
	}

	result, err := iso.BuildDocker(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Command should be at the end after the image tag
	argsStr := strings.Join(result.Args, " ")
	if !strings.Contains(argsStr, "devsandbox:local echo hello") {
		t.Error("Build args missing or misplaced command")
	}
}

func TestDockerIsolator_Build_BindingNotExists(t *testing.T) {
	_, lookErr := exec.LookPath("docker")
	if lookErr != nil {
		t.Skip("Docker not installed")
	}

	configDir := t.TempDir()
	iso := NewDockerIsolator(DockerConfig{ConfigDir: configDir, KeepContainer: false})

	cfg := &Config{
		ProjectDir:  "/tmp/test-project",
		SandboxHome: "/tmp/test-sandbox",
		HomeDir:     "/home/testuser",
		Shell:       "/bin/bash",
		Bindings: []Binding{
			{
				Source:   "/nonexistent/path",
				Dest:     "/container/path",
				Optional: false,
			},
		},
	}

	_, err := iso.BuildDocker(context.Background(), cfg)
	if err == nil {
		t.Error("Build should fail with non-existent required binding")
	}
	if !strings.Contains(err.Error(), "binding source does not exist") {
		t.Errorf("Error should mention binding source: %v", err)
	}
}

func TestDockerIsolator_Build_OptionalBindingNotExists(t *testing.T) {
	_, lookErr := exec.LookPath("docker")
	if lookErr != nil {
		t.Skip("Docker not installed")
	}

	configDir := t.TempDir()
	iso := NewDockerIsolator(DockerConfig{ConfigDir: configDir, KeepContainer: false})

	cfg := &Config{
		ProjectDir:  "/tmp/test-project",
		SandboxHome: "/tmp/test-sandbox",
		HomeDir:     "/home/testuser",
		Shell:       "/bin/bash",
		Bindings: []Binding{
			{
				Source:   "/nonexistent/path",
				Dest:     "/container/path",
				Optional: true,
			},
		},
	}

	_, err := iso.BuildDocker(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build should not fail with non-existent optional binding: %v", err)
	}
}

func TestDockerIsolator_Cleanup(t *testing.T) {
	iso := NewDockerIsolator(DockerConfig{})
	err := iso.Cleanup()
	if err != nil {
		t.Errorf("Cleanup() error: %v", err)
	}
}

func TestDockerIsolator_ImplementsInterface(t *testing.T) {
	var _ Isolator = (*DockerIsolator)(nil)
}

func TestDockerIsolator_proxyHost(t *testing.T) {
	iso := NewDockerIsolator(DockerConfig{})
	host := iso.proxyHost()

	// Should always return host.docker.internal (works on all platforms with --add-host)
	expected := "host.docker.internal"
	if host != expected {
		t.Errorf("proxyHost() = %s, want %s", host, expected)
	}
}

func TestDockerIsolator_sandboxVolumeName(t *testing.T) {
	iso := NewDockerIsolator(DockerConfig{})

	// Same input should produce same output
	name1 := iso.sandboxVolumeName("/tmp/test")
	name2 := iso.sandboxVolumeName("/tmp/test")
	if name1 != name2 {
		t.Error("sandboxVolumeName should be deterministic")
	}

	// Different inputs should produce different outputs
	name3 := iso.sandboxVolumeName("/tmp/other")
	if name1 == name3 {
		t.Error("sandboxVolumeName should produce different names for different paths")
	}

	// Should have devsandbox prefix
	if !strings.HasPrefix(name1, "devsandbox-") {
		t.Errorf("Volume name should have devsandbox prefix: %s", name1)
	}
}

func TestDockerIsolator_remapToContainerHome(t *testing.T) {
	iso := NewDockerIsolator(DockerConfig{})
	homeDir := "/home/testuser"

	tests := []struct {
		hostPath string
		expected string
	}{
		// Paths under home should be remapped
		{"/home/testuser/.config/fish", "/home/sandboxuser/.config/fish"},
		{"/home/testuser/.local/share/nvim", "/home/sandboxuser/.local/share/nvim"},
		{"/home/testuser/.bashrc", "/home/sandboxuser/.bashrc"},
		// Paths outside home should stay the same
		{"/etc/hosts", "/etc/hosts"},
		{"/tmp/project", "/tmp/project"},
		{"/usr/local/bin", "/usr/local/bin"},
	}

	for _, tt := range tests {
		result := iso.remapToContainerHome(tt.hostPath, homeDir)
		if result != tt.expected {
			t.Errorf("remapToContainerHome(%q) = %q, want %q", tt.hostPath, result, tt.expected)
		}
	}
}

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

func TestDockerIsolator_getContainerState(t *testing.T) {
	iso := NewDockerIsolator(DockerConfig{})

	// Non-existent container should return not exists
	exists, running := iso.getContainerState("nonexistent-container-xyz-123456")
	if exists {
		t.Error("Non-existent container should not exist")
	}
	if running {
		t.Error("Non-existent container should not be running")
	}
}

func TestDockerIsolator_BuildDocker_KeepContainer_Create(t *testing.T) {
	_, lookErr := exec.LookPath("docker")
	if lookErr != nil {
		t.Skip("Docker not installed")
	}

	// Test that with KeepContainer=true and no existing container,
	// the result is DockerActionCreate
	configDir := t.TempDir()
	iso := NewDockerIsolator(DockerConfig{
		ConfigDir:     configDir,
		KeepContainer: true,
	})

	cfg := &Config{
		ProjectDir:  "/tmp/test-project-unique-12345",
		SandboxHome: "/tmp/test-sandbox",
		HomeDir:     "/home/testuser",
		Shell:       "/bin/bash",
	}

	result, err := iso.BuildDocker(context.Background(), cfg)
	if err != nil {
		t.Fatalf("BuildDocker failed: %v", err)
	}

	// Should return DockerActionCreate since container doesn't exist
	if result.Action != DockerActionCreate {
		t.Errorf("Expected DockerActionCreate, got %v", result.Action)
	}

	// Container name should be set
	if result.ContainerName == "" {
		t.Error("ContainerName should be set for create action")
	}

	// Args should start with "create"
	if len(result.Args) == 0 || result.Args[0] != "create" {
		t.Error("Args should start with 'create'")
	}

	argsStr := strings.Join(result.Args, " ")

	// Should have --name with container name
	if !strings.Contains(argsStr, "--name") {
		t.Error("Args should have --name for persistent container")
	}

	// Should have labels
	if !strings.Contains(argsStr, "--label") {
		t.Error("Args should have labels for persistent container")
	}

	// Should NOT have --rm
	if strings.Contains(argsStr, "--rm") {
		t.Error("Args should NOT have --rm for persistent container")
	}
}

func TestDockerIsolator_BuildDocker_KeepContainer_Labels(t *testing.T) {
	_, lookErr := exec.LookPath("docker")
	if lookErr != nil {
		t.Skip("Docker not installed")
	}

	configDir := t.TempDir()
	iso := NewDockerIsolator(DockerConfig{
		ConfigDir:     configDir,
		KeepContainer: true,
	})

	cfg := &Config{
		ProjectDir:  "/tmp/test-label-project",
		SandboxHome: "/tmp/test-sandbox",
		HomeDir:     "/home/testuser",
		Shell:       "/bin/bash",
	}

	result, err := iso.BuildDocker(context.Background(), cfg)
	if err != nil {
		t.Fatalf("BuildDocker failed: %v", err)
	}

	argsStr := strings.Join(result.Args, " ")

	// Check all required labels are present
	if !strings.Contains(argsStr, LabelDevsandbox+"=true") {
		t.Error("Args missing devsandbox label")
	}
	if !strings.Contains(argsStr, LabelProjectDir+"=") {
		t.Error("Args missing project_dir label")
	}
	if !strings.Contains(argsStr, LabelProjectName+"=") {
		t.Error("Args missing project_name label")
	}
	if !strings.Contains(argsStr, LabelCreatedAt+"=") {
		t.Error("Args missing created_at label")
	}
}

func TestDockerIsolator_Build_CacheVolume(t *testing.T) {
	_, lookErr := exec.LookPath("docker")
	if lookErr != nil {
		t.Skip("Docker not installed")
	}

	configDir := t.TempDir()
	iso := NewDockerIsolator(DockerConfig{ConfigDir: configDir, KeepContainer: false})

	cfg := &Config{
		ProjectDir:  "/tmp/test-project",
		SandboxHome: "/tmp/test-sandbox",
		HomeDir:     "/home/testuser",
		Shell:       "/bin/bash",
	}

	result, err := iso.BuildDocker(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	argsStr := strings.Join(result.Args, " ")

	// Should have cache volume mount
	if !strings.Contains(argsStr, "-v devsandbox-cache:/cache") {
		t.Error("Build args missing cache volume mount")
	}

	// Should have mise cache env vars
	if !strings.Contains(argsStr, "MISE_DATA_DIR=/cache/mise") {
		t.Error("Build args missing MISE_DATA_DIR")
	}
	if !strings.Contains(argsStr, "MISE_CACHE_DIR=/cache/mise/cache") {
		t.Error("Build args missing MISE_CACHE_DIR")
	}

	// Should have go cache env vars
	if !strings.Contains(argsStr, "GOMODCACHE=/cache/go/mod") {
		t.Error("Build args missing GOMODCACHE")
	}
	if !strings.Contains(argsStr, "GOCACHE=/cache/go/build") {
		t.Error("Build args missing GOCACHE")
	}
}

func TestDockerIsolator_Build_InterfaceCompatibility(t *testing.T) {
	_, lookErr := exec.LookPath("docker")
	if lookErr != nil {
		t.Skip("Docker not installed")
	}

	// Test that the interface-compliant Build() method works
	configDir := t.TempDir()
	iso := NewDockerIsolator(DockerConfig{ConfigDir: configDir, KeepContainer: false})

	cfg := &Config{
		ProjectDir:  "/tmp/test-project",
		SandboxHome: "/tmp/test-sandbox",
		HomeDir:     "/home/testuser",
		Shell:       "/bin/bash",
	}

	binaryPath, args, err := iso.Build(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Should return docker path
	if binaryPath == "" {
		t.Error("BinaryPath should not be empty")
	}

	// Should return args
	if len(args) == 0 {
		t.Error("Args should not be empty")
	}

	// Args should contain "run" for non-persistent mode
	argsStr := strings.Join(args, " ")
	if !strings.Contains(argsStr, "run") {
		t.Error("Args should contain 'run' for non-persistent mode")
	}
}

func TestResolveDockerfile_Default(t *testing.T) {
	tmpDir := t.TempDir()
	d := &DockerIsolator{config: DockerConfig{}}

	path, err := d.resolveDockerfile("", tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := filepath.Join(tmpDir, "Dockerfile")
	if path != expected {
		t.Errorf("expected %q, got %q", expected, path)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read created Dockerfile: %v", err)
	}
	if !strings.Contains(string(content), "FROM ghcr.io/zekker6/devsandbox:latest") {
		t.Errorf("expected default FROM line, got %q", string(content))
	}
}

func TestResolveDockerfile_ConfigOverride(t *testing.T) {
	tmpDir := t.TempDir()
	customDockerfile := filepath.Join(tmpDir, "custom", "Dockerfile")
	if err := os.MkdirAll(filepath.Dir(customDockerfile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(customDockerfile, []byte("FROM ubuntu:22.04\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := &DockerIsolator{config: DockerConfig{Dockerfile: customDockerfile}}

	path, err := d.resolveDockerfile("", tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if path != customDockerfile {
		t.Errorf("expected %q, got %q", customDockerfile, path)
	}
}

func TestResolveDockerfile_RelativePath(t *testing.T) {
	tmpDir := t.TempDir()
	dockerfilePath := filepath.Join(tmpDir, "docker", "Dockerfile")
	if err := os.MkdirAll(filepath.Dir(dockerfilePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dockerfilePath, []byte("FROM ubuntu:22.04\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := &DockerIsolator{config: DockerConfig{Dockerfile: "docker/Dockerfile"}}

	path, err := d.resolveDockerfile(tmpDir, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if path != dockerfilePath {
		t.Errorf("expected %q, got %q", dockerfilePath, path)
	}
}

func TestResolveDockerfile_NotFound(t *testing.T) {
	d := &DockerIsolator{config: DockerConfig{Dockerfile: "/nonexistent/Dockerfile"}}

	_, err := d.resolveDockerfile("", t.TempDir())
	if err == nil {
		t.Error("expected error for non-existent Dockerfile")
	}
}

func TestDetermineImageTag_Global(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "config")
	dockerfilePath := filepath.Join(configDir, "Dockerfile")

	d := &DockerIsolator{config: DockerConfig{}}
	tag := d.determineImageTag(dockerfilePath, configDir, "/some/project")

	if tag != "devsandbox:local" {
		t.Errorf("expected 'devsandbox:local', got %q", tag)
	}
}

func TestDetermineImageTag_PerProject(t *testing.T) {
	d := &DockerIsolator{config: DockerConfig{}}
	tag := d.determineImageTag("/project/.docker/Dockerfile", "/global/config", "/home/user/myproject")

	if !strings.HasPrefix(tag, "devsandbox:myproject-") {
		t.Errorf("expected tag starting with 'devsandbox:myproject-', got %q", tag)
	}
}
