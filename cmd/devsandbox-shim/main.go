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
// This binary uses internal/notice for warning output; all other deps are stdlib + syscall.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"devsandbox/internal/notice"
)

const (
	sandboxUser         = "sandboxuser"
	sandboxHome         = "/home/sandboxuser"
	readySentinel       = "/tmp/.devsandbox-ready"
	overlayManifestPath = "/tmp/.devsandbox-overlays.json"

	// bakedMiseInstalls is the image's pre-baked mise installs directory. The
	// Dockerfile installs node here; seedMiseInstalls mirrors it into the
	// persistent MISE_DATA_DIR so baked tools resolve without a per-run reinstall.
	bakedMiseInstalls = "/opt/mise/installs"

	// hostMiseInstalls is the host's mise installs directory, mounted read-only
	// by the isolator on Linux hosts. LOAD-BEARING: coupled to
	// hostMiseInstallsDest in internal/sandbox/tools/mise.go.
	hostMiseInstalls = "/opt/host-mise/installs"

	// miseReshimTimeout bounds the post-seed `mise reshim` so a misbehaving mise
	// (e.g. attempting network resolution in an egress-locked guest) cannot hang
	// the boot; on timeout the workload still starts, just without fresh shims.
	miseReshimTimeout = 30 * time.Second
)

