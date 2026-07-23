package tools

import (
	"net"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"devsandbox/internal/cmdpattern"
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

	t.Run("agent session reporting active", func(t *testing.T) {
		defer removeHerdrConsumers(t).restore()
		t.Setenv("HERDR_ENV", "1")
		t.Setenv("HERDR_PANE_ID", "pane-7")
		startHerdrWithHostSocket(t)

		h := &Herdr{}
		h.Configure(GlobalConfig{LaunchedAgent: "claude"}, nil)

		res := h.Check("")
		if !hasInfoContaining(res, "agent session reporting: active for claude") {
			t.Errorf("info = %v, want it to report active reporting for claude", res.Info)
		}
		if !hasInfoContaining(res, string(herdrproxy.CapAgentReporting)) {
			t.Errorf("info = %v, want the agent_reporting capability listed", res.Info)
		}
	})

	t.Run("agent session reporting inactive names what is missing", func(t *testing.T) {
		defer removeHerdrConsumers(t).restore()
		t.Setenv("HERDR_ENV", "1")
		t.Setenv("HERDR_PANE_ID", "pane-7")
		startHerdrWithHostSocket(t)

		h := &Herdr{}
		h.Configure(GlobalConfig{}, map[string]any{"mode": "enforce"})

		res := h.Check("")
		if !hasInfoContaining(res, "no known agent was launched") {
			t.Errorf("info = %v, want it to name the missing agent", res.Info)
		}
		if !hasInfoContaining(res, "direct `devsandbox <agent>` launch") {
			t.Errorf("info = %v, want it to explain what enables reporting", res.Info)
		}
	})
}

func hasIssueContaining(res CheckResult, substr string) bool {
	return slices.ContainsFunc(res.Issues, func(s string) bool {
		return strings.Contains(s, substr)
	})
}

func hasInfoContaining(res CheckResult, substr string) bool {
	return slices.ContainsFunc(res.Info, func(s string) bool {
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

// removeHerdrConsumers unregisters every tool declaring a herdr capability, so
// aggregation results depend only on what the test injects. Whether revdiff is
// installed on the machine running the tests must not change the outcome.
func removeHerdrConsumers(t *testing.T) savedConsumers {
	t.Helper()
	var saved savedConsumers
	for _, tl := range All() {
		if _, ok := tl.(ToolWithHerdrRequirements); ok {
			saved.tools = append(saved.tools, tl)
			Unregister(tl.Name())
		}
	}
	return saved
}

// TestHerdr_AgentReportingNeedsBothHostAnchors pins the rule that makes the
// capability's validator meaningful: it is granted only when devsandbox itself
// knows both the pane and the agent. Neither can be influenced from inside the
// sandbox.
func TestHerdr_AgentReportingNeedsBothHostAnchors(t *testing.T) {
	defer removeHerdrConsumers(t).restore()

	tests := []struct {
		name    string
		paneID  string
		agent   string
		want    bool
		wantWhy string
	}{
		{name: "pane and agent present", paneID: "pane-7", agent: "claude", want: true},
		{name: "pane id missing", paneID: "", agent: "claude", want: false, wantWhy: "HERDR_PANE_ID"},
		{name: "agent unknown", paneID: "pane-7", agent: "", want: false, wantWhy: "no known agent"},
		{name: "neither present", paneID: "", agent: "", want: false, wantWhy: "HERDR_PANE_ID"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HERDR_PANE_ID", tt.paneID)

			h := &Herdr{}
			h.Configure(GlobalConfig{LaunchedAgent: tt.agent}, nil)

			ok, why := h.agentReporting()
			if ok != tt.want {
				t.Fatalf("agentReporting() = %v (%q), want %v", ok, why, tt.want)
			}
			if !tt.want && !strings.Contains(why, tt.wantWhy) {
				t.Errorf("reason = %q, want it to name %q", why, tt.wantWhy)
			}

			caps, _ := h.capabilities("")
			has := slices.Contains(caps, herdrproxy.CapAgentReporting)
			if has != tt.want {
				t.Errorf("capabilities() = %v, want CapAgentReporting present=%v", caps, tt.want)
			}
		})
	}
}

func TestHerdr_ConfigureCapturesLaunchedAgent(t *testing.T) {
	h := &Herdr{}
	h.Configure(GlobalConfig{LaunchedAgent: "claude"}, nil)

	if h.launchedAgent != "claude" {
		t.Errorf("launchedAgent = %q, want the agent devsandbox was asked to launch", h.launchedAgent)
	}
}

// TestHerdr_FilterConfigCarriesHostAnchors proves the values the capability
// decision rests on are the same ones the validator receives. A wiring mistake
// here would enable the capability while denying every real report.
func TestHerdr_FilterConfigCarriesHostAnchors(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "pane-42")

	h := &Herdr{}
	h.Configure(GlobalConfig{LaunchedAgent: "claude"}, nil)

	cfg := h.filterConfig(nil, cmdpattern.ScriptPattern{}, nil, nil, nil, "/home/test")
	if cfg.CurrentPaneID != "pane-42" {
		t.Errorf("CurrentPaneID = %q, want the pane herdr gave this process", cfg.CurrentPaneID)
	}
	if cfg.ExpectedAgent != "claude" {
		t.Errorf("ExpectedAgent = %q, want the host-derived launched agent", cfg.ExpectedAgent)
	}
	if want := filepath.Join("/home/test", ".claude", "projects"); cfg.AgentSessionDir != want {
		t.Errorf("AgentSessionDir = %q, want the launched agent's session directory %q", cfg.AgentSessionDir, want)
	}
}

// TestHerdr_AgentSessionDirResolution pins where the agent_session_path bound
// comes from. An empty home must yield an empty bound rather than
// "/.claude/projects", which would deny every real report instead of only
// unbounded paths.
func TestHerdr_AgentSessionDirResolution(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("PI_CODING_AGENT_DIR", "")
	t.Setenv("CODEX_HOME", "")

	tests := []struct {
		name    string
		agent   string
		homeDir string
		want    string
	}{
		{name: "launched agent with a session dir", agent: "claude", homeDir: "/home/test", want: "/home/test/.claude/projects"},
		{name: "pi resolves to its sessions dir", agent: "pi", homeDir: "/home/test", want: "/home/test/.pi/agent/sessions"},
		{name: "pi with an unresolved home", agent: "pi", homeDir: "", want: ""},
		{name: "codex resolves to its sessions dir", agent: "codex", homeDir: "/home/test", want: "/home/test/.codex/sessions"},
		{name: "codex with an unresolved home", agent: "codex", homeDir: "", want: ""},
		{name: "no launched agent", agent: "", homeDir: "/home/test", want: ""},
		{name: "agent with no session dir", agent: "git", homeDir: "/home/test", want: ""},
		{name: "unknown agent", agent: "nope", homeDir: "/home/test", want: ""},
		{name: "unresolved home", agent: "claude", homeDir: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &Herdr{}
			h.Configure(GlobalConfig{LaunchedAgent: tt.agent}, nil)

			if got := h.agentSessionDir(tt.homeDir); got != tt.want {
				t.Errorf("agentSessionDir(%q) = %q, want %q", tt.homeDir, got, tt.want)
			}
		})
	}
}

