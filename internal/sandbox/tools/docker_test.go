package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDocker_Name(t *testing.T) {
	d := &Docker{}
	if d.Name() != "docker" {
		t.Errorf("expected name 'docker', got %q", d.Name())
	}
}

func TestDocker_Available(t *testing.T) {
	d := &Docker{hostSocket: "/nonexistent/docker.sock"}
	if d.Available("/home/user") {
		t.Error("expected Available=false for nonexistent socket")
	}

	// Test with a real file
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "docker.sock")
	if err := os.WriteFile(sockPath, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	d = &Docker{hostSocket: sockPath}
	if !d.Available("/home/user") {
		t.Error("expected Available=true for existing socket")
	}
}

func TestDocker_Configure_Disabled(t *testing.T) {
	d := &Docker{}
	d.Configure(GlobalConfig{}, nil)

	if d.enabled {
		t.Error("expected enabled=false without config")
	}

	d.Configure(GlobalConfig{}, map[string]any{"enabled": false})
	if d.enabled {
		t.Error("expected enabled=false with enabled=false")
	}
}

func TestDocker_Configure_Enabled(t *testing.T) {
	d := &Docker{}
	d.Configure(GlobalConfig{}, map[string]any{"enabled": true})

	if !d.enabled {
		t.Error("expected enabled=true")
	}

	// Default socket should be resolved via resolveDockerSocket.
	expected := resolveDockerSocket(runtime.GOOS, "", "")
	if d.hostSocket != expected {
		t.Errorf("expected default socket %q, got %q", expected, d.hostSocket)
	}
}

func TestDocker_Configure_CustomSocket(t *testing.T) {
	d := &Docker{}
	d.Configure(GlobalConfig{}, map[string]any{
		"enabled": true,
		"socket":  "/var/run/docker.sock",
	})

	if d.hostSocket != "/var/run/docker.sock" {
		t.Errorf("expected custom socket, got %q", d.hostSocket)
	}
}

func TestDocker_Environment_Disabled(t *testing.T) {
	d := &Docker{enabled: false}
	env := d.Environment("/home/user", "/sandbox/home")
	if env != nil {
		t.Errorf("expected nil environment when disabled, got %d vars", len(env))
	}
}

func TestDocker_Environment_Enabled(t *testing.T) {
	d := &Docker{enabled: true}
	homeDir := "/home/user"
	sandboxHome := "/sandbox/home"
	env := d.Environment(homeDir, sandboxHome)

	if len(env) != 1 {
		t.Fatalf("expected 1 env var, got %d", len(env))
	}

	if env[0].Name != "DOCKER_HOST" {
		t.Errorf("expected DOCKER_HOST, got %q", env[0].Name)
	}

	// Socket path is homeDir/docker.sock (where sandboxHome is mounted inside sandbox)
	expected := "unix://" + homeDir + "/docker.sock"
	if env[0].Value != expected {
		t.Errorf("expected %q, got %q", expected, env[0].Value)
	}
}

func TestDocker_Bindings(t *testing.T) {
	d := &Docker{enabled: true}
	bindings := d.Bindings("/home/user", "/sandbox/home")

	// Docker tool uses proxy, not direct bindings
	if bindings != nil {
		t.Errorf("expected nil bindings, got %d", len(bindings))
	}
}

func TestDocker_Check(t *testing.T) {
	d := &Docker{hostSocket: "/nonexistent/docker.sock"}
	result := d.Check("/home/user")

	if result.Available {
		t.Error("expected Available=false for nonexistent socket")
	}

	if result.InstallHint == "" {
		t.Error("expected non-empty InstallHint")
	}
}

// --- resolveDockerSocket tests ---

func TestResolveDockerSocket_UserProvided(t *testing.T) {
	// Explicit user socket always wins, regardless of OS.
	got := resolveDockerSocket("darwin", "/Users/test", "/custom/docker.sock")
	if got != "/custom/docker.sock" {
		t.Errorf("expected user socket, got %q", got)
	}

	got = resolveDockerSocket("linux", "/home/test", "/custom/docker.sock")
	if got != "/custom/docker.sock" {
		t.Errorf("expected user socket, got %q", got)
	}
}

func TestResolveDockerSocket_Linux(t *testing.T) {
	got := resolveDockerSocket("linux", "/home/test", "")
	if got != "/run/docker.sock" {
		t.Errorf("expected /run/docker.sock, got %q", got)
	}
}

func TestResolveDockerSocket_Darwin_DetectsExisting(t *testing.T) {
	tmpDir := t.TempDir()

	// Create Docker Desktop socket (first candidate).
	dockerDesktopDir := filepath.Join(tmpDir, ".docker", "run")
	if err := os.MkdirAll(dockerDesktopDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dockerDesktopSock := filepath.Join(dockerDesktopDir, "docker.sock")
	if err := os.WriteFile(dockerDesktopSock, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	got := resolveDockerSocket("darwin", tmpDir, "")
	if got != dockerDesktopSock {
		t.Errorf("expected Docker Desktop socket %q, got %q", dockerDesktopSock, got)
	}
}

func TestResolveDockerSocket_Darwin_FallsThrough(t *testing.T) {
	tmpDir := t.TempDir()

	// Only create Colima socket (third candidate); skip Docker Desktop and /var/run.
	colimaDir := filepath.Join(tmpDir, ".colima", "default")
	if err := os.MkdirAll(colimaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	colimaSock := filepath.Join(colimaDir, "docker.sock")
	if err := os.WriteFile(colimaSock, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	got := resolveDockerSocket("darwin", tmpDir, "")
	// /var/run/docker.sock might exist on the test host. If so, it takes precedence.
	if got == colimaSock {
		// Colima socket found — expected on hosts without /var/run/docker.sock.
		return
	}
	if got == "/var/run/docker.sock" {
		// Host has /var/run/docker.sock — that's fine, it's higher priority.
		return
	}
	t.Errorf("expected Colima socket or /var/run/docker.sock, got %q", got)
}

func TestResolveDockerSocket_Darwin_NoneExist(t *testing.T) {
	// Use a temp dir where no sockets exist.
	tmpDir := t.TempDir()

	got := resolveDockerSocket("darwin", tmpDir, "")

	// Should return first candidate as fallback.
	firstCandidate := macOSDockerSocketCandidates(tmpDir)[0]

	// /var/run/docker.sock might exist on the test host; if so it wins.
	if got == "/var/run/docker.sock" {
		return
	}
	if got != firstCandidate {
		t.Errorf("expected first candidate %q, got %q", firstCandidate, got)
	}
}
