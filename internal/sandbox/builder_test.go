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
		SandboxHome: "/home/test/.sandboxes/myproject",
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
	expected := []string{
		"--clearenv",
		"--unshare-pid",
		"--die-with-parent",
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
	}

	if !reflect.DeepEqual(args, expected) {
		t.Errorf("AddBaseArgs() = %v, want %v", args, expected)
	}
}