func main() {
	_ = notice.Setup("/tmp/devsandbox-shim.log", os.Getenv("DEVSANDBOX_DEBUG") != "", nil)
	defer notice.Flush()

	// Check if this is a re-exec as the overlay child process
	if os.Getenv("__DEVSANDBOX_OVERLAY_CHILD") == "1" {
		overlayChild()
		return // unreachable — overlayChild calls syscall.Exec
	}

	uid, gid := getHostIDs()

	// krun egress lockdown is applied HOST-side (the isolator locks this microVM's
	// pasta-netns routing to the proxy gateway). Wait for that to complete BEFORE
	// running anything guest-influenced - the mise setup below execs the guest's
	// (persistent, guest-writable) mise binary and data dir, so a prior untrusted
	// run's planted code must not run while direct egress is still open, nor be
	// able to plant the go-signal sentinel itself. Nothing between here and the
	// workload needs the network, so gating first costs nothing. Fail-closed:
	// abort if the host does not confirm in time.
	if egressLockdownRequested() {
		if err := waitForEgressReady(); err != nil {
			fatal("egress lockdown not confirmed (fail-closed): %v", err)
		}
	}

	ensureUser(uid, gid)
	preferHostMise(uid, gid)
	// NOTE: .env hiding is now handled at container creation via Docker volume
	// mounts (no CAP_SYS_ADMIN needed).
	miseTrust(uid, gid)
	setupCacheDirs(uid, gid)
	setupDirectories(uid, gid)
	// Seed baked installs first: on a version present in both sources the baked
	// copy wins (image-local ext4 beats virtiofs I/O), host installs fill the rest.
	miseDataDir := sandboxHome + "/.local/share/mise"
	seeded := seedMiseInstalls(bakedMiseInstalls, miseDataDir, uid, gid)
	seeded += seedMiseInstalls(hostMiseInstalls, miseDataDir, uid, gid)
	// Reshim when something new was seeded, and also when the active mise
	// binary changed since the previous boot (preferHostMise may have swapped
	// it): shims persist in the sandbox home, and different mise versions can
	// disagree on which seeded tools they can shim, so stale shims must be
	// regenerated once under the current binary.
	miseVer := miseVersion(uid, gid)
	markerPath := filepath.Join(miseDataDir, ".devsandbox-mise-version")
	prevVer, _ := os.ReadFile(markerPath)
	if seeded > 0 || (miseVer != "" && miseVer != string(prevVer)) {
		reshimMise(uid, gid)
		if miseVer != "" {
			if err := os.WriteFile(markerPath, []byte(miseVer), 0o644); err != nil {
				warn("write mise version marker: %v", err)
			}
			_ = os.Lchown(markerPath, uid, gid)
		}
	}
	suppressSSHAgent()
	writeReadySentinel()

	// Load overlay manifest — the Docker isolator writes this when tmpoverlay
	// bindings exist. If present, we either copy (macOS) or fork with overlayfs (Linux).
	overlays := loadOverlayManifest(overlayManifestPath)

	// Separate overlay types: copyoverlay (macOS) vs tmpoverlay (Linux overlayfs)
	var copyOverlays, fsOverlays []overlayEntry
	for _, entry := range overlays {
		if entry.Type == "copyoverlay" {
			copyOverlays = append(copyOverlays, entry)
		} else {
			fsOverlays = append(fsOverlays, entry)
		}
	}

	// Apply copy overlays — no namespaces needed, just copy from shadow source.
	// Clean the target first so a previous run's writes do not persist (krun on any
	// OS, and the Docker backend on macOS, degrade tmpoverlay to a copy into the
	// persistent home); the clean is mount-aware so nested read-only bindings under
	// the target are preserved.
	for _, entry := range copyOverlays {
		if err := cleanCopyOverlayTarget(sandboxHome, entry.Path); err != nil {
			fatal("clean copy overlay target %s: %v", entry.Path, err)
		}
		if err := copyDir(sandboxHome, entry.Source, entry.Path); err != nil {
			fatal("copy overlay %s → %s: %v", entry.Source, entry.Path, err)
		}
		if err := chownRecursive(entry.Path, uid, gid); err != nil {
			warn("chown copy overlay %s: %v", entry.Path, err)
		}
	}

	// The remaining args after the entrypoint are the command to run
	args := os.Args[1:]
	if len(args) == 0 {
		args = []string{"/bin/bash"}
	}

	if len(fsOverlays) > 0 {
		execWithOverlays(uid, gid, fsOverlays, args)
		// execWithOverlays does not return on success
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
//
// In the krun keep-id case the host UID/GID are already provisioned in the
// image's /etc/passwd, so groupadd/useradd would fail and print noise. We check
// with getent first and skip creation when the ID already exists; the docker /
// non-keep-id case (ID absent) still creates them. Create errors stay non-fatal —
// the ID may be claimed under a different name we did not detect.
func ensureUser(uid, gid int) {
	if argv := groupCreateArgs(gid, getentExists); argv != nil {
		if err := run(argv[0], argv[1:]...); err != nil {
			warn("create sandbox group (gid %d): %v (the group may be claimed under another name)", gid, err)
		}
	}
	if argv := userCreateArgs(uid, gid, getentExists); argv != nil {
		if err := run(argv[0], argv[1:]...); err != nil {
			warn("create sandbox user (uid %d): %v (the uid may be claimed under another name; USER will reflect the real account)", uid, err)
		}
	}

	// Fix ownership of sandbox home
	if err := chownRecursive(sandboxHome, uid, gid); err != nil {
		warn("chown sandbox home: %v", err)
	}
}

// idExistsFunc reports whether getent finds an entry for key in the given
// database ("group" or "passwd"). It is a parameter so the create-arg helpers
// stay unit-testable without a real getent.
type idExistsFunc func(database, key string) bool

// getentExists is the production idExistsFunc: it shells out to getent, which
// exits 0 when the entry exists and non-zero otherwise, so a nil error means
// "found". A missing getent (or any other error) is treated as "not found",
// falling through to the create path and preserving the original behavior.
func getentExists(database, key string) bool {
	return exec.Command("getent", database, key).Run() == nil
}

// passwdName returns the login name for uid from `getent passwd <uid>`, or "" when
// there is no entry (or getent is unavailable). Used so USER names the account we
// actually drop to rather than asserting "sandboxuser", which may be wrong when
// the uid was provisioned under another name (keep-id) or our own useradd was a
// no-op on a name collision.
func passwdName(uid int) string {
	out, err := exec.Command("getent", "passwd", strconv.Itoa(uid)).Output()
	if err != nil {
		return ""
	}
	if i := strings.IndexByte(string(out), ':'); i > 0 {
		return string(out)[:i]
	}
	return ""
}

// groupCreateArgs returns the groupadd argv for the sandbox group, or nil when a
// group with gid already exists (keep-id case) so creation is skipped quietly.
func groupCreateArgs(gid int, exists idExistsFunc) []string {
	if exists("group", strconv.Itoa(gid)) {
		return nil
	}
	return []string{"groupadd", "-g", strconv.Itoa(gid), sandboxUser}
}

// userCreateArgs returns the useradd argv for the sandbox user, or nil when a
// user with uid already exists (keep-id case) so creation is skipped quietly.
func userCreateArgs(uid, gid int, exists idExistsFunc) []string {
	if exists("passwd", strconv.Itoa(uid)) {
		return nil
	}
	return []string{
		"useradd",
		"-u", strconv.Itoa(uid),
		"-g", strconv.Itoa(gid),
		"-m",
		"-d", sandboxHome,
		"-s", "/bin/bash",
		sandboxUser,
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

// seedMiseInstalls mirrors a mise installs directory (the image's pre-baked
// /opt/mise or the host's installs shadow-mounted by the isolator) into the
// persistent MISE_DATA_DIR (the sandbox home), so a tool the source already
// ships - the baked node the AI CLIs run on, or a host-installed toolchain -
// resolves instantly instead of being reinstalled on every fresh guest, while
// any tool version the project installs itself lands in a real directory in
// the sandbox home and persists across runs.
//
// The container backends point MISE_DATA_DIR at the sandbox home (bind-mounted,
// persistent) rather than the ephemeral image path, so without this seed the
// baked node would be orphaned: mise would see the version its system/global
// config pins as missing in the empty home and stall the guest reinstalling it
// (badly so alongside a large global config that drags in unresolvable @latest
// tools). Seeding restores instant resolution without giving up persistence.
//
// Seed shape: installs/<tool>/<version> is a REAL directory whose child
// entries are symlinks into the source. Both directory levels must be real:
// symlinking the whole installs/<tool> dir would redirect project-installed
// versions back into the ephemeral image path (defeating persistence), and a
// symlinked <version> dir breaks mise's aqua/ubi bin-path discovery - tools
// with a nested layout (uv, golangci-lint, gh, ...) fail with "couldn't exec
// process" even though the content resolves (reproduced on the host with a
// plain symlinked version dir; child-level symlinks fix it).
//
// A version dir that already exists is never touched - it is either a real
// install the project made (which must win) or a complete prior seed - EXCEPT
// when it is a symlink into this same source: that is this seed's own pre-
// child-level shape, which is replaced in place (migration). Source version
// entries that are symlinks (version aliases like "latest" or "22") are
// skipped entirely: their targets are often host-absolute (dangling in the
// guest), and mise resolves partial versions against the real version dir
// names anyway. Child entries that do not resolve in the guest are skipped for
// the same reason. Tool-level regular files (.mise.backend metadata) are
// symlinked as before.
//
// Seeding is best-effort: a failure is logged (never silent) but not fatal, since
// mise can still reinstall the tool - slower, but correct. srcInstalls and
// miseDataDir are parameters so the logic is unit-testable without /opt/mise.
// The number of symlinks created is returned so the caller can regenerate the
// mise shims only when something new appeared.
func seedMiseInstalls(srcInstalls, miseDataDir string, uid, gid int) int {
	tools, err := os.ReadDir(srcInstalls)
	if err != nil {
		// No source installs (or unreadable): nothing to seed. Only surface a real
		// error, not the expected "nothing mounted here" case.
		if !errors.Is(err, fs.ErrNotExist) {
			warn("seed mise installs %s: read: %v", srcInstalls, err)
		}
		return 0
	}

	// The installs/ parent is created here by root (the shim runs privileged), but
	// setupDirectories' chown pass already ran, so it must be handed to the user or
	// the workload (dropped to that user) could not create installs/<tool> for a
	// tool the image did not bake.
	installsDir := filepath.Join(miseDataDir, "installs")
	if err := os.MkdirAll(installsDir, 0o755); err != nil {
		warn("seed mise installs %s: mkdir %s: %v", srcInstalls, installsDir, err)
		return 0
	}
	_ = os.Lchown(installsDir, uid, gid)

	created := 0
	for _, tool := range tools {
		if !tool.IsDir() {
			continue
		}
		srcToolDir := filepath.Join(srcInstalls, tool.Name())
		dstToolDir := filepath.Join(installsDir, tool.Name())

		if err := os.MkdirAll(dstToolDir, 0o755); err != nil {
			warn("seed mise installs %s: mkdir %s: %v", srcInstalls, dstToolDir, err)
			continue
		}
		_ = os.Lchown(dstToolDir, uid, gid)

		entries, err := os.ReadDir(srcToolDir)
		if err != nil {
			warn("seed mise installs %s: read %s: %v", srcInstalls, srcToolDir, err)
			continue
		}
		for _, e := range entries {
			srcPath := filepath.Join(srcToolDir, e.Name())
			dstPath := filepath.Join(dstToolDir, e.Name())

			if e.Type()&fs.ModeSymlink != 0 {
				// Version alias (latest, major.minor, ...): skip, see doc comment.
				continue
			}

			if !e.IsDir() {
				// Tool-level metadata file: symlink if absent.
				created += seedSymlink(srcInstalls, srcPath, dstPath, uid, gid)
				continue
			}

			// Real version directory: materialize as a real dir with symlinked
			// children. An existing dst is left alone unless it is this seed's own
			// old version-level symlink (target under this source), which is
			// migrated to the child-level shape.
			if fi, err := os.Lstat(dstPath); err == nil {
				if fi.Mode()&fs.ModeSymlink == 0 {
					continue // real install or complete prior seed: never clobber
				}
				target, rerr := os.Readlink(dstPath)
				if rerr != nil || target != srcPath {
					continue // foreign symlink (e.g. the other seed source): keep
				}
				if err := os.Remove(dstPath); err != nil {
					warn("seed mise installs %s: migrate %s: %v", srcInstalls, dstPath, err)
					continue
				}
			} else if !errors.Is(err, fs.ErrNotExist) {
				warn("seed mise installs %s: stat %s: %v", srcInstalls, dstPath, err)
				continue
			}

			children, err := os.ReadDir(srcPath)
			if err != nil {
				warn("seed mise installs %s: read %s: %v", srcInstalls, srcPath, err)
				continue
			}
			if err := os.MkdirAll(dstPath, 0o755); err != nil {
				warn("seed mise installs %s: mkdir %s: %v", srcInstalls, dstPath, err)
				continue
			}
			_ = os.Lchown(dstPath, uid, gid)
			for _, c := range children {
				created += seedSymlink(srcInstalls, filepath.Join(srcPath, c.Name()), filepath.Join(dstPath, c.Name()), uid, gid)
			}
		}
	}
	return created
}

// seedSymlink creates dst as a symlink to src for seedMiseInstalls, returning 1
// when a link was created. Entries that do not resolve in the guest (dangling
// host-absolute symlinks) are skipped - a link to one would dangle too - and an
// existing dst is never clobbered: it is either a prior seed or part of a real
// install, both of which must win.
func seedSymlink(srcInstalls, src, dst string, uid, gid int) int {
	if _, err := os.Stat(src); err != nil {
		return 0
	}
	if _, err := os.Lstat(dst); err == nil {
		return 0
	} else if !errors.Is(err, fs.ErrNotExist) {
		warn("seed mise installs %s: stat %s: %v", srcInstalls, dst, err)
		return 0
	}
	if err := os.Symlink(src, dst); err != nil {
		warn("seed mise installs %s: symlink %s: %v", srcInstalls, dst, err)
		return 0
	}
	_ = os.Lchown(dst, uid, gid)
	return 1
}

// miseVersion returns `mise --version` output (trimmed) for the mise currently
// on PATH, or "" on any failure. Offline and time-bounded: it feeds the reshim
// version marker, never blocks the boot.
func miseVersion(uid, gid int) string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "mise", "--version")
	cmd.Env = append(os.Environ(), "MISE_OFFLINE=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{
			Uid: uint32(uid),
			Gid: uint32(gid),
		},
	}
	out, err := cmd.Output()
	if err != nil {
		warn("mise --version: %v", err)
		return ""
	}
	return strings.TrimSpace(string(out))
}

// reshimMise regenerates the shims in the persistent MISE_DATA_DIR after new
// versions were seeded. Without it a freshly seeded tool is invisible to
// non-interactive shells (the AI CLIs spawn `bash -c`, which never runs `mise
// activate` and resolves tools purely through the shims directory on PATH).
// Best-effort and time-bounded: on failure or timeout the workload still
// starts, and interactive shells (mise activate) plus `mise x` keep working.
//
// Runs with MISE_OFFLINE=1: shim generation is a purely local operation, but
// resolving the mounted host global config's `@latest` tool specs would make
// mise fetch remote version lists - each hanging to its timeout in an
// egress-locked guest, and this runs before the workload so the stall is pure
// added boot latency. Offline mode makes mise use installed/cached data only.
func reshimMise(uid, gid int) {
	ctx, cancel := context.WithTimeout(context.Background(), miseReshimTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "mise", "reshim")
	cmd.Env = append(os.Environ(), "MISE_OFFLINE=1")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{
			Uid: uint32(uid),
			Gid: uint32(gid),
		},
	}
	if err := cmd.Run(); err != nil {
		warn("mise reshim: %v", err)
	}
}

