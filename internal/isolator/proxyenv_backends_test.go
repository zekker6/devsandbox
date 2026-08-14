package isolator

import (
	"slices"
	"strings"
	"testing"

	"devsandbox/internal/proxyenv"
	"devsandbox/internal/sandbox"
)

const (
	testProxyHost   = "10.0.2.2"
	testProxyPort   = 8080
	testProxyURL    = "http://10.0.2.2:8080"
	bwrapCADest     = "/tmp/devsandbox-ca.crt"
	dockerCADest    = "/etc/ssl/certs/devsandbox-ca.crt"
	miseTimeoutName = "MISE_FETCH_REMOTE_VERSIONS_TIMEOUT"
)

// sharedVarNames lists, in order, every variable the shared source produces for
// a proxied MITM run - the sequence both backends must render.
func sharedVarNames(extraEnv, extraCAEnv []string) []string {
	vars := proxyenv.Vars(testProxyURL, extraEnv)
	vars = append(vars, proxyenv.CAVars("ignored", extraCAEnv)...)
	names := make([]string, 0, len(vars))
	for _, v := range vars {
		names = append(names, v.Name)
	}
	return names
}

// dockerEnv returns the `-e NAME=VALUE` pairs in the order they appear.
func dockerEnv(t *testing.T, args []string) []proxyenv.Var {
	t.Helper()
	var out []proxyenv.Var
	for i := 0; i < len(args)-1; i++ {
		if args[i] != "-e" {
			continue
		}
		name, value, ok := strings.Cut(args[i+1], "=")
		if !ok {
			t.Fatalf("malformed -e argument %q", args[i+1])
		}
		out = append(out, proxyenv.Var{Name: name, Value: value})
	}
	return out
}

// bwrapEnv returns the `--setenv NAME VALUE` triples in the order they appear.
func bwrapEnv(args []string) []proxyenv.Var {
	var out []proxyenv.Var
	for i := 0; i < len(args)-2; i++ {
		if args[i] == "--setenv" {
			out = append(out, proxyenv.Var{Name: args[i+1], Value: args[i+2]})
		}
	}
	return out
}

// filterShared keeps only the variables the shared source owns, dropping each
// backend's own additions (docker's PROXY_MODE/PROXY_HOST/PROXY_PORT, HOST_UID).
func filterShared(vars []proxyenv.Var, shared []string) []proxyenv.Var {
	owned := make(map[string]bool, len(shared))
	for _, name := range shared {
		owned[name] = true
	}
	var out []proxyenv.Var
	for _, v := range vars {
		if owned[v.Name] {
			out = append(out, v)
		}
	}
	return out
}

func varNames(vars []proxyenv.Var) []string {
	names := make([]string, 0, len(vars))
	for _, v := range vars {
		names = append(names, v.Name)
	}
	return names
}

func valueOf(vars []proxyenv.Var, name string) (string, bool) {
	// Last wins for both backends: `-e` is last-wins in docker, and the bwrap
	// builder replaces a --setenv entry in place.
	for _, v := range slices.Backward(vars) {
		if v.Name == name {
			return v.Value, true
		}
	}
	return "", false
}

func bwrapProxyArgs(t *testing.T, cfg *sandbox.Config) []proxyenv.Var {
	t.Helper()
	b := sandbox.NewBuilder(cfg)
	b.AddProxyEnvironment()
	return bwrapEnv(b.Build())
}

func dockerProxyArgs(t *testing.T, cfg *Config) []proxyenv.Var {
	t.Helper()
	iso := NewDockerIsolator(DockerConfig{})
	iso.imageTag = "test:latest"
	args, err := iso.buildCommonArgs(cfg)
	if err != nil {
		t.Fatalf("buildCommonArgs: %v", err)
	}
	return dockerEnv(t, args)
}

