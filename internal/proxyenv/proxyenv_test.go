package proxyenv

import "testing"

func names(vars []Var) []string {
	out := make([]string, 0, len(vars))
	for _, v := range vars {
		out = append(out, v.Name)
	}
	return out
}

func TestURL(t *testing.T) {
	tests := []struct {
		host  string
		port  int
		token string
		want  string
	}{
		{"10.0.2.2", 8080, "tok", "http://devsandbox:tok@10.0.2.2:8080"},
		{"host.docker.internal", 18889, "0123456789abcdef", "http://devsandbox:0123456789abcdef@host.docker.internal:18889"},
		{"::1", 8080, "tok", "http://devsandbox:tok@[::1]:8080"},
	}
	for _, tt := range tests {
		if got := URL(tt.host, tt.port, tt.token); got != tt.want {
			t.Errorf("URL(%q, %d, %q) = %q, want %q", tt.host, tt.port, tt.token, got, tt.want)
		}
	}
}

// TestVars_EveryProxyVariableCarriesTheCredential pins that the credentialed
// URL reaches every variable a client might read the proxy from: one that
// lost the userinfo would send no Proxy-Authorization and get 407. The same
// variables, and only those, are marked Secret: a backend keys on that flag
// to keep the credential off the launch command line, so a URL-carrying
// variable without it lands in world-readable argv.
func TestVars_EveryProxyVariableCarriesTheCredential(t *testing.T) {
	proxyURL := URL("10.0.2.2", 8080, "tok")
	for _, v := range Vars(proxyURL, []string{"MY_TOOL_PROXY"}) {
		switch v.Name {
		case "HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy",
			"YARN_HTTP_PROXY", "YARN_HTTPS_PROXY", "MY_TOOL_PROXY":
			if v.Value != proxyURL {
				t.Errorf("%s = %q, want %q", v.Name, v.Value, proxyURL)
			}
			if !v.Secret {
				t.Errorf("%s carries the credential but is not marked Secret", v.Name)
			}
		default:
			if v.Value == proxyURL {
				t.Errorf("%s = the proxy URL; only proxy address variables carry it", v.Name)
			}
			if v.Secret {
				t.Errorf("%s = %q is marked Secret without carrying the credential", v.Name, v.Value)
			}
		}
	}
	for _, v := range CAVars("/tmp/ca.crt", []string{"MY_CA"}) {
		if v.Secret {
			t.Errorf("CA variable %s is marked Secret", v.Name)
		}
	}
}

func TestVars_OrderAndValues(t *testing.T) {
	const proxyURL = "http://devsandbox:tok@10.0.2.2:8080"

	got := Vars(proxyURL, []string{"MY_TOOL_PROXY"})

	want := []Var{
		{Name: "HTTP_PROXY", Value: proxyURL, Secret: true},
		{Name: "HTTPS_PROXY", Value: proxyURL, Secret: true},
		{Name: "http_proxy", Value: proxyURL, Secret: true},
		{Name: "https_proxy", Value: proxyURL, Secret: true},
		{Name: "NO_PROXY", Value: "localhost,127.0.0.1"},
		{Name: "no_proxy", Value: "localhost,127.0.0.1"},
		{Name: "YARN_HTTP_PROXY", Value: proxyURL, Secret: true},
		{Name: "YARN_HTTPS_PROXY", Value: proxyURL, Secret: true},
		{Name: "NODE_USE_ENV_PROXY", Value: "1"},
		{Name: "MISE_FETCH_REMOTE_VERSIONS_TIMEOUT", Value: "3s", Default: true},
		{Name: "MY_TOOL_PROXY", Value: proxyURL, Secret: true},
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
