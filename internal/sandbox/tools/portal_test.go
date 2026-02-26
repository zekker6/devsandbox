package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPortal_Name(t *testing.T) {
	p := &Portal{}
	if p.Name() != "portal" {
		t.Errorf("expected name 'portal', got %q", p.Name())
	}
}

func TestPortal_Available_NoBinary(t *testing.T) {
	p := &Portal{}
	t.Setenv("PATH", t.TempDir())
	if p.Available("/home/user") {
		t.Error("expected Available=false when xdg-dbus-proxy is not in PATH")
	}
}

func TestPortal_Available_NoDBusSocket(t *testing.T) {
	p := &Portal{}
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent/bus")
	if p.Available("/home/user") {
		t.Error("expected Available=false when D-Bus socket doesn't exist")
	}
}

func TestPortal_Configure_Defaults(t *testing.T) {
	p := &Portal{}
	p.Configure(GlobalConfig{}, nil)

	if !p.notifications {
		t.Error("expected notifications=true by default")
	}
}

func TestPortal_Configure_Disabled(t *testing.T) {
	p := &Portal{}
	p.Configure(GlobalConfig{}, map[string]any{"notifications": false})

	if p.notifications {
		t.Error("expected notifications=false")
	}
}

// Task 2 tests

func TestPortal_ProxySocketPath(t *testing.T) {
	p := &Portal{}
	path := p.proxySocketDir("/sandbox/home")
	expected := filepath.Join("/sandbox/home", ".dbus-proxy")
	if path != expected {
		t.Errorf("expected %q, got %q", expected, path)
	}
}

func TestPortal_BuildProxyArgs_AllEnabled(t *testing.T) {
	p := &Portal{notifications: true}
	busAddr := "unix:path=/run/user/1000/bus"

	args := p.buildProxyArgs(busAddr, "/tmp/proxy.sock")

	if args[0] != busAddr {
		t.Errorf("first arg should be bus address, got %q", args[0])
	}
	if args[1] != "/tmp/proxy.sock" {
		t.Errorf("second arg should be proxy socket, got %q", args[1])
	}

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--filter") {
		t.Error("expected --filter flag")
	}
	if !strings.Contains(joined, "--talk=org.freedesktop.portal.Desktop") {
		t.Error("expected portal.Desktop talk rule")
	}
}

func TestPortal_BuildProxyArgs_NotificationsOnly(t *testing.T) {
	p := &Portal{notifications: true}
	args := p.buildProxyArgs("unix:path=/run/user/1000/bus", "/tmp/proxy.sock")
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "--talk=org.freedesktop.portal.Desktop") {
		t.Error("expected portal.Desktop talk rule")
	}
	if !strings.Contains(joined, "--talk=org.freedesktop.portal.Notification") {
		t.Error("expected portal.Notification talk rule")
	}
}

