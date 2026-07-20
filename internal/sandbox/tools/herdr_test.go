package tools

import (
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"devsandbox/internal/herdrproxy"
)

func TestHerdr_Name(t *testing.T) {
	h := &Herdr{}
	if h.Name() != "herdr" {
		t.Errorf("Name() = %q, want %q", h.Name(), "herdr")
	}
	if h.Description() == "" {
		t.Error("Description() is empty")
	}
}

func TestHerdr_Available(t *testing.T) {
	tests := []struct {
		name    string
		herdEnv string
		want    bool
	}{
		{name: "inside a herdr session", herdEnv: "1", want: true},
		{name: "HERDR_ENV unset", herdEnv: "", want: false},
		{name: "HERDR_ENV set to something else", herdEnv: "0", want: false},
		{name: "HERDR_ENV set to true rather than 1", herdEnv: "true", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HERDR_ENV", tt.herdEnv)

			h := &Herdr{}
			got := h.Available("")
			// Available also requires the binary; when it is absent the answer
			// is false regardless, so only assert the positive case when the
			// binary is actually present.
			if !tt.want && got {
				t.Errorf("Available() = true, want false")
			}
			if tt.want && !got && !binaryMissing(t, "herdr") {
				t.Errorf("Available() = false with HERDR_ENV=1 and herdr installed, want true")
			}
		})
	}
}

func binaryMissing(t *testing.T, name string) bool {
	t.Helper()
	return !CheckBinary(name, "").Available
}

func TestHerdr_ConfigureModes(t *testing.T) {
	tests := []struct {
		name string
		cfg  map[string]any
		want string
	}{
		{name: "nil config defaults to auto", cfg: nil, want: herdrModeAuto},
		{name: "empty config defaults to auto", cfg: map[string]any{}, want: herdrModeAuto},
		{name: "explicit auto", cfg: map[string]any{"mode": "auto"}, want: herdrModeAuto},
		{name: "disabled", cfg: map[string]any{"mode": "disabled"}, want: herdrModeDisabled},
		{name: "enforce", cfg: map[string]any{"mode": "enforce"}, want: herdrModeEnforce},
		{name: "unknown mode falls back to auto", cfg: map[string]any{"mode": "yolo"}, want: herdrModeAuto},
		{name: "non-string mode falls back to auto", cfg: map[string]any{"mode": 42}, want: herdrModeAuto},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &Herdr{}
			h.Configure(GlobalConfig{}, tt.cfg)
			if h.mode != tt.want {
				t.Errorf("mode = %q, want %q", h.mode, tt.want)
			}
		})
	}
}

func TestHerdr_ConfigureCapturesProjectDir(t *testing.T) {
	h := &Herdr{}
	h.Configure(GlobalConfig{ProjectDir: "/work/proj", HomeDir: "/home/u"}, nil)

	if h.projectDir != "/work/proj" {
		t.Errorf("projectDir = %q, want the configured project dir", h.projectDir)
	}
	if h.homeDir != "/home/u" {
		t.Errorf("homeDir = %q, want the configured home dir", h.homeDir)
	}
}

// TestHerdr_BindingsNeverIncludeHostSocket is the mount-side half of the
// security model: the sandbox may see the herdr binary, never the socket.
func TestHerdr_BindingsNeverIncludeHostSocket(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_SOCKET_PATH", "/home/u/.config/herdr/herdr.sock")

	h := &Herdr{}
	for _, b := range h.Bindings("/home/u", "/sandbox/home") {
		if strings.Contains(b.Source, ".sock") || strings.Contains(b.Dest, ".sock") {
			t.Errorf("Bindings exposes a socket: %+v", b)
		}
		if !b.ReadOnly {
			t.Errorf("Bindings mounts %q writable; the herdr binary must be read-only", b.Source)
		}
	}
}

func TestHerdr_BindingsEmptyOutsideSession(t *testing.T) {
	t.Setenv("HERDR_ENV", "")

	h := &Herdr{}
	if got := h.Bindings("/home/u", "/sandbox/home"); got != nil {
		t.Errorf("Bindings() = %v outside a herdr session, want nil", got)
	}
}

// TestHerdr_EnvironmentRequiresRunningProxy guards against pointing the CLI at
// a socket that does not exist, which is how the kitty tool previously stranded
// clients on a dead path.
func TestHerdr_EnvironmentRequiresRunningProxy(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")

	h := &Herdr{} // proxy nil: not started
	if got := h.Environment("/home/u", "/sandbox/home"); got != nil {
		t.Errorf("Environment() = %v with no proxy running, want nil", got)
	}
}

