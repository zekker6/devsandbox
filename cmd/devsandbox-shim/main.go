// Package main implements the devsandbox container entrypoint shim.
// It replaces the bash entrypoint.sh with a Go binary that:
// 1. Creates sandboxuser matching host UID/GID
// 2. Trusts mise config files
// 3. Sets up cache/directory structure
// 4. Suppresses ssh-agent when SSH is not forwarded
// 5. Drops privileges and execs the user command
//
// Note: .env file hiding is handled at container creation via Docker volume
// mounts (no CAP_SYS_ADMIN needed). See internal/isolator/docker.go.
//
// This is a standalone binary — no internal/ imports, pure stdlib + syscall.
package main

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

const (
	sandboxUser   = "sandboxuser"
	sandboxHome   = "/home/sandboxuser"
	readySentinel = "/tmp/.devsandbox-ready"
)

func main() {
	uid, gid := getHostIDs()

	ensureUser(uid, gid)
	// NOTE: .env hiding is now handled at container creation via Docker volume
	// mounts (no CAP_SYS_ADMIN needed).
	miseTrust(uid, gid)
	setupCacheDirs(uid, gid)
	setupDirectories(uid, gid)
	suppressSSHAgent()
	writeReadySentinel()

	// The remaining args after the entrypoint are the command to run
	args := os.Args[1:]
	if len(args) == 0 {
		args = []string{"/bin/bash"}
	}

	dropPrivsAndExec(uid, gid, args)
}

// getHostIDs reads HOST_UID and HOST_GID from environment, defaulting to 1000.
// Rejects root (0) and negative values to prevent privilege escalation.
func getHostIDs() (uid, gid int) {
	uid = envInt("HOST_UID", 1000)
	gid = envInt("HOST_GID", 1000)
	if uid < 1 {
		uid = 1000
	}
	if gid < 1 {
		gid = 1000
	}
	return uid, gid
}

// envInt reads an integer from an environment variable with a default fallback.
func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

// ensureUser creates the sandboxuser group and user matching the host UID/GID.
// Errors are logged but not fatal — the user/group may already exist.
func ensureUser(uid, gid int) {
	// Create group if it doesn't exist (ignore error — may already exist)
	_ = run("groupadd", "-g", strconv.Itoa(gid), sandboxUser)

	// Create user if it doesn't exist (ignore error — may already exist)
	_ = run("useradd",
		"-u", strconv.Itoa(uid),
		"-g", strconv.Itoa(gid),
		"-m",
		"-d", sandboxHome,
		"-s", "/bin/bash",
		sandboxUser,
	)

	// Fix ownership of sandbox home
	if err := chownRecursive(sandboxHome, uid, gid); err != nil {
		warn("chown sandbox home: %v", err)
	}
}

// miseTrust trusts only known mise config files in the project directory.
// We deliberately avoid `mise trust --all` because it would auto-trust every
// .mise.toml in the container, including attacker-injected configs in cloned repos.
func miseTrust(uid, gid int) {
	projectDir := os.Getenv("PROJECT_DIR")
	if projectDir == "" {
		return
	}

	// Trust only the project-level mise configs that the user controls.
	configs := []string{
		filepath.Join(projectDir, ".mise.toml"),
		filepath.Join(projectDir, ".mise", "config.toml"),
	}

	for _, cfg := range configs {
		if _, err := os.Stat(cfg); err == nil {
			runAsUser(uid, gid, "mise", "trust", cfg)
		}
	}
}

// setupCacheDirs creates and chowns cache directories if the cache volume is mounted.
func setupCacheDirs(uid, gid int) {
	if _, err := os.Stat("/cache"); os.IsNotExist(err) {
		return
	}

	dirs := []string{
		"/cache/mise",
		"/cache/mise/cache",
		"/cache/go/mod",
		"/cache/go/build",
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			warn("create cache dir %s: %v", dir, err)
		}
	}

	if err := chownRecursive("/cache", uid, gid); err != nil {
		warn("chown cache: %v", err)
	}
}

