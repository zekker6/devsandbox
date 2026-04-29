package sandbox

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"devsandbox/internal/config"
	"devsandbox/internal/sandbox/mounts"
	"devsandbox/internal/sandbox/tools"
)

func TestBuilder_BasicArgs(t *testing.T) {
	cfg := &Config{
		HomeDir:     "/home/test",
		ProjectDir:  "/home/test/myproject",
		ProjectName: "myproject",
		SandboxHome: "/home/test/.local/share/devsandbox/myproject/home",
		XDGRuntime:  "/run/user/1000",
	}

	b := NewBuilder(cfg)
	b.ClearEnv().UnsharePID().DieWithParent()

	args := b.Build()
	expected := []string{"--clearenv", "--unshare-pid", "--die-with-parent"}

	if !reflect.DeepEqual(args, expected) {
		t.Errorf("Builder args = %v, want %v", args, expected)
	}
}

func TestBuilder_Proc_Dev_Tmpfs(t *testing.T) {
	cfg := &Config{}
	b := NewBuilder(cfg)
	b.Proc("/proc").Dev("/dev").Tmpfs("/tmp")

	args := b.Build()
	expected := []string{"--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp"}

	if !reflect.DeepEqual(args, expected) {
		t.Errorf("Builder args = %v, want %v", args, expected)
	}
}

func TestBuilder_Bindings(t *testing.T) {
	cfg := &Config{}
	b := NewBuilder(cfg)
	b.ROBind("/usr", "/usr").
		Bind("/home/test/project", "/home/test/project").
		Symlink("usr/lib", "/lib").
		Dir("/home/test/.config")

	args := b.Build()
	expected := []string{
		"--ro-bind", "/usr", "/usr",
		"--bind", "/home/test/project", "/home/test/project",
		"--symlink", "usr/lib", "/lib",
		"--dir", "/home/test/.config",
	}

	if !reflect.DeepEqual(args, expected) {
		t.Errorf("Builder args = %v, want %v", args, expected)
	}
}

func TestBuilder_Network_Chdir(t *testing.T) {
	cfg := &Config{}
	b := NewBuilder(cfg)
	b.ShareNet().Chdir("/home/test/project")

	args := b.Build()
	expected := []string{"--share-net", "--chdir", "/home/test/project"}

	if !reflect.DeepEqual(args, expected) {
		t.Errorf("Builder args = %v, want %v", args, expected)
	}
}

func TestBuilder_SetEnv(t *testing.T) {
	cfg := &Config{}
	b := NewBuilder(cfg)
	b.SetEnv("HOME", "/home/test").
		SetEnv("USER", "testuser").
		SetEnv("PATH", "/usr/bin:/bin")

	args := b.Build()
	expected := []string{
		"--setenv", "HOME", "/home/test",
		"--setenv", "USER", "testuser",
		"--setenv", "PATH", "/usr/bin:/bin",
	}

	if !reflect.DeepEqual(args, expected) {
		t.Errorf("Builder args = %v, want %v", args, expected)
	}
}

func TestBuilder_AddBaseArgs(t *testing.T) {
	cfg := &Config{}
	b := NewBuilder(cfg)
	b.AddBaseArgs()

	args := b.Build()

	// Check that base args are present
	expectedPrefix := []string{
		"--clearenv",
		"--unshare-user",
		"--unshare-pid",
		"--unshare-ipc",
		"--unshare-uts",
		"--die-with-parent",
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
	}

	if len(args) < len(expectedPrefix) {
		t.Fatalf("AddBaseArgs() returned too few args: %v", args)
	}

	for i, expected := range expectedPrefix {
		if args[i] != expected {
			t.Errorf("AddBaseArgs()[%d] = %v, want %v", i, args[i], expected)
		}
	}

	// Check that --uid and --gid are present
	hasUID := false
	hasGID := false
	for i, arg := range args {
		if arg == "--uid" && i+1 < len(args) {
			hasUID = true
		}
		if arg == "--gid" && i+1 < len(args) {
			hasGID = true
		}
	}

	if !hasUID {
		t.Error("AddBaseArgs() missing --uid flag")
	}
	if !hasGID {
		t.Error("AddBaseArgs() missing --gid flag")
	}
}

