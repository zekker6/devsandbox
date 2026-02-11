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
		Dockerfile:  "/custom/Dockerfile",
		ConfigDir:   "/custom/config",
		MemoryLimit: "2g",
		CPULimit:    "1.5",
	}
	iso := NewDockerIsolator(cfg)

	if iso.config.Dockerfile != "/custom/Dockerfile" {
		t.Errorf("Dockerfile = %s, want /custom/Dockerfile", iso.config.Dockerfile)
	}
	if iso.config.ConfigDir != "/custom/config" {
		t.Errorf("ConfigDir = %s, want /custom/config", iso.config.ConfigDir)
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

// setupTestDockerfile creates a Dockerfile with a locally-available base image
// for integration tests that call BuildDocker() (which triggers docker build).
func setupTestDockerfile(t *testing.T) string {
	t.Helper()
	configDir := t.TempDir()
	dockerfilePath := filepath.Join(configDir, "Dockerfile")
	if err := os.WriteFile(dockerfilePath, []byte("FROM alpine:latest\n"), 0o644); err != nil {
		t.Fatalf("failed to create test Dockerfile: %v", err)
	}
	return configDir
}

func TestDockerIsolator_Build_BasicArgs(t *testing.T) {
	_, lookErr := exec.LookPath("docker")
	if lookErr != nil {
		t.Skip("Docker not installed")
	}

	configDir := setupTestDockerfile(t)
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

	configDir := setupTestDockerfile(t)
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

	configDir := setupTestDockerfile(t)
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

func TestDockerIsolator_Build_EnvFilesAlwaysHidden(t *testing.T) {
	_, lookErr := exec.LookPath("docker")
	if lookErr != nil {
		t.Skip("Docker not installed")
	}

	// Create a temp project dir with .env files
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, ".env"), []byte("SECRET=val"), 0o600); err != nil {
		t.Fatalf("failed to create .env file: %v", err)
	}

	configDir := setupTestDockerfile(t)
	iso := NewDockerIsolator(DockerConfig{ConfigDir: configDir, KeepContainer: false})

	cfg := &Config{
		ProjectDir:  projectDir,
		SandboxHome: "/tmp/test-sandbox",
		HomeDir:     "/home/testuser",
		Shell:       "/bin/bash",
	}

	result, err := iso.BuildDocker(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	argsStr := strings.Join(result.Args, " ")

	// Should have /dev/null volume mount for .env file (always-on)
	if !strings.Contains(argsStr, "/dev/null:") {
		t.Error("Build args missing /dev/null volume mount for .env hiding")
	}

	// Should NOT have SYS_ADMIN
	if strings.Contains(argsStr, "SYS_ADMIN") {
		t.Error("SYS_ADMIN capability should not be present")
	}
}

func TestDockerIsolator_Build_WithCommand(t *testing.T) {
	_, lookErr := exec.LookPath("docker")
	if lookErr != nil {
		t.Skip("Docker not installed")
	}

	configDir := setupTestDockerfile(t)
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

	configDir := setupTestDockerfile(t)
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

	configDir := setupTestDockerfile(t)
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
	projectDir := "/home/testuser/projects/myapp"

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
		// Paths under project directory should stay the same (project mounted at host path)
		{"/home/testuser/projects/myapp/.git", "/home/testuser/projects/myapp/.git"},
		{"/home/testuser/projects/myapp/.git/config", "/home/testuser/projects/myapp/.git/config"},
		{"/home/testuser/projects/myapp/src/main.go", "/home/testuser/projects/myapp/src/main.go"},
	}

	for _, tt := range tests {
		result := iso.remapToContainerHome(tt.hostPath, homeDir, projectDir)
		if result != tt.expected {
			t.Errorf("remapToContainerHome(%q) = %q, want %q", tt.hostPath, result, tt.expected)
		}
	}
}