// TestHerdr_AgentSessionDirHonorsClaudeConfigDir proves the bound tracks the
// same environment Claude's bindings do, so a custom config dir cannot leave
// the filter confining paths to a directory the sandbox never uses.
func TestHerdr_AgentSessionDirHonorsClaudeConfigDir(t *testing.T) {
	custom := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", custom)

	h := &Herdr{}
	h.Configure(GlobalConfig{LaunchedAgent: "claude"}, nil)

	if got, want := h.agentSessionDir("/home/test"), filepath.Join(custom, "projects"); got != want {
		t.Errorf("agentSessionDir = %q, want %q", got, want)
	}
}

// The same invariant for Codex, whose session directory moves with CODEX_HOME.
func TestHerdr_AgentSessionDirHonorsCodexHome(t *testing.T) {
	custom := t.TempDir()
	t.Setenv("CODEX_HOME", custom)

	h := &Herdr{}
	h.Configure(GlobalConfig{LaunchedAgent: "codex"}, nil)

	if got, want := h.agentSessionDir("/home/test"), filepath.Join(custom, "sessions"); got != want {
		t.Errorf("agentSessionDir = %q, want %q", got, want)
	}
}

// startHerdrWithHostSocket stands up a fake host control socket and returns a
// home directory and a sandbox home for the proxy. They must be distinct: the
// relocator refuses to place scripts anywhere the sandbox can write.
func startHerdrWithHostSocket(t *testing.T) (homeDir, sandboxHome string) {
	t.Helper()

	dir := shortSocketDir(t)
	upstream := filepath.Join(dir, "upstream.sock")
	l, err := net.Listen("unix", upstream)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })

	t.Setenv("HERDR_SOCKET_PATH", upstream)
	return shortSocketDir(t), shortSocketDir(t)
}