func TestBuilder_OverlaySrc(t *testing.T) {
	cfg := &Config{}
	b := NewBuilder(cfg)
	b.OverlaySrc("/lower1").OverlaySrc("/lower2")

	args := b.Build()
	expected := []string{
		"--overlay-src", "/lower1",
		"--overlay-src", "/lower2",
	}

	if !reflect.DeepEqual(args, expected) {
		t.Errorf("OverlaySrc args = %v, want %v", args, expected)
	}
}

func TestBuilder_TmpOverlay(t *testing.T) {
	cfg := &Config{}
	b := NewBuilder(cfg)
	b.OverlaySrc("/lower").TmpOverlay("/dest")

	args := b.Build()
	expected := []string{
		"--overlay-src", "/lower",
		"--tmp-overlay", "/dest",
	}

	if !reflect.DeepEqual(args, expected) {
		t.Errorf("TmpOverlay args = %v, want %v", args, expected)
	}
}

func TestBuilder_Overlay(t *testing.T) {
	cfg := &Config{}
	b := NewBuilder(cfg)
	b.OverlaySrc("/lower").Overlay("/upper", "/work", "/dest")

	args := b.Build()
	expected := []string{
		"--overlay-src", "/lower",
		"--overlay", "/upper", "/work", "/dest",
	}

	if !reflect.DeepEqual(args, expected) {
		t.Errorf("Overlay args = %v, want %v", args, expected)
	}
}

func TestBuilder_ROOverlay(t *testing.T) {
	cfg := &Config{}
	b := NewBuilder(cfg)
	b.OverlaySrc("/lower1").OverlaySrc("/lower2").ROOverlay("/dest")

	args := b.Build()
	expected := []string{
		"--overlay-src", "/lower1",
		"--overlay-src", "/lower2",
		"--ro-overlay", "/dest",
	}

	if !reflect.DeepEqual(args, expected) {
		t.Errorf("ROOverlay args = %v, want %v", args, expected)
	}
}

func TestBuilder_TmpOverlay_PanicWithoutSrc(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("TmpOverlay should panic without preceding OverlaySrc")
		}
	}()

	cfg := &Config{}
	b := NewBuilder(cfg)
	b.TmpOverlay("/dest") // should panic
}

func TestBuilder_Overlay_PanicWithoutSrc(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("Overlay should panic without preceding OverlaySrc")
		}
	}()

	cfg := &Config{}
	b := NewBuilder(cfg)
	b.Overlay("/upper", "/work", "/dest") // should panic
}

func TestBuilder_ROOverlay_PanicWithoutSrc(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("ROOverlay should panic without preceding OverlaySrc")
		}
	}()

	cfg := &Config{}
	b := NewBuilder(cfg)
	b.ROOverlay("/dest") // should panic
}

func TestBuilder_Overlay_ResetsState(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("second TmpOverlay should panic after state reset")
		}
	}()

	cfg := &Config{}
	b := NewBuilder(cfg)
	b.OverlaySrc("/lower").TmpOverlay("/dest1")
	// State should be reset, so this should panic
	b.TmpOverlay("/dest2")
}

func TestBuilder_MountConflict_ExactPath(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Error("mounting same path twice should panic")
		}
		// Verify error message mentions "ambiguous"
		msg, ok := r.(string)
		if !ok || !contains(msg, "ambiguous") {
			t.Errorf("panic message should mention 'ambiguous', got: %v", r)
		}
	}()

	cfg := &Config{}
	b := NewBuilder(cfg)
	b.Bind("/home/test/project", "/home/test/project")
	b.ROBind("/home/test/project", "/home/test/project") // should panic - same dest
}

func TestBuilder_MountConflict_ParentAfterChild(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Error("mounting parent after child should panic")
		}
		// Verify error message mentions "shadow"
		msg, ok := r.(string)
		if !ok || !contains(msg, "shadow") {
			t.Errorf("panic message should mention 'shadow', got: %v", r)
		}
	}()

	cfg := &Config{}
	b := NewBuilder(cfg)
	b.ROBind("/home/test/project/.git", "/home/test/project/.git") // child first
	b.Bind("/home/test/project", "/home/test/project")             // parent after - should panic
}