func TestRemapToContainerHome_SimilarPrefix(t *testing.T) {
	iso := NewDockerIsolator(DockerConfig{})
	// /home/test should NOT match /home/testing
	result := iso.remapToContainerHome("/home/testing/.config", "/home/test", "/project")
	if result != "/home/testing/.config" {
		t.Errorf("should not remap /home/testing when homeDir is /home/test, got %s", result)
	}
}

func TestRemapToContainerHome_ExactMatch(t *testing.T) {
	iso := NewDockerIsolator(DockerConfig{})
	result := iso.remapToContainerHome("/home/test", "/home/test", "/project")
	if result != containerHome {
		t.Errorf("exact home match should remap to %s, got %s", containerHome, result)
	}
}

func TestRemapToContainerHome_SubPath(t *testing.T) {
	iso := NewDockerIsolator(DockerConfig{})
	result := iso.remapToContainerHome("/home/test/.config/nvim", "/home/test", "/project")
	expected := containerHome + "/.config/nvim"
	if result != expected {
		t.Errorf("expected %s, got %s", expected, result)
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
	exists, running := iso.getContainerState(context.Background(), "nonexistent-container-xyz-123456")
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
	configDir := setupTestDockerfile(t)
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

	configDir := setupTestDockerfile(t)
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

	configDir := setupTestDockerfile(t)
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

func TestDockerIsolator_IsolationType(t *testing.T) {
	iso := NewDockerIsolator(DockerConfig{})
	if iso.IsolationType() != "docker" {
		t.Errorf("IsolationType() = %s, want docker", iso.IsolationType())
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

func TestBuildCommonArgs_CapDrop(t *testing.T) {
	iso := NewDockerIsolator(DockerConfig{})
	iso.imageTag = "test:latest"

	cfg := &Config{
		ProjectDir:  "/tmp/test-project",
		SandboxHome: "/tmp/test-sandbox",
		HomeDir:     "/home/testuser",
		Shell:       "/bin/bash",
	}

	args, err := iso.buildCommonArgs(cfg)
	if err != nil {
		t.Fatalf("buildCommonArgs failed: %v", err)
	}

	argsStr := strings.Join(args, " ")

	if !strings.Contains(argsStr, "--cap-drop ALL") {
		t.Error("Expected --cap-drop ALL in args")
	}
	if !strings.Contains(argsStr, "--cap-add CHOWN") {
		t.Error("Expected --cap-add CHOWN in args")
	}
	if !strings.Contains(argsStr, "--cap-add SETUID") {
		t.Error("Expected --cap-add SETUID in args")
	}
	if !strings.Contains(argsStr, "--cap-add SETGID") {
		t.Error("Expected --cap-add SETGID in args")
	}
	if !strings.Contains(argsStr, "--security-opt no-new-privileges:true") {
		t.Error("Expected --security-opt no-new-privileges:true in args")
	}
}

func TestBuildCommonArgs_NoSysAdmin(t *testing.T) {
	iso := NewDockerIsolator(DockerConfig{})
	iso.imageTag = "test:latest"

	cfg := &Config{
		ProjectDir:  "/tmp/test-project",
		SandboxHome: "/tmp/test-sandbox",
		HomeDir:     "/home/testuser",
		Shell:       "/bin/bash",
	}

	args, err := iso.buildCommonArgs(cfg)
	if err != nil {
		t.Fatalf("buildCommonArgs failed: %v", err)
	}
	argsStr := strings.Join(args, " ")
	if strings.Contains(argsStr, "SYS_ADMIN") {
		t.Error("SYS_ADMIN should never be present")
	}
}

func TestBuildCommonArgs_Network(t *testing.T) {
	iso := NewDockerIsolator(DockerConfig{})
	iso.imageTag = "test:latest"
	iso.networkName = "devsandbox-net-abcd1234"

	cfg := &Config{
		ProjectDir:  "/tmp/test-project",
		SandboxHome: "/tmp/test-sandbox",
		HomeDir:     "/home/testuser",
		Shell:       "/bin/bash",
	}

	args, err := iso.buildCommonArgs(cfg)
	if err != nil {
		t.Fatalf("buildCommonArgs failed: %v", err)
	}

	argsStr := strings.Join(args, " ")
	if !strings.Contains(argsStr, "--network devsandbox-net-abcd1234") {
		t.Error("Expected --network flag with custom network name")
	}
}

func TestBuildCommonArgs_DefaultNetwork_WhenNoProxy(t *testing.T) {
	iso := NewDockerIsolator(DockerConfig{})
	iso.imageTag = "test:latest"
	// networkName is empty by default

	cfg := &Config{
		ProjectDir:  "/tmp/test-project",
		SandboxHome: "/tmp/test-sandbox",
		HomeDir:     "/home/testuser",
		Shell:       "/bin/bash",
	}

	args, err := iso.buildCommonArgs(cfg)
	if err != nil {
		t.Fatalf("buildCommonArgs failed: %v", err)
	}

	argsStr := strings.Join(args, " ")
	// Without proxy, use default Docker bridge networking (like bwrap without proxy).
	// --network=none would break all networking on macOS where Docker is the only backend.
	if strings.Contains(argsStr, "--network") {
		t.Error("Should not set any --network flag when proxy is disabled (use default bridge)")
	}
}

func TestBuildCommonArgs_EnvFilesAlwaysHidden(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, ".env"), []byte("SECRET=val"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".env.local"), []byte("OTHER=val"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "notenv.txt"), []byte("safe"), 0o644); err != nil {
		t.Fatal(err)
	}

	iso := NewDockerIsolator(DockerConfig{})
	iso.imageTag = "test:latest"

	cfg := &Config{
		ProjectDir:  projectDir,
		SandboxHome: "/tmp/test-sandbox",
		HomeDir:     "/home/testuser",
		Shell:       "/bin/bash",
	}
	args, err := iso.buildCommonArgs(cfg)
	if err != nil {
		t.Fatalf("buildCommonArgs failed: %v", err)
	}
	argsStr := strings.Join(args, " ")

	if strings.Contains(argsStr, "SYS_ADMIN") {
		t.Error("SYS_ADMIN capability should not be present")
	}

	if !strings.Contains(argsStr, "/dev/null") {
		t.Error("expected /dev/null volume mounts for .env files")
	}

	// Verify both .env files are mounted
	envCount := strings.Count(argsStr, "/dev/null:")
	if envCount < 2 {
		t.Errorf("expected at least 2 /dev/null mounts for .env files, got %d", envCount)
	}

	// Verify notenv.txt is NOT mounted with /dev/null
	if strings.Contains(argsStr, "notenv.txt") {
		t.Error("notenv.txt should not be hidden")
	}
}

func TestBuildCommonArgs_NestedEnvFilesHidden(t *testing.T) {
	projectDir := t.TempDir()
	subDir := filepath.Join(projectDir, "config")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".env"), []byte("ROOT=val"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, ".env.production"), []byte("PROD=val"), 0o600); err != nil {
		t.Fatal(err)
	}

	iso := NewDockerIsolator(DockerConfig{})
	iso.imageTag = "test:latest"

	cfg := &Config{
		ProjectDir:  projectDir,
		SandboxHome: "/tmp/test-sandbox",
		HomeDir:     "/home/testuser",
		Shell:       "/bin/bash",
	}
	args, err := iso.buildCommonArgs(cfg)
	if err != nil {
		t.Fatalf("buildCommonArgs failed: %v", err)
	}
	argsStr := strings.Join(args, " ")

	// Both root and nested .env files should be mounted
	envCount := strings.Count(argsStr, "/dev/null:")
	if envCount < 2 {
		t.Errorf("expected at least 2 /dev/null mounts (root + nested), got %d", envCount)
	}
}

