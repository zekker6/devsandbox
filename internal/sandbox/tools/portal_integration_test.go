//go:build integration

package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestPortal_Integration_ProxyStartStop(t *testing.T) {
	if _, err := exec.LookPath("xdg-dbus-proxy"); err != nil {
		t.Skip("xdg-dbus-proxy not installed")
	}

	busAddr := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	if busAddr == "" {
		t.Skip("DBUS_SESSION_BUS_ADDRESS not set")
	}

	socketPath := dbusSocketPath(busAddr)
	if socketPath == "" {
		t.Skip("D-Bus session bus is not a unix socket")
	}
	if _, err := os.Stat(socketPath); err != nil {
		t.Skip("D-Bus session bus socket not found")
	}

	sandboxHome := t.TempDir()

	p := &Portal{notifications: true}
	p.Configure(GlobalConfig{}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start proxy
	err := p.Start(ctx, "/home/user", sandboxHome)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Verify socket exists
	proxySocket := filepath.Join(sandboxHome, ".dbus-proxy", "bus")
	if _, err := os.Stat(proxySocket); err != nil {
		t.Fatalf("proxy socket not found: %v", err)
	}

	// Stop proxy
	err = p.Stop()
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	// Verify socket cleaned up
	if _, err := os.Stat(proxySocket); !os.IsNotExist(err) {
		t.Error("expected proxy socket to be cleaned up after Stop")
	}
}
