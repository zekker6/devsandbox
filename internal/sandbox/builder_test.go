package sandbox

import (
	"reflect"
	"testing"
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
