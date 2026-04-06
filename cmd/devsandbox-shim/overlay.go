package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

const overlayLayersBase = "/tmp/.devsandbox-overlay-layers"

// prepareOverlayDirs creates upper and work directories for an overlay mount.
// Uses a hash of the target path to produce deterministic, unique dir names.
func prepareOverlayDirs(base, target string) (upper, work string, err error) {
	h := sha256.Sum256([]byte(target))
	name := hex.EncodeToString(h[:])[:16]

	upper = filepath.Join(base, name, "upper")
	work = filepath.Join(base, name, "work")

	if err := os.MkdirAll(upper, 0o755); err != nil {
		return "", "", fmt.Errorf("create overlay upper dir: %w", err)
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		return "", "", fmt.Errorf("create overlay work dir: %w", err)
	}
	return upper, work, nil
}

// buildOverlayMountOptions builds the data string for mount(2) with overlay type.
func buildOverlayMountOptions(lowerdir, upperdir, workdir string) string {
	return "lowerdir=" + lowerdir + ",upperdir=" + upperdir + ",workdir=" + workdir
}

// splitOverlaySpec splits a "target:upper:work" spec. Uses last two colons
// to handle paths that may contain colons (unlikely but defensive).
func splitOverlaySpec(spec string) []string {
	last := len(spec) - 1
	secondColon := -1
	firstColon := -1
	for i := last; i >= 0; i-- {
		if spec[i] == ':' {
			if secondColon == -1 {
				secondColon = i
			} else {
				firstColon = i
				break
			}
		}
	}
	if firstColon == -1 || secondColon == -1 {
		return nil
	}
	return []string{spec[:firstColon], spec[firstColon+1 : secondColon], spec[secondColon+1:]}
}

func writeIDMap(pid int, filename, content string) {
	path := fmt.Sprintf("/proc/%d/%s", pid, filename)
	writeFile(path, content)
}

func writeFile(path, content string) {
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		fatal("write %s: %v", path, err)
	}
}

// execWithOverlays forks into a new user+mount namespace, mounts overlayfs
// for each entry, drops privileges, and execs the command. Does not return.
func execWithOverlays(uid, gid int, overlays []overlayEntry, args []string) {
	// Create base directory for overlay layers
	if err := os.MkdirAll(overlayLayersBase, 0o755); err != nil {
		fatal("create overlay layers base: %v", err)
	}

	// Prepare overlay directories (upper + work for each target)
	type overlayMount struct {
		target string
		upper  string
		work   string
	}
	var mounts []overlayMount
	for _, entry := range overlays {
		upper, work, err := prepareOverlayDirs(overlayLayersBase, entry.Path)
		if err != nil {
			fatal("prepare overlay dirs for %s: %v", entry.Path, err)
		}
		mounts = append(mounts, overlayMount{
			target: entry.Path,
			upper:  upper,
			work:   work,
		})
	}

	// Set up synchronization pipes.
	// readyR/readyW: child signals parent that unshare is done.
	// ackR/ackW: parent signals child that uid/gid maps are written.
	readyR, readyW, err := os.Pipe()
	if err != nil {
		fatal("create ready pipe: %v", err)
	}
	ackR, ackW, err := os.Pipe()
	if err != nil {
		fatal("create ack pipe: %v", err)
	}

	// Fork child process.
	// We use /proc/self/exe to re-exec the shim with the overlay child env var
	// that triggers the child overlay path.
	childArgs := []string{"__devsandbox_overlay_child"}
	childArgs = append(childArgs, args...)

	binary, err := os.Executable()
	if err != nil {
		fatal("resolve self executable: %v", err)
	}

	cmd := exec.Command(binary, childArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.ExtraFiles = []*os.File{readyW, ackR} // FD 3 = readyW, FD 4 = ackR
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
	}

	// Pass overlay mounts encoded in environment
	cmd.Env = append(os.Environ(),
		"__DEVSANDBOX_OVERLAY_CHILD=1",
		"__DEVSANDBOX_TARGET_UID="+strconv.Itoa(uid),
		"__DEVSANDBOX_TARGET_GID="+strconv.Itoa(gid),
	)
	for i, m := range mounts {
		cmd.Env = append(cmd.Env,
			fmt.Sprintf("__DEVSANDBOX_OVERLAY_%d=%s:%s:%s", i, m.target, m.upper, m.work),
		)
	}
	cmd.Env = append(cmd.Env,
		"__DEVSANDBOX_OVERLAY_COUNT="+strconv.Itoa(len(mounts)),
	)

	if err := cmd.Start(); err != nil {
		fatal("fork overlay child: %v", err)
	}
	_ = readyW.Close()
	_ = ackR.Close()

	// Wait for child to signal that unshare completed
	buf := make([]byte, 1)
	if _, err := readyR.Read(buf); err != nil {
		fatal("wait for child ready: %v", err)
	}
	_ = readyR.Close()

	// Write uid/gid maps for the child
	childPid := cmd.Process.Pid
	writeIDMap(childPid, "uid_map", fmt.Sprintf("0 0 1\n%d %d 1\n", uid, uid))
	writeFile(fmt.Sprintf("/proc/%d/setgroups", childPid), "deny")
	writeIDMap(childPid, "gid_map", fmt.Sprintf("0 0 1\n%d %d 1\n", gid, gid))

	// Signal child that maps are ready
	_, _ = ackW.Write([]byte{0})
	_ = ackW.Close()

	// Wait for child to exit and forward its exit code
	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		fatal("overlay child: %v", err)
	}
	os.Exit(0)
}

