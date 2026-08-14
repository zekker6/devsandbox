package proxyenv

import "testing"

func names(vars []Var) []string {
	out := make([]string, 0, len(vars))
	for _, v := range vars {
		out = append(out, v.Name)
	}
	return out
}

func TestVars_OrderAndValues(t *testing.T) {
	const proxyURL = "http://10.0.2.2:8080"

	got := Vars(proxyURL, []string{"MY_TOOL_PROXY"})

	want := []Var{
		{Name: "HTTP_PROXY", Value: proxyURL},
		{Name: "HTTPS_PROXY", Value: proxyURL},
		{Name: "http_proxy", Value: proxyURL},
		{Name: "https_proxy", Value: proxyURL},
		{Name: "NO_PROXY", Value: "localhost,127.0.0.1"},
		{Name: "no_proxy", Value: "localhost,127.0.0.1"},
		{Name: "YARN_HTTP_PROXY", Value: proxyURL},
		{Name: "YARN_HTTPS_PROXY", Value: proxyURL},
		{Name: "NODE_USE_ENV_PROXY", Value: "1"},
		{Name: "MISE_FETCH_REMOTE_VERSIONS_TIMEOUT", Value: "3s", Default: true},
		{Name: "MY_TOOL_PROXY", Value: proxyURL},
		{Name: "DEVSANDBOX_PROXY", Value: "1"},
	}

	if len(got) != len(want) {
		t.Fatalf("Vars returned %d vars %v, want %d %v", len(got), names(got), len(want), names(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Vars[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestVars_OnlyMiseTimeoutIsADefault pins which variable defers to a
// user-configured value: every other one is devsandbox stating where the proxy
// is, which a user value must not silently replace.
func TestVars_OnlyMiseTimeoutIsADefault(t *testing.T) {
	for _, v := range Vars("http://10.0.2.2:8080", []string{"MY_TOOL_PROXY"}) {
		want := v.Name == "MISE_FETCH_REMOTE_VERSIONS_TIMEOUT"
		if v.Default != want {
			t.Errorf("%s: Default = %v, want %v", v.Name, v.Default, want)
		}
	}
}

func TestVars_NoExtraEnv(t *testing.T) {
	got := names(Vars("http://10.0.2.2:8080", nil))
	if len(got) != 11 {
		t.Fatalf("Vars with no extra_env returned %d vars: %v", len(got), got)
	}
	if got[len(got)-1] != "DEVSANDBOX_PROXY" {
		t.Errorf("last var = %q, want DEVSANDBOX_PROXY", got[len(got)-1])
	}
}

func TestCAVars_OrderAndValues(t *testing.T) {
	const caPath = "/tmp/devsandbox-ca.crt"

	got := CAVars(caPath, []string{"MY_TOOL_CA_BUNDLE", "CUSTOM_SSL_CERT"})

	want := []string{
		"REQUESTS_CA_BUNDLE",
		"NODE_EXTRA_CA_CERTS",
		"CURL_CA_BUNDLE",
		"GIT_SSL_CAINFO",
		"SSL_CERT_FILE",
		"MY_TOOL_CA_BUNDLE",
		"CUSTOM_SSL_CERT",
	}

	gotNames := names(got)
	if len(gotNames) != len(want) {
		t.Fatalf("CAVars returned %v, want %v", gotNames, want)
	}
	for i := range want {
		if gotNames[i] != want[i] {
			t.Errorf("CAVars[%d] name = %q, want %q", i, gotNames[i], want[i])
		}
	}
	for _, v := range got {
		if v.Value != caPath {
			t.Errorf("%s = %q, want the CA path %q", v.Name, v.Value, caPath)
		}
		if v.Default {
			t.Errorf("%s must not be a default: the sandbox only trusts the CA at the path devsandbox mounted", v.Name)
		}
	}
}

// TestCAVars_PerBackendPath asserts the CA path is the caller's, not a constant
// baked into the shared list: bwrap mounts the certificate under /tmp because
// /etc/ssl is a read-only bind there, docker under /etc/ssl/certs.
func TestCAVars_PerBackendPath(t *testing.T) {
	for _, caPath := range []string{"/tmp/devsandbox-ca.crt", "/etc/ssl/certs/devsandbox-ca.crt"} {
		for _, v := range CAVars(caPath, nil) {
			if v.Value != caPath {
				t.Errorf("CAVars(%q): %s = %q", caPath, v.Name, v.Value)
			}
		}
	}
}
