package tools

import (
	"os"
	"path/filepath"
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

	// Default socket
	if d.hostSocket != "/run/docker.sock" {
		t.Errorf("expected default socket /run/docker.sock, got %q", d.hostSocket)
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