func TestHerdrHostSocket(t *testing.T) {
	t.Run("honors a host-set HERDR_SOCKET_PATH", func(t *testing.T) {
		t.Setenv("HERDR_SOCKET_PATH", "/custom/herdr.sock")
		if got := herdrHostSocket("/home/u"); got != "/custom/herdr.sock" {
			t.Errorf("herdrHostSocket() = %q, want the host override", got)
		}
	})

	t.Run("defaults to the config directory", func(t *testing.T) {
		t.Setenv("HERDR_SOCKET_PATH", "")
		want := filepath.Join("/home/u", ".config", "herdr", "herdr.sock")
		if got := herdrHostSocket("/home/u"); got != want {
			t.Errorf("herdrHostSocket() = %q, want %q", got, want)
		}
	})
}

// TestHerdrScriptsPathIsOutsideSandboxReach checks the relocation directory is
// not somewhere the sandbox can write, which would defeat relocation entirely.
func TestHerdrScriptsPathIsOutsideSandboxReach(t *testing.T) {
	const home = "/home/u"
	const sandboxHome = "/home/u/.local/share/devsandbox/proj/home"

	got := herdrScriptsPath(home, sandboxHome)

	for _, forbidden := range sandboxVisiblePaths(home, sandboxHome) {
		if got == forbidden || strings.HasPrefix(got, forbidden+"/") {
			t.Errorf("scripts path %q lies inside sandbox-visible path %q", got, forbidden)
		}
	}
	if !strings.HasPrefix(got, filepath.Join(home, herdrScriptsRelPath)) {
		t.Errorf("scripts path %q is not under the expected root", got)
	}
}

func TestHerdrScriptsPathIsPerSession(t *testing.T) {
	a := herdrScriptsPath("/home/u", "/sandbox/a")
	b := herdrScriptsPath("/home/u", "/sandbox/b")

	if a == b {
		t.Error("two sandbox homes share a scripts directory; sessions must not see each other's scripts")
	}
	if again := herdrScriptsPath("/home/u", "/sandbox/a"); again != a {
		t.Error("scripts path is not deterministic for the same sandbox home")
	}
}

func TestHerdr_StartDisabledDoesNothing(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")

	h := &Herdr{}
	h.Configure(GlobalConfig{}, map[string]any{"mode": "disabled"})

	if err := h.Start(t.Context(), t.TempDir(), t.TempDir()); err != nil {
		t.Fatalf("Start returned error in disabled mode: %v", err)
	}
	if h.proxy != nil {
		t.Error("disabled mode started a proxy")
	}
	if got := h.Environment("/home/u", "/sandbox/home"); got != nil {
		t.Errorf("disabled mode exported %v, want no environment", got)
	}
}

func TestHerdr_StartOutsideSessionDoesNothing(t *testing.T) {
	t.Setenv("HERDR_ENV", "")

	h := &Herdr{}
	h.Configure(GlobalConfig{}, nil)

	if err := h.Start(t.Context(), t.TempDir(), t.TempDir()); err != nil {
		t.Fatalf("Start returned error outside a herdr session: %v", err)
	}
	if h.proxy != nil {
		t.Error("Start opened a proxy outside a herdr session")
	}
}

func TestHerdr_StopIsSafeWhenNeverStarted(t *testing.T) {
	h := &Herdr{}
	if err := h.Stop(); err != nil {
		t.Errorf("Stop returned error when never started: %v", err)
	}
}

func TestHerdr_CheckBranches(t *testing.T) {
	if binaryMissing(t, "herdr") {
		t.Skip("herdr binary not installed")
	}

	t.Run("not in a herdr session", func(t *testing.T) {
		t.Setenv("HERDR_ENV", "")
		h := &Herdr{}
		h.Configure(GlobalConfig{}, nil)

		res := h.Check("")
		if res.Available {
			t.Error("Check reports available outside a herdr session")
		}
		if !hasIssueContaining(res, "HERDR_ENV") {
			t.Errorf("issues = %v, want one mentioning HERDR_ENV", res.Issues)
		}
	})

	t.Run("disabled mode", func(t *testing.T) {
		t.Setenv("HERDR_ENV", "1")
		h := &Herdr{}
		h.Configure(GlobalConfig{}, map[string]any{"mode": "disabled"})

		res := h.Check("")
		if res.Available {
			t.Error("Check reports available in disabled mode")
		}
		if !hasIssueContaining(res, "disabled") {
			t.Errorf("issues = %v, want one mentioning that it is disabled", res.Issues)
		}
	})

	t.Run("socket unreachable", func(t *testing.T) {
		t.Setenv("HERDR_ENV", "1")
		t.Setenv("HERDR_SOCKET_PATH", filepath.Join(t.TempDir(), "absent.sock"))

		h := &Herdr{}
		h.Configure(GlobalConfig{}, nil)

		res := h.Check("")
		if res.Available {
			t.Error("Check reports available with an unreachable socket")
		}
		if !hasIssueContaining(res, "socket not found") {
			t.Errorf("issues = %v, want one mentioning the missing socket", res.Issues)
		}
	})
}