// imageMiseBin is the image's baked mise binary; preferHostMise replaces it
// with a symlink to the host's own mise when one is mounted and runs here.
const imageMiseBin = "/usr/local/bin/mise"

// preferHostMise makes the guest use the HOST's mise binary (mounted read-only
// via ~/.local/bin) instead of the image's baked one, so mise semantics inside
// the sandbox match the host exactly. The image bakes the latest mise at build
// time, and a guest mise NEWER than the host's breaks host-installed tools:
// newer mise re-maps a tool's stored backend to its current registry default
// ("backend for 'bat' changed from stored 'ubi:sharkdp/bat' to registry
// 'aqua:sharkdp/bat'") and then derives bin paths from the NEW backend's
// expected archive layout, which does not match what the stored backend put on
// disk - the seeded tool resolves to a nonexistent path and gets no shim.
// Version parity makes that class of skew impossible.
//
// The swap is probe-gated and best-effort: the host binary must actually run
// in the guest (it will not on a macOS host, or if it needs a newer glibc than
// the guest ships) or the image's mise is kept. mise's official builds target
// a very old glibc baseline, so on Linux hosts the probe passing is the norm.
func preferHostMise(uid, gid int) {
	hostMise := sandboxHome + "/.local/bin/mise"
	if _, err := os.Stat(hostMise); err != nil {
		return // host mise not mounted: keep the image's
	}

	// Probe as the (already provisioned) sandbox user, time-bounded so a
	// wedged binary cannot hang the boot.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	probe := exec.CommandContext(ctx, hostMise, "version")
	// Offline: `mise version` performs a best-effort update check over the
	// network, which in an egress-locked guest would eat the probe timeout.
	probe.Env = append(os.Environ(), "MISE_OFFLINE=1")
	probe.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{
			Uid: uint32(uid),
			Gid: uint32(gid),
		},
	}
	if err := probe.Run(); err != nil {
		warn("host mise %s does not run in the guest (%v); using the image's mise", hostMise, err)
		return
	}

	// Symlink-then-rename so the swap is atomic: a failure part-way can never
	// leave the guest with no mise at all.
	tmp := imageMiseBin + ".host"
	_ = os.Remove(tmp)
	if err := os.Symlink(hostMise, tmp); err != nil {
		warn("prefer host mise: symlink %s: %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, imageMiseBin); err != nil {
		warn("prefer host mise: rename over %s: %v", imageMiseBin, err)
		_ = os.Remove(tmp)
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

	// Set HOME/USER for the new user. Resolve USER from the passwd entry for the
	// uid we drop to so it names the real account even when the uid was provisioned
	// under a name other than sandboxuser (keep-id, or a name collision that made
	// our own useradd a no-op); fall back to sandboxUser only when there is no entry.
	userName := sandboxUser
	if n := passwdName(uid); n != "" {
		userName = n
	}
	_ = os.Setenv("HOME", sandboxHome)
	_ = os.Setenv("USER", userName)

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

type overlayEntry struct {
	Path   string `json:"path"`
	Type   string `json:"type"`
	Source string `json:"source,omitempty"`
}

type overlayManifest struct {
	Overlays []overlayEntry `json:"overlays"`
}

func parseOverlayManifest(data []byte, m *overlayManifest) error {
	return json.Unmarshal(data, m)
}

// loadOverlayManifest reads the overlay manifest and returns validated entries.
// Returns an empty slice if the manifest file does not exist.
// Fatals if the file exists but is malformed.
// Skips entries whose path is not an existing directory.
func loadOverlayManifest(path string) []overlayEntry {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		fatal("read overlay manifest: %v", err)
	}

	var m overlayManifest
	if err := parseOverlayManifest(data, &m); err != nil {
		fatal("parse overlay manifest: %v", err)
	}

	// Filter to existing directories only (overlayfs requires dirs)
	var valid []overlayEntry
	for _, entry := range m.Overlays {
		info, err := os.Stat(entry.Path)
		if err != nil || !info.IsDir() {
			continue
		}
		valid = append(valid, entry)
	}
	return valid
}

func warn(format string, args ...any) {
	notice.Warn("devsandbox-shim: "+format, args...)
}

// fatal writes directly to stderr (bypassing notice) because it calls os.Exit(1)
// immediately after — buffered or log-routed output may never flush in time.
func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "devsandbox-shim: fatal: "+format+"\n", args...)
	os.Exit(1)
}