func TestBuilder_MountConflict_ChildAfterParent_OK(t *testing.T) {
	// Child after parent should NOT panic - this is valid (child overrides)
	cfg := &Config{}
	b := NewBuilder(cfg)
	b.Bind("/home/test/project", "/home/test/project")             // parent first
	b.ROBind("/home/test/project/.git", "/home/test/project/.git") // child after - OK

	// If we get here without panic, the test passes
	args := b.Build()
	if len(args) != 6 {
		t.Errorf("expected 6 args, got %d: %v", len(args), args)
	}
}

func TestBuilder_MountConflict_DifferentPaths_OK(t *testing.T) {
	// Unrelated paths should not conflict
	cfg := &Config{}
	b := NewBuilder(cfg)
	b.Bind("/home/test/project1", "/home/test/project1")
	b.Bind("/home/test/project2", "/home/test/project2")
	b.ROBind("/usr", "/usr")

	args := b.Build()
	if len(args) != 9 {
		t.Errorf("expected 9 args, got %d: %v", len(args), args)
	}
}

func TestBuilder_MountConflict_OverlayAfterBind(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Error("overlay to same path as existing bind should panic")
		}
	}()

	cfg := &Config{}
	b := NewBuilder(cfg)
	b.Bind("/home/test/project", "/home/test/project")
	b.OverlaySrc("/lower").TmpOverlay("/home/test/project") // should panic - same dest
}

func TestIsParentPath(t *testing.T) {
	tests := []struct {
		parent   string
		child    string
		expected bool
	}{
		{"/home/test/project", "/home/test/project/.git", true},
		{"/home/test/project", "/home/test/project/src/main.go", true},
		{"/home/test", "/home/test/project", true},
		{"/home/test/project/.git", "/home/test/project", false},
		{"/home/test/project", "/home/test/project", false},
		{"/home/test/project1", "/home/test/project2", false},
		{"/home/test/project", "/home/test/project-other", false},
		{"/usr", "/usr/lib", true},
		{"/usr/lib", "/usr", false},
	}

	for _, tt := range tests {
		t.Run(tt.parent+"_"+tt.child, func(t *testing.T) {
			result := isParentPath(tt.parent, tt.child)
			if result != tt.expected {
				t.Errorf("isParentPath(%q, %q) = %v, want %v",
					tt.parent, tt.child, result, tt.expected)
			}
		})
	}
}

