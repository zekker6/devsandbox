//go:build integration_krun

// Package integration holds KVM-gated end-to-end tests for the krun (libkrun
// microVM) backend. They are behind the integration_krun build tag and the
// runtime prerequisite checks below because they require a host with /dev/kvm,
// podman, and a libkrun-enabled krun OCI runtime - hardware that standard CI
// runners and this development sandbox lack. Run them with:
//
//	task test:integration:krun
//
// On a host without /dev/kvm (or without podman/krun/curl) every test skips
// cleanly, so the target is safe to invoke anywhere.
package integration

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// binaryPath is the freshly built devsandbox binary shared by all tests.
var binaryPath string

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "devsandbox-krun-integration-*")
	if err != nil {
		panic("failed to create temp dir: " + err.Error())
	}

	root, err := projectRoot()
	if err != nil {
		panic("failed to locate project root: " + err.Error())
	}

	binaryPath = filepath.Join(tmpDir, "devsandbox")
	build := exec.Command("go", "build", "-o", binaryPath, "./cmd/devsandbox")
	build.Dir = root
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		panic("failed to build binary: " + err.Error())
	}

	code := m.Run()
	_ = os.RemoveAll(tmpDir)
	os.Exit(code)
}

// projectRoot walks up from the test's working directory until it finds the
// go.mod, so the binary build works regardless of how deep the test package is.
func projectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}

// requireKrun skips the test unless every krun prerequisite is present:
// /dev/kvm (hardware virtualization), podman, the krun OCI runtime, and curl
// (used by the egress assertions). It mirrors the CheckMicroVM preflight so a
// non-KVM host - including this devsandbox - skips instead of failing.
func requireKrun(t *testing.T) {
	t.Helper()
	if f, err := os.Open("/dev/kvm"); err != nil {
		t.Skipf("/dev/kvm not accessible (%v); krun integration tests require hardware virtualization", err)
	} else {
		_ = f.Close()
	}
	for _, bin := range []string{"podman", "krun", "curl"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%q not found in PATH; krun integration tests require podman + krun runtime + curl", bin)
		}
	}
}

// krunConfig describes the config.toml written for a test run.
type krunConfig struct {
	// proxy enables proxy mode (and therefore egress lockdown for krun).
	proxy bool
	// allowHosts is the proxy filter allowlist (exact host matches). When set,
	// the default action is "block", so anything not listed is denied.
	allowHosts []string
}

// writeConfig writes a krun config.toml into a temp XDG_CONFIG_HOME and returns
// that directory for use as XDG_CONFIG_HOME.
func writeConfig(t *testing.T, cfg krunConfig) string {
	t.Helper()
	configDir := t.TempDir()
	path := filepath.Join(configDir, "devsandbox", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}

	var b strings.Builder
	b.WriteString("[sandbox]\nisolation = \"krun\"\n\n")
	if cfg.proxy {
		b.WriteString("[proxy]\nenabled = true\n\n")
		if len(cfg.allowHosts) > 0 {
			b.WriteString("[proxy.filter]\ndefault_action = \"block\"\n\n")
			for _, host := range cfg.allowHosts {
				b.WriteString("[[proxy.filter.rules]]\n")
				b.WriteString("pattern = \"" + host + "\"\n")
				b.WriteString("action = \"allow\"\n")
				b.WriteString("scope = \"host\"\n")
				b.WriteString("type = \"exact\"\n\n")
			}
		}
	}

	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return configDir
}

// runKrun boots a krun sandbox in projectDir with the given config and runs the
// command argv inside it, returning the guest command's stdout and the run
// error. stdout and stderr are captured separately on purpose: devsandbox emits
// startup/teardown notices and the wrapper-log banner on stderr, so merging the
// streams (CombinedOutput) would pollute the kernel/HTTP-code assertions and let
// them false-pass or false-fail. Only stdout carries the guest command output;
// stderr is folded into the returned error for diagnostics on failure.
func runKrun(t *testing.T, projectDir, configDir string, argv ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(binaryPath, argv...)
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+configDir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil && stderr.Len() > 0 {
		err = fmt.Errorf("%w\ndevsandbox stderr:\n%s", err, stderr.String())
	}
	return stdout.String(), err
}