func hasIssueContaining(res CheckResult, substr string) bool {
	return slices.ContainsFunc(res.Issues, func(s string) bool {
		return strings.Contains(s, substr)
	})
}

// TestHerdr_ConfigFlowsThroughGenericToolMap documents that [tools.herdr]
// needs no config-package change: config.Tools is map[string]any and only
// mount_mode is validated, so the section reaches Configure verbatim.
func TestHerdr_ConfigFlowsThroughGenericToolMap(t *testing.T) {
	// Shaped exactly as config.Tools["herdr"] arrives from TOML decoding.
	toolCfg := map[string]any{"mode": "disabled"}

	h := &Herdr{}
	h.Configure(GlobalConfig{ProjectDir: "/work"}, toolCfg)

	if h.mode != herdrModeDisabled {
		t.Fatalf("mode = %q, want %q", h.mode, herdrModeDisabled)
	}

	// And disabled must actually prevent the proxy from starting.
	t.Setenv("HERDR_ENV", "1")
	if err := h.Start(t.Context(), t.TempDir(), t.TempDir()); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	if h.proxy != nil {
		t.Error("mode=disabled from config still started the proxy")
	}
}

// TestBinaryNeedsBind covers the crash reported when herdr is mise-managed.
//
// The mise tool mounts ~/.local/share/mise as an overlay. Binding the herdr
// binary at its own absolute path under that directory registered a child mount
// before its parent, and the builder panics rather than let the parent shadow
// the child ("mounting parent ... after child ... would shadow it").
func TestBinaryNeedsBind(t *testing.T) {
	const home = "/home/zekker"

	tests := []struct {
		name string
		bin  string
		home string
		want bool
	}{
		{
			name: "mise-managed binary under home needs no bind",
			bin:  "/home/zekker/.local/share/mise/installs/github-ogulcancelik-herdr/0.7.4/herdr",
			home: home,
			want: false,
		},
		{
			name: "binary in ~/.local/bin needs no bind",
			bin:  "/home/zekker/.local/bin/herdr",
			home: home,
			want: false,
		},
		{
			name: "system binary needs a bind",
			bin:  "/usr/bin/herdr",
			home: home,
			want: true,
		},
		{
			name: "usr-local binary needs a bind",
			bin:  "/usr/local/bin/herdr",
			home: home,
			want: true,
		},
		{
			name: "sibling directory sharing a prefix still needs a bind",
			bin:  "/home/zekker-other/bin/herdr",
			home: home,
			want: true,
		},
		{
			name: "unknown home falls back to binding",
			bin:  "/usr/bin/herdr",
			home: "",
			want: true,
		},
		{
			name: "uncleaned path under home is still recognized",
			bin:  "/home/zekker/./.local/share/mise/bin/herdr",
			home: home,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := binaryNeedsBind(tt.bin, tt.home); got != tt.want {
				t.Errorf("binaryNeedsBind(%q, %q) = %v, want %v", tt.bin, tt.home, got, tt.want)
			}
		})
	}
}

// TestHerdr_BindingsSkipsBinaryUnderHome is the end-to-end form of the same
// regression: with a home that contains the resolved herdr binary, Bindings
// must produce nothing at all rather than a mount the builder will reject.
func TestHerdr_BindingsSkipsBinaryUnderHome(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")

	bin, err := exec.LookPath("herdr")
	if err != nil {
		t.Skipf("herdr not on PATH: %v", err)
	}
	if !strings.HasPrefix(bin, "/home/") && !strings.HasPrefix(bin, "/root/") {
		t.Skipf("herdr is not installed under a home directory (%s)", bin)
	}

	// Use the binary's own ancestor as the home so the test is independent of
	// which user runs it.
	home := filepath.Dir(filepath.Dir(bin))

	h := &Herdr{}
	h.Configure(GlobalConfig{HomeDir: home}, nil)

	if got := h.Bindings(home, "/sandbox/home"); len(got) != 0 {
		t.Errorf("Bindings() = %+v for a binary under %q, want none (the parent mount already covers it)", got, home)
	}
}