// TestHerdr_StartAgentAloneStartsProxy covers the phase 1 behavior change: a
// bare `devsandbox claude` in a herdr pane previously started nothing.
func TestHerdr_StartAgentAloneStartsProxy(t *testing.T) {
	defer removeHerdrConsumers(t).restore()

	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "pane-7")
	homeDir, sandboxHome := startHerdrWithHostSocket(t)

	h := &Herdr{}
	h.Configure(GlobalConfig{LaunchedAgent: "claude"}, nil)

	if err := h.Start(t.Context(), homeDir, sandboxHome); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = h.Stop() }()

	if h.proxy == nil {
		t.Fatal("no proxy started for a direct agent launch inside a herdr pane")
	}
}

func TestHerdr_StartWithoutAnchorsStartsNothingInAuto(t *testing.T) {
	defer removeHerdrConsumers(t).restore()

	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "pane-7")
	homeDir, sandboxHome := startHerdrWithHostSocket(t)

	h := &Herdr{}
	h.Configure(GlobalConfig{}, nil) // no launched agent

	if err := h.Start(t.Context(), homeDir, sandboxHome); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = h.Stop() }()

	if h.proxy != nil {
		t.Error("auto mode opened a socket with nothing requesting herdr")
	}
}

// TestHerdr_StartEnforceWithoutAgentStillStarts documents that enforce mode is
// the user asking for the proxy explicitly: it runs and denies, rather than
// quietly not running.
func TestHerdr_StartEnforceWithoutAgentStillStarts(t *testing.T) {
	defer removeHerdrConsumers(t).restore()

	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "")
	homeDir, sandboxHome := startHerdrWithHostSocket(t)

	h := &Herdr{}
	h.Configure(GlobalConfig{}, map[string]any{"mode": "enforce"})

	if err := h.Start(t.Context(), homeDir, sandboxHome); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = h.Stop() }()

	if h.proxy == nil {
		t.Fatal("enforce mode did not start the proxy")
	}
	caps, _ := h.capabilities("")
	if len(caps) != 0 {
		t.Errorf("capabilities = %v, want none without host anchors", caps)
	}
}

// TestHerdr_StaleSocketIsNonFatalInAuto is the regression guard for the hazard
// Task 4 introduces: auto mode now reaches the socket check for every
// `devsandbox <agent>` launch, so a socket left behind by a dead herdr server
// must warn rather than fail the launch.
func TestHerdr_StaleSocketIsNonFatalInAuto(t *testing.T) {
	defer removeHerdrConsumers(t).restore()

	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "pane-7")
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(t.TempDir(), "absent.sock"))

	h := &Herdr{}
	h.Configure(GlobalConfig{LaunchedAgent: "claude"}, nil)

	if err := h.Start(t.Context(), t.TempDir(), t.TempDir()); err != nil {
		t.Fatalf("auto mode failed the launch over an unreachable host socket: %v", err)
	}
	if h.proxy != nil {
		t.Error("auto mode reported a proxy despite an unreachable host socket")
	}
}

func TestHerdr_StaleSocketIsFatalInEnforce(t *testing.T) {
	defer removeHerdrConsumers(t).restore()

	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "pane-7")
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(t.TempDir(), "absent.sock"))

	h := &Herdr{}
	h.Configure(GlobalConfig{LaunchedAgent: "claude"}, map[string]any{"mode": "enforce"})

	err := h.Start(t.Context(), t.TempDir(), t.TempDir())
	if err == nil {
		t.Fatal("enforce mode accepted an unreachable host socket")
	}
	if !strings.Contains(err.Error(), "not reachable") {
		t.Errorf("error = %v, want it to name the unreachable socket", err)
	}
}

// TestHerdr_OutsideHerdrPaneNothingChanges: with HERDR_ENV unset, a devsandbox
// claude launch must behave exactly as it did before this feature existed.
func TestHerdr_OutsideHerdrPaneNothingChanges(t *testing.T) {
	defer removeHerdrConsumers(t).restore()

	t.Setenv("HERDR_ENV", "")
	t.Setenv("HERDR_PANE_ID", "pane-7")
	homeDir, sandboxHome := startHerdrWithHostSocket(t)

	h := &Herdr{}
	h.Configure(GlobalConfig{LaunchedAgent: "claude"}, nil)

	if err := h.Start(t.Context(), homeDir, sandboxHome); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if h.proxy != nil {
		t.Error("Start opened a proxy outside a herdr session")
	}
	if got := h.Environment("/home/u", sandboxHome); got != nil {
		t.Errorf("Environment() = %v outside a herdr session, want none", got)
	}
	if got := h.Bindings("/home/u", sandboxHome); got != nil {
		t.Errorf("Bindings() = %v outside a herdr session, want none", got)
	}
}
