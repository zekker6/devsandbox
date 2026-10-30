// Package seatbelt generates deny-by-default macOS sandbox profiles.
// Callers must resolve filesystem aliases before constructing a Policy: the
// generator validates path spelling but does not inspect the host filesystem.
package seatbelt

import (
	"context"
	"fmt"
	"os"
	"path"
	"runtime"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Executable is the system-provided Seatbelt launcher. Never resolve it via PATH.
const Executable = "/usr/bin/sandbox-exec"

// Available reports whether the native macOS launcher is installed.
func Available() error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("seatbelt is only supported on macOS")
	}
	info, err := os.Stat(Executable)
	if err != nil {
		return fmt.Errorf("seatbelt launcher %s: %w", Executable, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("seatbelt launcher %s is not an executable regular file", Executable)
	}
	return nil
}

// Check compiles and applies profile to /usr/bin/true, without inheriting host
// environment variables or working directory. It never falls back to an
// unsandboxed execution. Successful compilation is not an enforcement test.
func Check(ctx context.Context, profile string) error {
	if err := Available(); err != nil {
		return err
	}
	cmd := checkCommand(ctx, profile)
	stderr := boundedOutput{limit: 4096}
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("seatbelt profile check: %w", ctx.Err())
		}
		return fmt.Errorf("seatbelt profile check failed: %w; stderr: %s", err, stderr.String())
	}
	return nil
}

// Policy describes the filesystem and networking available to a workload.
// Paths must be clean, absolute POSIX paths. ReadOnly paths remain read-only
// even beneath Writable paths. DenyRead also forbids writing, and all denials
// override grants. Writable roots, UnixSocketRoots, ReadOnly paths, and denied
// paths cannot be moved by renaming an ancestor. Network permits outbound IP
// connections only.
// A nonzero ProxyPort restricts them to IPv4 TCP on that localhost port.
// UnixSocketRoots permits Unix socket bind, connect, and inbound operations only
// beneath the supplied private directories, independently of Network. Callers
// must supply canonical private roots, never host socket directories. Filesystem
// access still requires ReadOnly or Writable grants. Mach services are not granted.
//
// A profile does not revoke already-open descriptors. The launcher must close
// unwanted descriptors and sanitize its environment before starting a workload.
// ReadOnly does not protect executable content beneath a Writable directory
// unless that content is itself listed as ReadOnly or DenyWrite.
type Policy struct {
	ReadOnly        []string
	Writable        []string
	DenyRead        []string
	DenyWrite       []string
	UnixSocketRoots []string
	ProxyPort       int
	Network         bool
	HideEnvFiles    bool
	ProtectConfig   bool
	ConfigHidden    bool
}

// Profile renders a self-contained SBPL profile without reading host state.
func (p Policy) Profile() (string, error) {
	if p.ProxyPort < 0 || p.ProxyPort > 65535 {
		return "", fmt.Errorf("seatbelt proxy port must be between 0 and 65535: %d", p.ProxyPort)
	}
	if p.ProxyPort != 0 && !p.Network {
		return "", fmt.Errorf("seatbelt proxy port requires network access")
	}
	for _, list := range []struct {
		name  string
		paths []string
	}{
		{"read-only", p.ReadOnly}, {"writable", p.Writable},
		{"deny-read", p.DenyRead}, {"deny-write", p.DenyWrite},
		{"unix-socket", p.UnixSocketRoots},
	} {
		for _, name := range list.paths {
			if err := validatePath(name); err != nil {
				return "", fmt.Errorf("seatbelt %s path %q: %w", list.name, name, err)
			}
		}
	}

	var b strings.Builder
	b.WriteString(baseProfile)
	for _, name := range p.ReadOnly {
		pathRule(&b, "allow", "file-read*", "subpath", name)
	}
	for _, name := range p.Writable {
		pathRule(&b, "allow", "file-read* file-write*", "subpath", name)
	}
	if len(p.UnixSocketRoots) > 0 {
		// socket() has no path to filter. Bind/connect/accept remain confined
		// to explicit Unix paths; none of these rules grants IP listening.
		b.WriteString("(allow system-socket (socket-domain AF_UNIX))\n")
		for _, name := range p.UnixSocketRoots {
			fmt.Fprintf(&b, "(allow network-bind network-inbound (local unix-socket (subpath %s)))\n", sbplString(name))
			fmt.Fprintf(&b, "(allow network-outbound (remote unix-socket (subpath %s)))\n", sbplString(name))
		}
	}
	if p.Network {
		if p.ProxyPort != 0 {
			remote := fmt.Sprintf("localhost:%d", p.ProxyPort)
			fmt.Fprintf(&b, "(allow network-outbound (require-all (socket-domain AF_INET) (remote tcp %s)))\n", sbplString(remote))
		} else {
			b.WriteString("(allow network-outbound (remote ip \"*:*\"))\n")
		}
	}

	// Explicit denials come last, including ancestor unlink rules. Protecting
	// only the named entry leaves it movable through a writable parent.
	ancestors := make(map[string]struct{})
	protectAncestors := func(name string) {
		for {
			ancestors[name] = struct{}{}
			if name == "/" {
				break
			}
			name = path.Dir(name)
		}
	}
	// A writable root is persistent sandbox state, not a replaceable entry.
	for _, name := range p.Writable {
		protectAncestors(name)
	}
	for _, name := range p.UnixSocketRoots {
		protectAncestors(name)
	}
	for _, list := range []struct {
		operations string
		paths      []string
	}{
		{"file-write*", p.ReadOnly},
		{"file-read* file-write*", p.DenyRead},
		{"file-write*", p.DenyWrite},
	} {
		for _, name := range list.paths {
			pathRule(&b, "deny", list.operations, "subpath", name)
			protectAncestors(path.Dir(name))
		}
	}
	parents := make([]string, 0, len(ancestors))
	for name := range ancestors {
		parents = append(parents, name)
	}
	sort.Strings(parents)
	for _, name := range parents {
		pathRule(&b, "deny", "file-write-unlink", "literal", name)
	}
	if p.HideEnvFiles {
		b.WriteString("(deny file-read* file-write* (regex #\"(^|/)[.][eE][nN][vV]([.][^/]*)?(/|$)\"))\n")
	}
	if p.ProtectConfig || p.ConfigHidden {
		operations := "file-write*"
		if p.ConfigHidden {
			operations = "file-read* file-write*"
		}
		fmt.Fprintf(&b, "(deny %s (regex #\"(^|/)[.][dD][eE][vV][sS][aA][nN][dD][bB][oO][xX][.][tT][oO][mM][lL](/|$)\"))\n", operations)
	}
	return b.String(), nil
}

