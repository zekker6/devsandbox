package isolator

import (
	"slices"
	"testing"
)

// A non-interactive run/exec must attach stdin with -i (no TTY) so piped input
// reaches the workload; an interactive session gets a TTY with -it.
func TestBuildRunArgs_StdinAttachment(t *testing.T) {
	iso := NewDockerIsolator(DockerConfig{})
	iso.imageTag = "devsandbox:local"
	cfg := &Config{
		ProjectDir:  "/tmp/p",
		SandboxHome: "/tmp/s",
		HomeDir:     "/home/u",
		Shell:       "/bin/bash",
		Command:     []string{"cat"},
	}

	cfg.Interactive = false
	args, err := iso.buildRunArgs(cfg)
	if err != nil {
		t.Fatalf("buildRunArgs(non-interactive): %v", err)
	}
	if !slices.Contains(args, "-i") || slices.Contains(args, "-it") {
		t.Errorf("non-interactive run must use -i (not -it); got %v", args)
	}

	cfg.Interactive = true
	args, err = iso.buildRunArgs(cfg)
	if err != nil {
		t.Fatalf("buildRunArgs(interactive): %v", err)
	}
	if !slices.Contains(args, "-it") {
		t.Errorf("interactive run must use -it; got %v", args)
	}
}

func TestBuildExecArgs_StdinAttachment(t *testing.T) {
	cfg := &Config{Shell: "/bin/bash", Command: []string{"cat"}}
	args := buildExecArgs(cfg, "container")
	if !slices.Contains(args, "-i") || slices.Contains(args, "-it") {
		t.Errorf("non-interactive exec must use -i (not -it); got %v", args)
	}

	cfg.Interactive = true
	args = buildExecArgs(cfg, "container")
	if !slices.Contains(args, "-it") {
		t.Errorf("interactive exec must use -it; got %v", args)
	}
}