// TestProxyEnv_BackendsRenderTheSameNamesInOrder is the drift guard: a variable
// added to internal/proxyenv must reach both backends, and one added to a single
// backend must fail here rather than yielding a sandbox that proxies under bwrap
// and bypasses the proxy under docker/krun.
func TestProxyEnv_BackendsRenderTheSameNamesInOrder(t *testing.T) {
	extraEnv := []string{"MY_TOOL_PROXY"}
	extraCAEnv := []string{"MY_TOOL_CA_BUNDLE"}
	want := sharedVarNames(extraEnv, extraCAEnv)

	bwrap := bwrapProxyArgs(t, &sandbox.Config{
		ProxyEnabled:    true,
		ProxyMITM:       true,
		ProxyPort:       testProxyPort,
		GatewayIP:       testProxyHost,
		ProxyExtraEnv:   extraEnv,
		ProxyExtraCAEnv: extraCAEnv,
	})
	docker := dockerProxyArgs(t, &Config{
		ProjectDir:      "/tmp/test-project",
		SandboxHome:     "/tmp/test-sandbox",
		HomeDir:         "/home/testuser",
		Shell:           "/bin/bash",
		ProxyEnabled:    true,
		ProxyHost:       testProxyHost,
		ProxyPort:       testProxyPort,
		ProxyCAPath:     "/tmp/devsandbox-ca-src.crt",
		ProxyExtraEnv:   extraEnv,
		ProxyExtraCAEnv: extraCAEnv,
	})

	for _, tc := range []struct {
		backend string
		got     []proxyenv.Var
	}{
		{"bwrap", bwrap},
		{"docker", docker},
	} {
		got := varNames(filterShared(tc.got, want))
		if len(got) != len(want) {
			t.Fatalf("%s rendered %d shared vars %v, want %d %v", tc.backend, len(got), got, len(want), want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s var[%d] = %q, want %q (full order: %v)", tc.backend, i, got[i], want[i], got)
			}
		}
	}
}

// TestProxyEnv_BackendsKeepTheirOwnCAPath asserts the shared list does not flatten
// the one thing that legitimately differs: bwrap mounts the CA under /tmp because
// /etc/ssl is a read-only bind there, docker under /etc/ssl/certs.
func TestProxyEnv_BackendsKeepTheirOwnCAPath(t *testing.T) {
	caNames := []string{
		"REQUESTS_CA_BUNDLE",
		"NODE_EXTRA_CA_CERTS",
		"CURL_CA_BUNDLE",
		"GIT_SSL_CAINFO",
		"SSL_CERT_FILE",
		"MY_TOOL_CA_BUNDLE",
	}

	bwrap := bwrapProxyArgs(t, &sandbox.Config{
		ProxyEnabled:    true,
		ProxyMITM:       true,
		ProxyPort:       testProxyPort,
		GatewayIP:       testProxyHost,
		ProxyExtraCAEnv: []string{"MY_TOOL_CA_BUNDLE"},
	})
	docker := dockerProxyArgs(t, &Config{
		ProjectDir:      "/tmp/test-project",
		SandboxHome:     "/tmp/test-sandbox",
		HomeDir:         "/home/testuser",
		Shell:           "/bin/bash",
		ProxyEnabled:    true,
		ProxyHost:       testProxyHost,
		ProxyPort:       testProxyPort,
		ProxyCAPath:     "/tmp/devsandbox-ca-src.crt",
		ProxyExtraCAEnv: []string{"MY_TOOL_CA_BUNDLE"},
	})

	for _, tc := range []struct {
		backend string
		got     []proxyenv.Var
		want    string
	}{
		{"bwrap", bwrap, bwrapCADest},
		{"docker", docker, dockerCADest},
	} {
		for _, name := range caNames {
			got, ok := valueOf(tc.got, name)
			if !ok {
				t.Errorf("%s did not set %s", tc.backend, name)
				continue
			}
			if got != tc.want {
				t.Errorf("%s %s = %q, want %q", tc.backend, name, got, tc.want)
			}
		}
	}
}