func validatePath(name string) error {
	if !utf8.ValidString(name) {
		return fmt.Errorf("invalid UTF-8")
	}
	if !path.IsAbs(name) || path.Clean(name) != name {
		return fmt.Errorf("must be an absolute, clean POSIX path")
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return fmt.Errorf("control characters are not allowed")
		}
	}
	return nil
}

// SBPL strings need only quote and backslash escaping here: validatePath has
// already rejected control characters. Go's \x and \u escapes are not Scheme.
func sbplString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func pathRule(b *strings.Builder, action, operations, filter, name string) {
	fmt.Fprintf(b, "(%s %s (%s %s))\n", action, operations, filter, sbplString(name))
}

// No home, temporary directory, volume, Mach service, keychain, or broad
// /private/var access belongs in this baseline. Runtime/tool files outside
// these system roots must be supplied explicitly by the caller.
const baseProfile = `(version 1)
(deny default)
(allow process-exec)
(allow process-fork)
(allow signal (target same-sandbox))
(allow process-info* (target same-sandbox))
(allow sysctl-read
  (sysctl-name "hw.activecpu")
  (sysctl-name "hw.byteorder")
  (sysctl-name "hw.cachelinesize")
  (sysctl-name "hw.cpufamily")
  (sysctl-name "hw.cputype")
  (sysctl-name "hw.cpusubtype")
  (sysctl-name "hw.logicalcpu")
  (sysctl-name "hw.logicalcpu_max")
  (sysctl-name "hw.machine")
  (sysctl-name "hw.memsize")
  (sysctl-name "hw.ncpu")
  (sysctl-name "hw.pagesize")
  ; libc getpagesize/sysconf uses numeric HW_PAGESIZE, named pagesize_compat.
  (sysctl-name "hw.pagesize_compat")
  (sysctl-name "hw.physicalcpu")
  (sysctl-name "hw.physicalcpu_max")
  (sysctl-name-prefix "hw.optional.arm.")
  (sysctl-name-prefix "hw.optional.armv8_")
  (sysctl-name "kern.argmax")
  (sysctl-name "kern.osrelease")
  (sysctl-name "kern.ostype")
  (sysctl-name "kern.osversion")
  (sysctl-name "kern.version")
  (sysctl-name "kern.maxfilesperproc")
  (sysctl-name "kern.usrstack64"))
; dyld/libignition needs root-directory reads during startup, not recursive access.
(allow file-read* (literal "/"))
(allow file-read*
  (subpath "/System")
  (subpath "/usr")
  (subpath "/bin")
  (subpath "/sbin")
  (subpath "/Library/Apple")
  (subpath "/Library/Developer")
  (subpath "/Applications/Xcode.app")
  (subpath "/opt/homebrew")
  (subpath "/private/etc/ssl")
  (literal "/private/etc/passwd")
  (literal "/private/etc/group")
  (literal "/private/etc/hosts")
  (literal "/private/etc/resolv.conf")
  (literal "/private/etc/localtime")
  (literal "/private/etc/protocols")
  (literal "/private/etc/services")
  (subpath "/private/var/db/dyld")
  (subpath "/private/var/db/timezone"))
(allow file-read* file-write-data file-ioctl
  (require-all
    (require-any
      (literal "/dev/null")
      (literal "/dev/zero")
      (literal "/dev/random")
      (literal "/dev/urandom")
      (literal "/dev/tty"))
    (vnode-type CHARACTER-DEVICE)))
(allow file-read* file-write-data
  (literal "/dev/stdin")
  (literal "/dev/stdout")
  (literal "/dev/stderr")
  (literal "/dev/fd/0")
  (literal "/dev/fd/1")
  (literal "/dev/fd/2"))
(allow pseudo-tty)
(allow file-read* file-write* file-ioctl (literal "/dev/ptmx"))
(allow file-read* file-write*
  (require-all
    (regex #"^/dev/ttys[0-9]+$")
    (extension "com.apple.sandbox.pty")))
(allow file-ioctl (regex #"^/dev/ttys[0-9]+$"))
`