// hostKernel returns the host's `uname -r`.
func hostKernel(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("uname", "-r").Output()
	if err != nil {
		t.Fatalf("host uname -r: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// hostHasInternet reports whether the host itself can reach example.com, so the
// egress tests can skip (rather than fail) on an offline host.
func hostHasInternet() bool {
	cmd := exec.Command("curl", "-s", "-o", "/dev/null", "--max-time", "8", "https://example.com")
	return cmd.Run() == nil
}

// TestKrunIntegration_GuestKernelDiffers asserts the workload runs behind a
// distinct guest kernel - the defining property of the microVM boundary. A
// bwrap/docker sandbox shares the host kernel and would report the same release.
func TestKrunIntegration_GuestKernelDiffers(t *testing.T) {
	requireKrun(t)

	host := hostKernel(t)
	configDir := writeConfig(t, krunConfig{})
	out, err := runKrun(t, t.TempDir(), configDir, "uname", "-r")
	if err != nil {
		t.Fatalf("uname -r in krun sandbox failed: %v\nOutput: %s", err, out)
	}

	guest := strings.TrimSpace(out)
	if guest == "" {
		t.Fatalf("empty guest kernel release\nOutput: %s", out)
	}
	if guest == host {
		t.Errorf("guest kernel %q matches host %q - microVM is not running its own kernel", guest, host)
	}
}

// TestKrunIntegration_ProjectDirWritable asserts the project directory is
// writable from inside the guest and the write lands on the host (virtio-fs +
// keep-id mapping), so edits made by the workload are visible to the user.
func TestKrunIntegration_ProjectDirWritable(t *testing.T) {
	requireKrun(t)

	projectDir := t.TempDir()
	configDir := writeConfig(t, krunConfig{})

	const marker = "krun-writable-marker.txt"
	out, err := runKrun(t, projectDir, configDir, "touch", marker)
	if err != nil {
		t.Fatalf("touch in krun sandbox failed: %v\nOutput: %s", err, out)
	}

	if _, err := os.Stat(filepath.Join(projectDir, marker)); err != nil {
		t.Errorf("marker file not visible on host after guest write: %v", err)
	}
}

// TestKrunIntegration_AllowlistedEgressSucceeds asserts that, with the proxy on
// and example.com allowlisted, a request to it succeeds through the proxy.
func TestKrunIntegration_AllowlistedEgressSucceeds(t *testing.T) {
	requireKrun(t)
	if !hostHasInternet() {
		t.Skip("host has no internet; cannot validate allowlisted egress")
	}

	configDir := writeConfig(t, krunConfig{proxy: true, allowHosts: []string{"example.com"}})
	out, err := runKrun(t, t.TempDir(), configDir,
		"curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
		"--max-time", "20", "https://example.com")
	code := strings.TrimSpace(out)

	if code == "000" {
		t.Fatalf("allowlisted request to example.com failed to connect (000); err=%v", err)
	}
	if code != "200" {
		t.Errorf("allowlisted request to example.com returned %q, want 200 (err=%v)", code, err)
	}
}

// TestKrunIntegration_NonAllowlistedEgressBlocked asserts the egress boundary
// from both sides: a proxied request to a non-allowlisted host is denied by the
// filter, and a direct (proxy-bypassing) request to an external IP fails for
// lack of a route - the in-guest egress lockdown (Tasks 1-2). Both must hold,
// or untrusted code could exfiltrate past the allowlist.
func TestKrunIntegration_NonAllowlistedEgressBlocked(t *testing.T) {
	requireKrun(t)
	if !hostHasInternet() {
		t.Skip("host has no internet; cannot distinguish a block from an offline host")
	}

	configDir := writeConfig(t, krunConfig{proxy: true, allowHosts: []string{"example.com"}})

	// (a) Proxied request to a host that is not on the allowlist must not succeed.
	out, _ := runKrun(t, t.TempDir(), configDir,
		"curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
		"--max-time", "20", "https://github.com")
	if code := strings.TrimSpace(out); code == "200" {
		t.Errorf("non-allowlisted host github.com returned 200; the allowlist did not block it")
	}

	// (b) Direct-IP request bypassing the proxy must fail: the egress lockdown
	// deletes the default route, so the guest has no path to the internet
	// except the proxy gateway. A non-000 code here means egress is still open.
	out, err := runKrun(t, t.TempDir(), configDir,
		"curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
		"--noproxy", "*", "--max-time", "10", "http://1.1.1.1")
	code := strings.TrimSpace(out)
	if code != "000" {
		t.Errorf("direct-IP egress to 1.1.1.1 returned %q (err=%v); egress lockdown did not block proxy-bypassing traffic", code, err)
	}
}

// TestKrunIntegration_HostLoopbackNonProxyPortBlocked is the repro/regression for
// the host-loopback exposure (Task 3): pasta's --map-host-loopback maps EVERY
// port of the gateway (10.0.2.2) to the host's 127.0.0.1, and the egress route
// surgery keeps a /32 to the gateway on all ports. Without the port-scoped
// firewall, a guest could therefore reach any host-loopback service (not just the
// devsandbox proxy) through the gateway, bypassing the proxy filter.
//
// It starts a throwaway host-loopback listener on a non-proxy port, boots
// krun+proxy, then from inside the guest (bypassing the proxy with --noproxy)
// probes both the proxy port ($PROXY_PORT, must connect) and the host listener's
// port on the gateway (must be blocked). The proxy port stays reachable so the
// workload still works; the other loopback port must return 000.
func TestKrunIntegration_HostLoopbackNonProxyPortBlocked(t *testing.T) {
	requireKrun(t)

	// Throwaway host-loopback listener on a random non-proxy port. httptest binds
	// 127.0.0.1, which is exactly what --map-host-loopback maps the gateway onto.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	hostURL, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL %q: %v", srv.URL, err)
	}
	hostPort := hostURL.Port()
	if hostPort == "" {
		t.Fatalf("test server URL %q has no port", srv.URL)
	}

	configDir := writeConfig(t, krunConfig{proxy: true, allowHosts: []string{"example.com"}})

	// Probe both ports on the gateway from inside the guest, bypassing the proxy.
	// $PROXY_PORT is set in the guest env by the proxy wiring; the host listener's
	// port is interpolated from the host side. Emit two codes on one line.
	script := fmt.Sprintf(
		`printf '%%s %%s' `+
			`"$(curl -s -o /dev/null -w '%%{http_code}' --noproxy '*' --max-time 5 http://10.0.2.2:$PROXY_PORT)" `+
			`"$(curl -s -o /dev/null -w '%%{http_code}' --noproxy '*' --max-time 5 http://10.0.2.2:%s)"`,
		hostPort)
	out, err := runKrun(t, t.TempDir(), configDir, "sh", "-c", script)
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) != 2 {
		t.Fatalf("expected two HTTP codes (proxy-port host-port), got %q (err=%v)", out, err)
	}
	proxyCode, hostCode := fields[0], fields[1]

	// The proxy port MUST stay reachable through the gateway, otherwise the
	// firewall is too aggressive and the workload cannot reach the proxy at all.
	if proxyCode == "000" {
		t.Fatalf("guest could not reach the proxy port on the gateway (got 000); the firewall is blocking the proxy path (err=%v)", err)
	}
	// The non-proxy host-loopback port MUST be blocked, otherwise the guest can
	// reach arbitrary host-loopback services through the gateway.
	if hostCode != "000" {
		t.Errorf("guest reached host-loopback port %s via the gateway (got %q); the port-scoped firewall did not block non-proxy host-loopback services", hostPort, hostCode)
	}
}
