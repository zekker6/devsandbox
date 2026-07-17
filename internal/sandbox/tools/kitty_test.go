package tools

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"devsandbox/internal/kittyproxy"
)

// shortSocketDir returns a tempdir whose path is short enough for a UNIX
// domain socket beneath it to fit within macOS's 104-byte sun_path limit.
// t.TempDir() on macOS lives under /var/folders/... and easily exceeds it.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ds")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// fakeKittyConsumer is a Tool that declares CapLaunchOverlay so we can test
// aggregation without depending on the real revdiff tool.
type fakeKittyConsumer struct{}

func (fakeKittyConsumer) Name() string                        { return "fake-kitty-consumer" }
func (fakeKittyConsumer) Description() string                 { return "test" }
func (fakeKittyConsumer) Available(string) bool               { return true }
func (fakeKittyConsumer) Bindings(string, string) []Binding   { return nil }
func (fakeKittyConsumer) Environment(string, string) []EnvVar { return nil }
func (fakeKittyConsumer) ShellInit(string) string             { return "" }
func (fakeKittyConsumer) KittyCapabilities() []kittyproxy.Capability {
	return []kittyproxy.Capability{kittyproxy.CapLaunchOverlay}
}
func (fakeKittyConsumer) KittyLaunchPatterns() []kittyproxy.CommandPattern {
	return []kittyproxy.CommandPattern{{Program: "revdiff", ArgsMatcher: kittyproxy.MatchAny()}}
}

func TestKitty_Available(t *testing.T) {
	t.Setenv("KITTY_LISTEN_ON", "")
	k := &Kitty{}
	if k.Available("") {
		t.Error("Available should be false when KITTY_LISTEN_ON unset")
	}
	t.Setenv("KITTY_LISTEN_ON", "unix:/tmp/kitty-1234")
	// Available also requires the kitty binary on PATH; if not present, this is fine.
	_ = k.Available("")
}

func TestKitty_NoBindingsForHostSocket(t *testing.T) {
	t.Setenv("KITTY_LISTEN_ON", "unix:/tmp/kitty-1234")
	k := &Kitty{}
	bs := k.Bindings("", "")
	for _, b := range bs {
		if strings.Contains(b.Source, "/tmp/kitty-1234") {
			t.Errorf("kitty tool must NOT bind-mount host socket, but did: %+v", b)
		}
	}
}

func TestKitty_AggregateAndStart_AutoModeInactiveWithoutConsumers(t *testing.T) {
	// Hermetically remove any registered kitty consumers so this test exercises
	// the "no consumers declared" branch regardless of what init() has wired up.
	saved := removeKittyConsumers(t)
	defer saved.restore()

	t.Setenv("KITTY_LISTEN_ON", "unix:/tmp/kitty-1234")
	k := &Kitty{}
	k.Configure(GlobalConfig{}, nil)

	dir := t.TempDir()
	if err := k.Start(context.Background(), dir, dir); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = k.Stop() }()

	if k.proxy != nil {
		t.Error("auto mode with no consumers should not start a proxy")
	}
	envs := k.Environment(dir, dir)
	for _, e := range envs {
		if e.Name == "KITTY_LISTEN_ON" {
			t.Errorf("inactive kitty must not export KITTY_LISTEN_ON; got %+v", e)
		}
	}
}

// savedConsumers remembers tools that implemented ToolWithKittyRequirements and
// were temporarily removed from the registry so re-registration on cleanup
// restores the pre-test state.
type savedConsumers struct {
	tools []Tool
}

func (s savedConsumers) restore() {
	for _, t := range s.tools {
		Register(t)
	}
}

// removeKittyConsumers unregisters every currently-registered ToolWithKittyRequirements.
// Returns a handle whose .restore() re-registers them. Keeps the Kitty tool itself.
func removeKittyConsumers(t *testing.T) savedConsumers {
	t.Helper()
	var saved savedConsumers
	for _, tl := range All() {
		if _, ok := tl.(ToolWithKittyRequirements); ok {
			saved.tools = append(saved.tools, tl)
			Unregister(tl.Name())
		}
	}
	return saved
}

func TestKitty_AggregateAndStart_StartsProxyWhenConsumerPresent(t *testing.T) {
	// Create a fake host upstream socket.
	dir := shortSocketDir(t)
	upstream := filepath.Join(dir, "upstream.sock")
	l, err := net.Listen("unix", upstream)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()

	t.Setenv("KITTY_LISTEN_ON", "unix:"+upstream)

	// Inject a fake consumer into the registry just for this test.
	Register(fakeKittyConsumer{})
	defer Unregister("fake-kitty-consumer")

	k := &Kitty{}
	k.Configure(GlobalConfig{}, nil)

	// sandboxHome must also be short: kitty's proxy listens under <sandboxHome>.
	sandboxHome := shortSocketDir(t)
	if err := k.Start(context.Background(), sandboxHome, sandboxHome); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = k.Stop() }()

	if k.proxy == nil {
		t.Fatal("expected proxy to be started when consumer present")
	}
	expected := filepath.Join(runDir(sandboxHome), kittyProxySocketName)
	if _, err := os.Stat(expected); err != nil {
		t.Errorf("proxy socket not created: %v", err)
	}

	// KITTY_LISTEN_ON must point at the proxy socket inside the sandbox.
	var listen string
	for _, e := range k.Environment(sandboxHome, sandboxHome) {
		if e.Name == "KITTY_LISTEN_ON" {
			listen = e.Value
		}
	}
	wantPrefix := "unix:" + expected
	if listen != wantPrefix {
		t.Errorf("KITTY_LISTEN_ON = %q, want %q", listen, wantPrefix)
	}
}

// The proxy socket must be private to this process: sandbox home is shared by
// every session for the project, and closing the listener unlinks the path, so
// a shared path lets one session's exit break a live session's KITTY_LISTEN_ON.
func TestKitty_SocketPath_IsProcessScoped(t *testing.T) {
	dir := shortSocketDir(t)
	upstream := filepath.Join(dir, "upstream.sock")
	l, err := net.Listen("unix", upstream)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()

	t.Setenv("KITTY_LISTEN_ON", "unix:"+upstream)
	Register(fakeKittyConsumer{})
	defer Unregister("fake-kitty-consumer")

	k := &Kitty{}
	k.Configure(GlobalConfig{}, nil)

	sandboxHome := shortSocketDir(t)
	if err := k.Start(context.Background(), sandboxHome, sandboxHome); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = k.Stop() }()

	// A concurrent session's socket, in its own run dir under the same home.
	peerDir := filepath.Join(sandboxHome, runDirName, "1")
	if err := os.MkdirAll(peerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	peerSocket := filepath.Join(peerDir, kittyProxySocketName)
	if err := os.WriteFile(peerSocket, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := k.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if _, err := os.Stat(peerSocket); err != nil {
		t.Errorf("Stop removed a concurrent session's socket: %v", err)
	}
}

func TestKitty_DisabledMode(t *testing.T) {
	dir := shortSocketDir(t)
	upstream := filepath.Join(dir, "upstream.sock")
	l, err := net.Listen("unix", upstream)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()

	t.Setenv("KITTY_LISTEN_ON", "unix:"+upstream)
	Register(fakeKittyConsumer{})
	defer Unregister("fake-kitty-consumer")

	k := &Kitty{}
	k.Configure(GlobalConfig{}, map[string]any{"mode": "disabled"})

	sandboxHome := t.TempDir()
	if err := k.Start(context.Background(), sandboxHome, sandboxHome); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = k.Stop() }()

	if k.proxy != nil {
		t.Error("disabled mode must not start proxy")
	}
}