func TestBuildCommonArgs_EnvFilesHiddenWithoutEnvFiles(t *testing.T) {
	// When no .env files exist, no /dev/null mounts should be added
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "safe.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	iso := NewDockerIsolator(DockerConfig{})
	iso.imageTag = "test:latest"

	cfg := &Config{
		ProjectDir:  projectDir,
		SandboxHome: "/tmp/test-sandbox",
		HomeDir:     "/home/testuser",
		Shell:       "/bin/bash",
	}
	args, err := iso.buildCommonArgs(cfg)
	if err != nil {
		t.Fatalf("buildCommonArgs failed: %v", err)
	}
	argsStr := strings.Join(args, " ")

	if strings.Contains(argsStr, "/dev/null:") {
		t.Error("no /dev/null mounts expected when no .env files exist")
	}
}

func TestGetToolBindings_DockerHostRemapped(t *testing.T) {
	_, lookErr := exec.LookPath("docker")
	if lookErr != nil {
		t.Skip("Docker not installed")
	}
	// Check if Docker socket exists
	if _, err := os.Stat("/run/docker.sock"); err != nil {
		t.Skip("Docker socket not available")
	}

	iso := NewDockerIsolator(DockerConfig{})

	cfg := &Config{
		ProjectDir:  "/tmp/test-project",
		SandboxHome: "/tmp/test-sandbox",
		HomeDir:     "/home/testuser",
		Shell:       "/bin/bash",
		ToolsConfig: map[string]any{
			"docker": map[string]any{
				"enabled": true,
			},
		},
	}

	_, envVars := iso.getToolBindings(cfg)

	// Find DOCKER_HOST in env vars
	var dockerHost string
	for _, env := range envVars {
		if strings.HasPrefix(env, "DOCKER_HOST=") {
			dockerHost = strings.TrimPrefix(env, "DOCKER_HOST=")
			break
		}
	}

	if dockerHost == "" {
		t.Fatal("DOCKER_HOST environment variable not found in tool bindings")
	}

	// DOCKER_HOST should point to /home/sandboxuser, not host homeDir
	if strings.Contains(dockerHost, "/home/testuser") {
		t.Errorf("DOCKER_HOST should not contain host homeDir, got: %s", dockerHost)
	}
	if !strings.Contains(dockerHost, "/home/sandboxuser/docker.sock") {
		t.Errorf("DOCKER_HOST should point to /home/sandboxuser/docker.sock, got: %s", dockerHost)
	}
}