// TestHerdr_EnvironmentExportsSandboxVisiblePath is the regression test for a
// bug that unit tests missed and only a live sandbox run surfaced.
//
// Start creates the proxy socket under the host-side sandbox home, but that
// directory is mounted at the user's home path inside the sandbox. Exporting
// the host path made HERDR_SOCKET_PATH name a file that does not exist in the
// sandbox, so every herdr command from inside failed. The exported value must
// be derived from homeDir, matching what the kitty tool does for
// KITTY_LISTEN_ON.
func TestHerdr_EnvironmentExportsSandboxVisiblePath(t *testing.T) {
	const homeDir = "/home/zekker"
	const sandboxHome = "/home/zekker/.local/share/devsandbox/devsandbox-a8ff856e/home"

	h := &Herdr{}
	// Environment only reports when a proxy is running; stand one up cheaply
	// rather than starting a real listener.
	h.proxy = &herdrproxy.Proxy{}

	var socketPath string
	for _, e := range h.Environment(homeDir, sandboxHome) {
		if e.Name == "HERDR_SOCKET_PATH" {
			socketPath = e.Value
		}
	}
	if socketPath == "" {
		t.Fatal("Environment did not export HERDR_SOCKET_PATH")
	}

	if strings.HasPrefix(socketPath, sandboxHome) {
		t.Errorf("HERDR_SOCKET_PATH = %q, which is the host-side sandbox home; "+
			"that path does not exist inside the sandbox", socketPath)
	}
	if !strings.HasPrefix(socketPath, homeDir+"/") {
		t.Errorf("HERDR_SOCKET_PATH = %q, want a path under the sandbox-visible home %q",
			socketPath, homeDir)
	}
	if !strings.HasSuffix(socketPath, herdrProxySocketName) {
		t.Errorf("HERDR_SOCKET_PATH = %q, want it to end in %q", socketPath, herdrProxySocketName)
	}
}

// TestHerdr_EnvironmentMatchesKittyConvention pins herdr's socket-path
// derivation to kitty's, since both mount the same per-session run directory.
func TestHerdr_EnvironmentMatchesKittyConvention(t *testing.T) {
	const homeDir = "/home/zekker"
	const sandboxHome = "/host/sandbox/home"

	h := &Herdr{proxy: &herdrproxy.Proxy{}}
	var herdrSock string
	for _, e := range h.Environment(homeDir, sandboxHome) {
		if e.Name == "HERDR_SOCKET_PATH" {
			herdrSock = e.Value
		}
	}

	want := filepath.Join(runDir(homeDir), herdrProxySocketName)
	if herdrSock != want {
		t.Errorf("HERDR_SOCKET_PATH = %q, want %q (runDir(homeDir), as kitty uses)", herdrSock, want)
	}
}

// TestHerdr_BindingsExposeProxyAtDefaultPath covers the fix for `herdr session
// list` reporting the session as "stopped" inside the sandbox.
//
// That subcommand ignores HERDR_SOCKET_PATH and connects straight to
// <home>/.config/herdr/herdr.sock. Its probe is connect(2) only, with no
// protocol traffic, so binding the proxy socket at that path makes the status
// correct without any request reaching the filter.
func TestHerdr_BindingsExposeProxyAtDefaultPath(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")

	const homeDir = "/home/zekker"
	const sandboxHome = "/host/sandbox/home"

	h := &Herdr{proxy: &herdrproxy.Proxy{}}
	bs := h.Bindings(homeDir, sandboxHome)

	var sock *Binding
	for i := range bs {
		if strings.HasSuffix(bs[i].Dest, herdrProxySocketName) {
			sock = &bs[i]
		}
	}
	if sock == nil {
		t.Fatal("Bindings did not expose the proxy socket at the default herdr path")
	}

	wantDest := filepath.Join(homeDir, ".config", "herdr", herdrProxySocketName)
	if sock.Dest != wantDest {
		t.Errorf("dest = %q, want %q (the path herdr's client derives on its own)", sock.Dest, wantDest)
	}
	wantSrc := filepath.Join(runDir(sandboxHome), herdrProxySocketName)
	if sock.Source != wantSrc {
		t.Errorf("source = %q, want the proxy socket %q", sock.Source, wantSrc)
	}

	// A unix socket cannot be reached through an overlay lower layer, and
	// connect(2) needs write permission on the socket file.
	if sock.Type != MountBind {
		t.Errorf("type = %q, want %q; an overlay would hide the socket", sock.Type, MountBind)
	}
	if sock.ReadOnly {
		t.Error("socket bound read-only; connect(2) requires write permission")
	}

	// Critically, it must be the PROXY socket, never the host control socket.
	if strings.Contains(sock.Source, ".config/herdr") {
		t.Errorf("source %q is the host control socket; only the proxy may be exposed", sock.Source)
	}
}

func TestHerdr_BindingsNoSocketWhenProxyStopped(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")

	h := &Herdr{} // proxy nil
	for _, b := range h.Bindings("/home/zekker", "/host/sandbox/home") {
		if strings.HasSuffix(b.Dest, herdrProxySocketName) {
			t.Errorf("exposed a socket path %q with no proxy running; it would be a dead endpoint", b.Dest)
		}
	}
}