func TestDbusSocketPath(t *testing.T) {
	tests := []struct {
		addr string
		want string
	}{
		{"unix:path=/run/user/1000/bus", "/run/user/1000/bus"},
		{"unix:path=/run/user/1000/bus,guid=abc123", "/run/user/1000/bus"},
		{"unix:abstract=/tmp/dbus-xyz", ""},
		{"tcp:host=127.0.0.1,port=1234", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := dbusSocketPath(tt.addr)
		if got != tt.want {
			t.Errorf("dbusSocketPath(%q) = %q, want %q", tt.addr, got, tt.want)
		}
	}
}

// Task 3 tests

func TestPortal_Bindings(t *testing.T) {
	p := &Portal{
		notifications: true,
		proxySocket:   "/sandbox/home/.dbus-proxy/bus",
		xdgRuntime:    "/run/user/1000",
	}

	bindings := p.Bindings("/home/user", "/sandbox/home")

	// Should have 2 bindings: proxy socket dir + .flatpak-info
	if len(bindings) != 2 {
		t.Fatalf("expected 2 bindings, got %d", len(bindings))
	}

	b := bindings[0]
	if b.Source != "/sandbox/home/.dbus-proxy" {
		t.Errorf("expected source /sandbox/home/.dbus-proxy, got %q", b.Source)
	}
	if !strings.HasSuffix(b.Dest, ".dbus-proxy") {
		t.Errorf("expected dest ending in .dbus-proxy, got %q", b.Dest)
	}
	if !b.ReadOnly {
		t.Error("expected read-only binding")
	}

	flatpakBinding := bindings[1]
	if flatpakBinding.Dest != "/.flatpak-info" {
		t.Errorf("expected dest /.flatpak-info, got %q", flatpakBinding.Dest)
	}
	if !flatpakBinding.ReadOnly {
		t.Error("expected .flatpak-info to be read-only")
	}
}

func TestPortal_Bindings_Disabled(t *testing.T) {
	p := &Portal{notifications: false}
	bindings := p.Bindings("/home/user", "/sandbox/home")
	if bindings != nil {
		t.Errorf("expected nil bindings when disabled, got %d", len(bindings))
	}
}

func TestPortal_Environment(t *testing.T) {
	p := &Portal{
		notifications: true,
		xdgRuntime:    "/run/user/1000",
	}
	env := p.Environment("/home/user", "/sandbox/home")

	var foundBusAddr bool
	for _, e := range env {
		if e.Name == "DBUS_SESSION_BUS_ADDRESS" {
			foundBusAddr = true
			expected := "unix:path=/run/user/1000/.dbus-proxy/bus"
			if e.Value != expected {
				t.Errorf("expected %q, got %q", expected, e.Value)
			}
		}
	}
	if !foundBusAddr {
		t.Error("expected DBUS_SESSION_BUS_ADDRESS in environment")
	}
}

func TestPortal_Environment_Disabled(t *testing.T) {
	p := &Portal{notifications: false}
	env := p.Environment("/home/user", "/sandbox/home")
	if env != nil {
		t.Errorf("expected nil environment when disabled, got %d", len(env))
	}
}

// Task 4 tests

func TestPortal_Setup_CreatesFlatpakInfo(t *testing.T) {
	sandboxHome := t.TempDir()

	p := &Portal{notifications: true}
	err := p.Setup("/home/user", sandboxHome)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	infoPath := filepath.Join(sandboxHome, ".flatpak-info")
	data, err := os.ReadFile(infoPath)
	if err != nil {
		t.Fatalf("failed to read .flatpak-info: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "[Application]") {
		t.Error("expected [Application] section in .flatpak-info")
	}
	if !strings.Contains(content, "name=dev.devsandbox.App") {
		t.Error("expected app name in .flatpak-info")
	}
}

func TestPortal_Setup_Disabled(t *testing.T) {
	sandboxHome := t.TempDir()

	p := &Portal{notifications: false}
	err := p.Setup("/home/user", sandboxHome)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	infoPath := filepath.Join(sandboxHome, ".flatpak-info")
	if _, err := os.Stat(infoPath); !os.IsNotExist(err) {
		t.Error("expected .flatpak-info to NOT be created when disabled")
	}
}

// Task 5 tests

func TestPortal_Check_NoBinary(t *testing.T) {
	p := &Portal{}
	t.Setenv("PATH", t.TempDir())

	result := p.Check("/home/user")
	if result.Available {
		t.Error("expected Available=false when binary not found")
	}
	if result.BinaryName != "xdg-dbus-proxy" {
		t.Errorf("expected BinaryName xdg-dbus-proxy, got %q", result.BinaryName)
	}
	if result.InstallHint == "" {
		t.Error("expected non-empty InstallHint")
	}
}

// Task 6 tests

func TestPortal_InterfaceCompliance(t *testing.T) {
	var _ Tool = (*Portal)(nil)
	var _ ToolWithConfig = (*Portal)(nil)
	var _ ToolWithSetup = (*Portal)(nil)
	var _ ToolWithCheck = (*Portal)(nil)
	var _ ActiveTool = (*Portal)(nil)
	var _ ToolWithLogger = (*Portal)(nil)
}

func TestPortal_Registered(t *testing.T) {
	tool := Get("portal")
	if tool == nil {
		t.Fatal("expected portal tool to be registered")
	}
	if tool.Name() != "portal" {
		t.Errorf("expected name 'portal', got %q", tool.Name())
	}
}