// setupDirectories creates XDG, fish, SSH, and other directories needed by tools.
func setupDirectories(uid, gid int) {
	xdgConfigHome := envOr("XDG_CONFIG_HOME", sandboxHome+"/.config")
	xdgDataHome := envOr("XDG_DATA_HOME", sandboxHome+"/.local/share")
	xdgCacheHome := envOr("XDG_CACHE_HOME", sandboxHome+"/.cache")
	xdgStateHome := envOr("XDG_STATE_HOME", sandboxHome+"/.local/state")

	miseDataDir := sandboxHome + "/.local/share/mise"
	miseCacheDir := sandboxHome + "/.cache/mise"
	miseStateDir := sandboxHome + "/.local/state/mise"

	dirs := []string{
		miseDataDir,
		miseCacheDir,
		miseStateDir,
		xdgDataHome + "/fish",
		xdgStateHome,
		xdgConfigHome,
		xdgCacheHome,
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			warn("create dir %s: %v", dir, err)
		}
	}

	// .ssh directory must be 0700 — OpenSSH refuses to use keys if permissions are too open
	if err := os.MkdirAll(sandboxHome+"/.ssh", 0o700); err != nil {
		warn("create dir %s/.ssh: %v", sandboxHome, err)
	}

	// Create fresh fish_variables file — clears stale universal variables
	fishVarsPath := xdgDataHome + "/fish/fish_variables"
	if err := os.WriteFile(fishVarsPath, nil, 0o644); err != nil {
		warn("create fish_variables: %v", err)
	}

	// Create empty ssh environment file for fish-ssh-agent and similar scripts
	sshEnvPath := sandboxHome + "/.ssh/environment"
	if err := os.WriteFile(sshEnvPath, nil, 0o600); err != nil {
		warn("create ssh/environment: %v", err)
	}

	// Fix ownership
	for _, dir := range []string{
		sandboxHome + "/.local",
		sandboxHome + "/.cache",
		sandboxHome + "/.config",
		sandboxHome + "/.ssh",
	} {
		if err := chownRecursive(dir, uid, gid); err != nil {
			warn("chown %s: %v", dir, err)
		}
	}
}

// suppressSSHAgent writes a no-op ssh-agent wrapper when SSH is not forwarded.
func suppressSSHAgent() {
	if os.Getenv("SSH_AUTH_SOCK") != "" {
		return
	}

	wrapper := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile("/usr/local/bin/ssh-agent", []byte(wrapper), 0o755); err != nil {
		warn("suppress ssh-agent: %v", err)
	}
}

// writeReadySentinel writes a sentinel file to signal that setup is complete.
func writeReadySentinel() {
	if err := os.WriteFile(readySentinel, nil, 0o644); err != nil {
		warn("write ready sentinel: %v", err)
	}
}

// dropPrivsAndExec drops to the sandboxuser UID/GID and execs the given command.
func dropPrivsAndExec(uid, gid int, args []string) {
	binary, err := exec.LookPath(args[0])
	if err != nil {
		fatal("command not found: %s", args[0])
	}

	// Set supplementary groups (clear any root groups)
	if err := syscall.Setgroups([]int{gid}); err != nil {
		fatal("setgroups: %v", err)
	}

	// Must set GID before UID
	if err := syscall.Setgid(gid); err != nil {
		fatal("setgid(%d): %v", gid, err)
	}

	if err := syscall.Setuid(uid); err != nil {
		fatal("setuid(%d): %v", uid, err)
	}

	// Set HOME for the new user
	_ = os.Setenv("HOME", sandboxHome)
	_ = os.Setenv("USER", sandboxUser)

	env := os.Environ()
	if err := syscall.Exec(binary, args, env); err != nil {
		fatal("exec %s: %v", binary, err)
	}
}

// --- helpers ---

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runAsUser(uid, gid int, name string, args ...string) {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{
			Uid: uint32(uid),
			Gid: uint32(gid),
		},
	}
	// Ignore errors — these are best-effort
	_ = cmd.Run()
}

func chownRecursive(path string, uid, gid int) error {
	return filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip inaccessible entries
		}
		// Skip symlinks to prevent following them into unintended directories
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		// For directories, check if they're actually symlinks (WalkDir resolves them)
		if d.IsDir() && p != path {
			info, err := os.Lstat(p)
			if err != nil {
				return nil
			}
			if info.Mode()&fs.ModeSymlink != 0 {
				return fs.SkipDir
			}
		}
		_ = os.Lchown(p, uid, gid)
		return nil
	})
}

func warn(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "devsandbox-shim: warning: "+format+"\n", args...)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "devsandbox-shim: fatal: "+format+"\n", args...)
	os.Exit(1)
}