// TestProxyEnv_BackendsSkipTheCABlock asserts each backend's own gating condition
// survives the extraction: bwrap keys the CA block on MITM, docker on a CA path
// having been produced.
func TestProxyEnv_BackendsSkipTheCABlock(t *testing.T) {
	bwrap := bwrapProxyArgs(t, &sandbox.Config{
		ProxyEnabled:    true,
		ProxyMITM:       false,
		ProxyPort:       testProxyPort,
		GatewayIP:       testProxyHost,
		ProxyExtraCAEnv: []string{"MY_TOOL_CA_BUNDLE"},
	})
	docker := dockerProxyArgs(t, &Config{
		ProjectDir:      "/tmp/test-project",
		SandboxHome:     "/tmp/test-sandbox",
		HomeDir:         "/home/testuser",
		Shell:           "/bin/bash",
		ProxyEnabled:    true,
		ProxyHost:       testProxyHost,
		ProxyPort:       testProxyPort,
		ProxyCAPath:     "",
		ProxyExtraCAEnv: []string{"MY_TOOL_CA_BUNDLE"},
	})

	for _, tc := range []struct {
		backend string
		got     []proxyenv.Var
	}{
		{"bwrap", bwrap},
		{"docker", docker},
	} {
		for _, name := range []string{"SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS", "MY_TOOL_CA_BUNDLE"} {
			if value, ok := valueOf(tc.got, name); ok {
				t.Errorf("%s set %s=%q with no CA certificate to point it at", tc.backend, name, value)
			}
		}
		// The proxy vars themselves must still be there.
		if value, ok := valueOf(tc.got, "HTTP_PROXY"); !ok || value != testProxyURL {
			t.Errorf("%s HTTP_PROXY = %q ok=%v, want %q", tc.backend, value, ok, testProxyURL)
		}
	}
}

// TestProxyEnv_BackendsDeferToAUserConfiguredValue asserts both precedence
// mechanisms survive the extraction - bwrap's SetEnvDefault and docker's
// userConfiguredEnv predicate - and that they apply to the one variable marked
// Default and to no other.
func TestProxyEnv_BackendsDeferToAUserConfiguredValue(t *testing.T) {
	t.Run("bwrap keeps an earlier value", func(t *testing.T) {
		cfg := &sandbox.Config{
			ProxyEnabled: true,
			ProxyMITM:    true,
			ProxyPort:    testProxyPort,
			GatewayIP:    testProxyHost,
		}
		b := sandbox.NewBuilder(cfg)
		// AddEnvironment applies passthrough and config.sandbox.environment before
		// the proxy block; SetEnv is what both end up calling.
		b.SetEnv(miseTimeoutName, "30s")
		b.SetEnv("HTTP_PROXY", "http://user-set:1")
		b.AddProxyEnvironment()

		got := bwrapEnv(b.Build())
		if value, _ := valueOf(got, miseTimeoutName); value != "30s" {
			t.Errorf("%s = %q, want the user's 30s", miseTimeoutName, value)
		}
		if value, _ := valueOf(got, "HTTP_PROXY"); value != testProxyURL {
			t.Errorf("HTTP_PROXY = %q, want the proxy's own %q - only the mise timeout defers", value, testProxyURL)
		}
	})

	t.Run("docker skips the default it would clobber with", func(t *testing.T) {
		got := dockerProxyArgs(t, &Config{
			ProjectDir:   "/tmp/test-project",
			SandboxHome:  "/tmp/test-sandbox",
			HomeDir:      "/home/testuser",
			Shell:        "/bin/bash",
			ProxyEnabled: true,
			ProxyHost:    testProxyHost,
			ProxyPort:    testProxyPort,
			Environment:  map[string]string{miseTimeoutName: "30s"},
		})
		if value, _ := valueOf(got, miseTimeoutName); value != "30s" {
			t.Errorf("%s = %q, want the user's 30s", miseTimeoutName, value)
		}
		count := 0
		for _, v := range got {
			if v.Name == miseTimeoutName {
				count++
			}
		}
		if count != 1 {
			t.Errorf("%s emitted %d times, want 1 - the default must be skipped, not appended after the user's value", miseTimeoutName, count)
		}
	})

	t.Run("docker applies the default when the user set nothing", func(t *testing.T) {
		got := dockerProxyArgs(t, &Config{
			ProjectDir:   "/tmp/test-project",
			SandboxHome:  "/tmp/test-sandbox",
			HomeDir:      "/home/testuser",
			Shell:        "/bin/bash",
			ProxyEnabled: true,
			ProxyHost:    testProxyHost,
			ProxyPort:    testProxyPort,
		})
		if value, ok := valueOf(got, miseTimeoutName); !ok || value != "3s" {
			t.Errorf("%s = %q ok=%v, want 3s", miseTimeoutName, value, ok)
		}
	})
}