func TestBuilder_AddCustomMounts_SkipsHomePaths(t *testing.T) {
	// Create temp dirs for the mounts to resolve against
	tmpDir := t.TempDir()
	homeDir := filepath.Join(tmpDir, "home", "test")
	outsideDir := filepath.Join(tmpDir, "opt", "tools")
	homeChildDir := filepath.Join(homeDir, ".claude")
	projectDir := filepath.Join(homeDir, "myproject")

	for _, d := range []string{outsideDir, homeChildDir, projectDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	engine := mounts.NewEngine(config.MountsConfig{
		Rules: []config.MountRule{
			{Pattern: outsideDir, Mode: "readonly"},
			{Pattern: homeChildDir, Mode: "readwrite"},
		},
	}, homeDir)

	cfg := &Config{
		HomeDir:      homeDir,
		ProjectDir:   projectDir,
		SandboxHome:  filepath.Join(tmpDir, "sandbox", "home"),
		MountsConfig: engine,
	}

	b := NewBuilder(cfg)
	b.AddCustomMounts()
	args := b.Build()

	// outsideDir should be mounted (--ro-bind)
	foundOutside := false
	foundHomeChild := false
	for _, arg := range args {
		if arg == outsideDir {
			foundOutside = true
		}
		if arg == homeChildDir {
			foundHomeChild = true
		}
	}

	if !foundOutside {
		t.Errorf("expected %s to be mounted by AddCustomMounts, args: %v", outsideDir, args)
	}
	if foundHomeChild {
		t.Errorf("expected %s to NOT be mounted by AddCustomMounts (should be deferred to AddHomeCustomMounts), args: %v", homeChildDir, args)
	}
}

func TestBuilder_HomeCustomMount_NoPanic(t *testing.T) {
	tmpDir := t.TempDir()
	homeDir := filepath.Join(tmpDir, "home", "test")
	homeChildDir := filepath.Join(homeDir, "Code", "victoria-metrics", ".claude")
	projectDir := filepath.Join(homeDir, "myproject")
	sandboxHome := filepath.Join(tmpDir, "sandbox", "home")

	for _, d := range []string{homeChildDir, projectDir, sandboxHome} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	engine := mounts.NewEngine(config.MountsConfig{
		Rules: []config.MountRule{
			{Pattern: homeChildDir, Mode: "readwrite"},
		},
	}, homeDir)

	cfg := &Config{
		HomeDir:      homeDir,
		ProjectDir:   projectDir,
		SandboxHome:  sandboxHome,
		MountsConfig: engine,
	}

	// This should NOT panic — the exact scenario from the bug report
	b := NewBuilder(cfg)
	b.AddCustomMounts()
	b.Bind(sandboxHome, homeDir) // simulates AddSandboxHome
	b.AddHomeCustomMounts()

	args := b.Build()

	// Verify the sandbox home bind comes before the child bind
	homeIdx := -1
	childIdx := -1
	for i, arg := range args {
		if arg == homeDir && i > 0 && args[i-1] == sandboxHome {
			homeIdx = i
		}
		if arg == homeChildDir {
			childIdx = i
		}
	}

	if homeIdx == -1 {
		t.Errorf("sandbox home bind not found in args: %v", args)
	}
	if childIdx == -1 {
		t.Errorf("home child bind not found in args: %v", args)
	}
	if homeIdx != -1 && childIdx != -1 && homeIdx >= childIdx {
		t.Errorf("sandbox home (idx %d) should come before child mount (idx %d) in args: %v", homeIdx, childIdx, args)
	}
}

func TestBuilder_AddHomeCustomMounts_MountsHomePaths(t *testing.T) {
	tmpDir := t.TempDir()
	homeDir := filepath.Join(tmpDir, "home", "test")
	outsideDir := filepath.Join(tmpDir, "opt", "tools")
	homeChildDir := filepath.Join(homeDir, ".claude")
	projectDir := filepath.Join(homeDir, "myproject")
	sandboxHome := filepath.Join(tmpDir, "sandbox", "home")

	for _, d := range []string{outsideDir, homeChildDir, projectDir, sandboxHome} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	engine := mounts.NewEngine(config.MountsConfig{
		Rules: []config.MountRule{
			{Pattern: outsideDir, Mode: "readonly"},
			{Pattern: homeChildDir, Mode: "readwrite"},
		},
	}, homeDir)

	cfg := &Config{
		HomeDir:      homeDir,
		ProjectDir:   projectDir,
		SandboxHome:  sandboxHome,
		MountsConfig: engine,
	}

	b := NewBuilder(cfg)
	// Simulate the correct build order: sandbox home first, then home custom mounts
	b.Bind(sandboxHome, homeDir) // AddSandboxHome would do this
	b.AddHomeCustomMounts()
	args := b.Build()

	// homeChildDir should be mounted (--bind)
	foundHomeChild := false
	foundOutside := false
	for _, arg := range args {
		if arg == homeChildDir {
			foundHomeChild = true
		}
		if arg == outsideDir {
			foundOutside = true
		}
	}

	if !foundHomeChild {
		t.Errorf("expected %s to be mounted by AddHomeCustomMounts, args: %v", homeChildDir, args)
	}
	if foundOutside {
		t.Errorf("expected %s to NOT be mounted by AddHomeCustomMounts, args: %v", outsideDir, args)
	}
}

func TestBuilder_isInsideHome(t *testing.T) {
	cfg := &Config{
		HomeDir:    "/home/test",
		ProjectDir: "/home/test/myproject",
	}
	b := NewBuilder(cfg)

	tests := []struct {
		path     string
		expected bool
	}{
		{"/home/test", true},
		{"/home/test/.config", true},
		{"/home/test/Code/project/.claude", true},
		{"/home/testing", false},
		{"/opt/tools", false},
		{"/usr/lib", false},
		{"/home/test-other", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := b.isInsideHome(tt.path)
			if result != tt.expected {
				t.Errorf("isInsideHome(%q) = %v, want %v", tt.path, result, tt.expected)
			}
		})
	}
}