// overlayChild is called when the shim re-execs itself as the overlay child.
// It is already in the new user+mount namespace.
func overlayChild() {
	// Signal parent that unshare is done (FD 3 = readyW)
	readyW := os.NewFile(3, "readyW")
	_, _ = readyW.Write([]byte{0})
	_ = readyW.Close()

	// Wait for parent to write uid/gid maps (FD 4 = ackR)
	ackR := os.NewFile(4, "ackR")
	buf := make([]byte, 1)
	_, _ = ackR.Read(buf)
	_ = ackR.Close()

	// Parse overlay mounts from environment
	countStr := os.Getenv("__DEVSANDBOX_OVERLAY_COUNT")
	count, err := strconv.Atoi(countStr)
	if err != nil {
		fatal("parse overlay count: %v", err)
	}

	for i := range count {
		spec := os.Getenv(fmt.Sprintf("__DEVSANDBOX_OVERLAY_%d", i))
		parts := splitOverlaySpec(spec)
		if len(parts) != 3 {
			fatal("invalid overlay spec: %s", spec)
		}
		target, upper, work := parts[0], parts[1], parts[2]

		opts := buildOverlayMountOptions(target, upper, work)
		if err := syscall.Mount("overlay", target, "overlay", 0, opts); err != nil {
			fatal("mount overlay on %s: %v", target, err)
		}
	}

	// Drop privileges
	uid, _ := strconv.Atoi(os.Getenv("__DEVSANDBOX_TARGET_UID"))
	gid, _ := strconv.Atoi(os.Getenv("__DEVSANDBOX_TARGET_GID"))

	if err := syscall.Setgroups([]int{gid}); err != nil {
		fatal("setgroups: %v", err)
	}
	if err := syscall.Setgid(gid); err != nil {
		fatal("setgid: %v", err)
	}
	if err := syscall.Setuid(uid); err != nil {
		fatal("setuid: %v", err)
	}

	_ = os.Setenv("HOME", sandboxHome)
	_ = os.Setenv("USER", sandboxUser)

	// Exec the user command (remaining args after the sentinel)
	userArgs := os.Args[1:]
	if len(userArgs) == 0 {
		userArgs = []string{"/bin/bash"}
	}

	binPath, err := exec.LookPath(userArgs[0])
	if err != nil {
		fatal("command not found: %s", userArgs[0])
	}

	// Clean overlay env vars before exec
	for i := range count {
		_ = os.Unsetenv(fmt.Sprintf("__DEVSANDBOX_OVERLAY_%d", i))
	}
	_ = os.Unsetenv("__DEVSANDBOX_OVERLAY_COUNT")
	_ = os.Unsetenv("__DEVSANDBOX_OVERLAY_CHILD")
	_ = os.Unsetenv("__DEVSANDBOX_TARGET_UID")
	_ = os.Unsetenv("__DEVSANDBOX_TARGET_GID")

	env := os.Environ()
	if err := syscall.Exec(binPath, userArgs, env); err != nil {
		fatal("exec %s: %v", binPath, err)
	}
}