func TestBuildCommonArgs_EnvSorted(t *testing.T) {
	iso := NewDockerIsolator(DockerConfig{})
	iso.imageTag = "test:latest"

	cfg := &Config{
		ProjectDir:  "/tmp/test-project",
		SandboxHome: "/tmp/test-sandbox",
		HomeDir:     "/home/testuser",
		Shell:       "/bin/bash",
		Environment: map[string]string{
			"ZEBRA": "z",
			"APPLE": "a",
			"MANGO": "m",
		},
	}

	args, err := iso.buildCommonArgs(cfg)
	if err != nil {
		t.Fatalf("buildCommonArgs failed: %v", err)
	}

	// Find the env var positions
	appleIdx, mangoIdx, zebraIdx := -1, -1, -1
	for i, arg := range args {
		switch arg {
		case "APPLE=a":
			appleIdx = i
		case "MANGO=m":
			mangoIdx = i
		case "ZEBRA=z":
			zebraIdx = i
		}
	}

	if appleIdx == -1 || mangoIdx == -1 || zebraIdx == -1 {
		t.Fatal("Expected all three env vars to be present")
	}

	if appleIdx >= mangoIdx || mangoIdx >= zebraIdx {
		t.Errorf("Environment variables should be sorted: APPLE(%d) < MANGO(%d) < ZEBRA(%d)", appleIdx, mangoIdx, zebraIdx)
	}
}