// contains checks if s contains substr (simple helper for tests)
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestBuilder_SuppressSSHAgent_NoSSH(t *testing.T) {
	sandboxHome := t.TempDir()

	cfg := &Config{
		HomeDir:     "/home/test",
		SandboxHome: sandboxHome,
	}

	b := NewBuilder(cfg)
	// No .ssh mount — SSH is not enabled
	b.SuppressSSHAgent()

	// Check that .ssh/environment was created
	envFile := filepath.Join(sandboxHome, ".ssh", "environment")
	info, err := os.Stat(envFile)
	if err != nil {
		t.Fatalf("expected .ssh/environment to exist, got error: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf("expected empty .ssh/environment, got size %d", info.Size())
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("expected .ssh/environment permissions 0600, got %o", info.Mode().Perm())
	}

	// Check that no-op ssh-agent wrapper was created
	wrapperPath := filepath.Join(sandboxHome, ".local", "bin", "ssh-agent")
	wrapperInfo, err := os.Stat(wrapperPath)
	if err != nil {
		t.Fatalf("expected ssh-agent wrapper to exist, got error: %v", err)
	}
	if wrapperInfo.Mode().Perm()&0o111 == 0 {
		t.Error("expected ssh-agent wrapper to be executable")
	}

	content, err := os.ReadFile(wrapperPath)
	if err != nil {
		t.Fatalf("failed to read ssh-agent wrapper: %v", err)
	}
	if string(content) != "#!/bin/sh\nexit 0\n" {
		t.Errorf("unexpected ssh-agent wrapper content: %q", string(content))
	}
}

func TestBuilder_SuppressSSHAgent_SSHEnabled(t *testing.T) {
	sandboxHome := t.TempDir()

	cfg := &Config{
		HomeDir:     "/home/test",
		SandboxHome: sandboxHome,
	}

	// Pre-create a leftover wrapper from a previous run
	localBin := filepath.Join(sandboxHome, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		t.Fatal(err)
	}
	wrapperPath := filepath.Join(localBin, "ssh-agent")
	if err := os.WriteFile(wrapperPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	b := NewBuilder(cfg)
	// Simulate SSH being enabled by mounting .ssh
	b.Bind(sandboxHome, "/home/test")
	b.ROBind("/home/test/.ssh", "/home/test/.ssh")
	b.SuppressSSHAgent()

	// Leftover wrapper should be removed
	if _, err := os.Stat(wrapperPath); !os.IsNotExist(err) {
		t.Error("expected ssh-agent wrapper to be removed when SSH is enabled")
	}
}

func TestBuilderErr(t *testing.T) {
	cfg := &Config{
		HomeDir:     "/home/test",
		SandboxHome: "/tmp/sandbox",
	}

	b := NewBuilder(cfg)

	// Initially no error
	if err := b.Err(); err != nil {
		t.Errorf("expected no error initially, got %v", err)
	}

	// Build should still work
	args := b.Build()
	if args == nil {
		t.Error("expected non-nil args")
	}
}

func TestBuilder_AddProxyEnvironment_BuiltinVars(t *testing.T) {
	cfg := &Config{
		ProxyEnabled: true,
		ProxyMITM:    true,
		ProxyPort:    8080,
		GatewayIP:    "10.0.2.2",
	}

	b := NewBuilder(cfg)
	b.AddProxyEnvironment()

	args := b.Build()

	// Check for YARN proxy vars
	expectedVars := map[string]string{
		"HTTP_PROXY":         "http://10.0.2.2:8080",
		"HTTPS_PROXY":        "http://10.0.2.2:8080",
		"http_proxy":         "http://10.0.2.2:8080",
		"https_proxy":        "http://10.0.2.2:8080",
		"YARN_HTTP_PROXY":    "http://10.0.2.2:8080",
		"YARN_HTTPS_PROXY":   "http://10.0.2.2:8080",
		"NO_PROXY":           "localhost,127.0.0.1",
		"no_proxy":           "localhost,127.0.0.1",
		"NODE_USE_ENV_PROXY": "1",
		"DEVSANDBOX_PROXY":   "1",
	}

	for wantName, wantValue := range expectedVars {
		found := false
		for i := 0; i < len(args)-2; i++ {
			if args[i] == "--setenv" && args[i+1] == wantName && args[i+2] == wantValue {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing env var %s=%s in args", wantName, wantValue)
		}
	}
}

func TestBuilder_AddProxyEnvironment_ExtraCAEnv(t *testing.T) {
	cfg := &Config{
		ProxyEnabled:    true,
		ProxyMITM:       true,
		ProxyPort:       8080,
		GatewayIP:       "10.0.2.2",
		ProxyExtraCAEnv: []string{"MY_CA_BUNDLE", "CUSTOM_SSL_CERT"},
	}

	b := NewBuilder(cfg)
	b.AddProxyEnvironment()

	args := b.Build()
	caCertPath := "/tmp/devsandbox-ca.crt"

	for _, varName := range []string{"MY_CA_BUNDLE", "CUSTOM_SSL_CERT"} {
		found := false
		for i := 0; i < len(args)-2; i++ {
			if args[i] == "--setenv" && args[i+1] == varName && args[i+2] == caCertPath {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing extra CA env var %s=%s in args", varName, caCertPath)
		}
	}
}

func TestBuilder_AddProxyEnvironment_ExtraEnv(t *testing.T) {
	cfg := &Config{
		ProxyEnabled:  true,
		ProxyMITM:     true,
		ProxyPort:     9090,
		GatewayIP:     "10.0.2.2",
		ProxyExtraEnv: []string{"MY_CUSTOM_PROXY", "ANOTHER_PROXY"},
	}

	b := NewBuilder(cfg)
	b.AddProxyEnvironment()

	args := b.Build()
	proxyURL := "http://10.0.2.2:9090"

	for _, varName := range []string{"MY_CUSTOM_PROXY", "ANOTHER_PROXY"} {
		found := false
		for i := 0; i < len(args)-2; i++ {
			if args[i] == "--setenv" && args[i+1] == varName && args[i+2] == proxyURL {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing extra env var %s=%s in args", varName, proxyURL)
		}
	}
}

func TestBuilder_AddEnvironment_EnvPassthrough(t *testing.T) {
	t.Setenv("PASSTHROUGH_SET", "hello")
	// Deliberately do NOT set PASSTHROUGH_UNSET

	cfg := &Config{
		HomeDir:        "/home/testuser",
		SandboxHome:    "/tmp/sandbox/home",
		Shell:          ShellBash,
		ShellPath:      "/bin/bash",
		EnvPassthrough: []string{"PASSTHROUGH_SET", "PASSTHROUGH_UNSET"},
	}

	b := NewBuilder(cfg)
	b.AddEnvironment()

	args := b.Build()
	argsStr := strings.Join(args, " ")

	if !strings.Contains(argsStr, "PASSTHROUGH_SET") {
		t.Error("expected PASSTHROUGH_SET to be passed through")
	}
	if strings.Contains(argsStr, "PASSTHROUGH_UNSET") {
		t.Error("PASSTHROUGH_UNSET should not appear (not set on host)")
	}
}

func TestBuilder_AddEnvironment_EnvVars(t *testing.T) {
	t.Setenv("PASSTHROUGH_CONFLICT", "from-host")

	cfg := &Config{
		HomeDir:        "/home/testuser",
		SandboxHome:    "/tmp/sandbox/home",
		Shell:          ShellBash,
		ShellPath:      "/bin/bash",
		EnvPassthrough: []string{"PASSTHROUGH_CONFLICT"},
		EnvVars: map[string]string{
			"PASSTHROUGH_CONFLICT": "from-config",
			"LITERAL_ONLY":         "hello",
		},
	}

	b := NewBuilder(cfg)
	b.AddEnvironment()

	args := b.Build()
	argsStr := strings.Join(args, " ")

	if !strings.Contains(argsStr, "--setenv LITERAL_ONLY hello") {
		t.Error("expected LITERAL_ONLY=hello to be set from EnvVars")
	}
	if !strings.Contains(argsStr, "--setenv PASSTHROUGH_CONFLICT from-config") {
		t.Error("expected PASSTHROUGH_CONFLICT=from-config (EnvVars value) to be present")
	}
	if strings.Contains(argsStr, "--setenv PASSTHROUGH_CONFLICT from-host") {
		t.Error("PASSTHROUGH_CONFLICT=from-host should not appear (EnvVars must override passthrough)")
	}
}

func TestBuilder_AddProxyEnvironment_NoMITM(t *testing.T) {
	cfg := &Config{
		ProxyEnabled: true,
		ProxyMITM:    false,
		ProxyPort:    8080,
		GatewayIP:    "10.0.2.2",
	}

	b := NewBuilder(cfg)
	b.AddProxyEnvironment()

	args := b.Build()
	joined := strings.Join(args, " ")

	// Should still have proxy env vars
	if !strings.Contains(joined, "--setenv HTTP_PROXY http://10.0.2.2:8080") {
		t.Error("expected HTTP_PROXY to be set")
	}
	if !strings.Contains(joined, "--setenv DEVSANDBOX_PROXY 1") {
		t.Error("expected DEVSANDBOX_PROXY to be set")
	}

	// Should NOT have CA cert env vars
	if strings.Contains(joined, "NODE_EXTRA_CA_CERTS") {
		t.Error("NODE_EXTRA_CA_CERTS should not be set when MITM is disabled")
	}
	if strings.Contains(joined, "SSL_CERT_FILE") {
		t.Error("SSL_CERT_FILE should not be set when MITM is disabled")
	}
	if strings.Contains(joined, "CURL_CA_BUNDLE") {
		t.Error("CURL_CA_BUNDLE should not be set when MITM is disabled")
	}
	if strings.Contains(joined, "GIT_SSL_CAINFO") {
		t.Error("GIT_SSL_CAINFO should not be set when MITM is disabled")
	}
	if strings.Contains(joined, "REQUESTS_CA_BUNDLE") {
		t.Error("REQUESTS_CA_BUNDLE should not be set when MITM is disabled")
	}
}

func TestBuilder_AddProxyEnvironment_NoMITM_SkipsExtraCAEnv(t *testing.T) {
	cfg := &Config{
		ProxyEnabled:    true,
		ProxyMITM:       false,
		ProxyPort:       8080,
		GatewayIP:       "10.0.2.2",
		ProxyExtraCAEnv: []string{"MY_TOOL_CA_BUNDLE"},
	}

	b := NewBuilder(cfg)
	b.AddProxyEnvironment()

	args := b.Build()
	joined := strings.Join(args, " ")

	if strings.Contains(joined, "MY_TOOL_CA_BUNDLE") {
		t.Error("extra CA env vars should not be set when MITM is disabled")
	}
}

func TestBuilder_ApplyPersistentOverlay_ConcurrentSession(t *testing.T) {
	tmpDir := t.TempDir()

	// Create the source directory (overlay requires existing dir)
	srcDir := filepath.Join(tmpDir, "src", ".cache", "mise")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Compute the persistent upper dir path the same way persistentOverlayUpperDir does
	sandboxHome := filepath.Join(tmpDir, "sandbox", "home")
	persistentUpper, err := persistentOverlayUpperDir(sandboxHome, srcDir, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(persistentUpper, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{
		HomeDir:      filepath.Join(tmpDir, "src"),
		SandboxHome:  sandboxHome,
		IsConcurrent: true,
		SessionID:    "test1234",
	}

	builder := NewBuilder(cfg)
	builder.AddBaseArgs()

	binding := tools.Binding{
		Source:   srcDir,
		Type:     tools.MountOverlay,
		Category: tools.CategoryCache,
	}

	builder.applyPersistentOverlay(binding, srcDir, sandboxHome)

	args := builder.Build()
	joined := strings.Join(args, " ")

	// Should have the persistent upper as an overlay-src
	if !strings.Contains(joined, "--overlay-src "+persistentUpper) {
		t.Errorf("expected persistent upper as overlay-src, got:\n%s", joined)
	}

	// Should have session-scoped overlay dirs
	sessionOverlayBase := filepath.Join(sandboxHome, "overlay", "sessions", "test1234")
	if !strings.Contains(joined, sessionOverlayBase) {
		t.Errorf("expected session-scoped overlay dir, got:\n%s", joined)
	}
}

func TestResolveBindingType(t *testing.T) {
	tests := []struct {
		name         string
		category     tools.BindingCategory
		toolMode     string
		globalMode   string
		explicitType tools.MountType
		wantType     tools.MountType
		wantRO       bool
	}{
		// Split mode (default)
		{"split/config", tools.CategoryConfig, "", "split", "", tools.MountTmpOverlay, false},
		{"split/cache", tools.CategoryCache, "", "split", "", tools.MountOverlay, false},
		{"split/data", tools.CategoryData, "", "split", "", tools.MountOverlay, false},
		{"split/state", tools.CategoryState, "", "split", "", tools.MountOverlay, false},
		{"split/runtime", tools.CategoryRuntime, "", "split", "", tools.MountTmpOverlay, false},
		{"split/empty category", "", "", "split", "", tools.MountTmpOverlay, false},

		// Readonly mode
		{"readonly/config", tools.CategoryConfig, "", "readonly", "", tools.MountBind, true},
		{"readonly/cache", tools.CategoryCache, "", "readonly", "", tools.MountBind, true},

		// Readwrite mode
		{"readwrite/config", tools.CategoryConfig, "", "readwrite", "", tools.MountBind, false},
		{"readwrite/cache", tools.CategoryCache, "", "readwrite", "", tools.MountBind, false},

		// Overlay mode
		{"overlay/config", tools.CategoryConfig, "", "overlay", "", tools.MountOverlay, false},
		{"overlay/cache", tools.CategoryCache, "", "overlay", "", tools.MountOverlay, false},

		// Tmpoverlay mode
		{"tmpoverlay/config", tools.CategoryConfig, "", "tmpoverlay", "", tools.MountTmpOverlay, false},
		{"tmpoverlay/cache", tools.CategoryCache, "", "tmpoverlay", "", tools.MountTmpOverlay, false},

		// Per-tool override
		{"tool override readwrite", tools.CategoryConfig, "readwrite", "split", "", tools.MountBind, false},
		{"tool override readonly", tools.CategoryCache, "readonly", "split", "", tools.MountBind, true},
		{"tool override overlay", tools.CategoryConfig, "overlay", "split", "", tools.MountOverlay, false},

		// Explicit Type from tool (escape hatch)
		{"explicit type preserved", tools.CategoryConfig, "", "split", tools.MountBind, tools.MountBind, false},

		// Default global mode when empty
		{"empty global defaults to split", tools.CategoryConfig, "", "", "", tools.MountTmpOverlay, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			binding := tools.Binding{
				Source:   "/home/test/.config/foo",
				Category: tt.category,
				Type:     tt.explicitType,
			}
			ResolveBindingType(&binding, tt.toolMode, tt.globalMode)
			if binding.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", binding.Type, tt.wantType)
			}
			if tt.wantType == tools.MountBind && binding.ReadOnly != tt.wantRO {
				t.Errorf("ReadOnly = %v, want %v", binding.ReadOnly, tt.wantRO)
			}
		})
	}
}

// TestBuilder_AddProjectBindings_WorktreeLeavesMainRepoAlone verifies that
// when ProjectDir is a worktree path (GitRepoRoot set and distinct), the
// project bindings mount only the worktree — the main repo tree must not
// appear in the mount plan. The git tool handles the .git mount separately.
func TestBuilder_AddProjectBindings_WorktreeLeavesMainRepoAlone(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	wt := t.TempDir()

	cfg := &Config{
		HomeDir:     t.TempDir(),
		ProjectDir:  wt,
		GitRepoRoot: repo,
		SandboxHome: t.TempDir(),
		XDGRuntime:  filepath.Join(t.TempDir(), "runtime"),
	}
	b := NewBuilder(cfg)
	b.AddProjectBindings()
	args := b.Build()
	joined := strings.Join(args, " ")

	// Worktree path is bound rw at its host path.
	wantBind := "--bind " + wt + " " + wt
	if !strings.Contains(joined, wantBind) {
		t.Errorf("expected %q in args; got:\n%s", wantBind, joined)
	}
	// Chdir targets the worktree, not the repo.
	wantChdir := "--chdir " + wt
	if !strings.Contains(joined, wantChdir) {
		t.Errorf("expected %q in args; got:\n%s", wantChdir, joined)
	}
	// Main repo path must not appear as a bind source. Since the repo path
	// is a tempdir, a simple substring check is safe — it won't collide with
	// unrelated builder args.
	if strings.Contains(joined, repo) {
		t.Errorf("main repo %q leaked into project bindings:\n%s", repo, joined)
	}
}
