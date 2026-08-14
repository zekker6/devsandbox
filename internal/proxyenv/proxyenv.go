// Package proxyenv holds the one environment variable set devsandbox exports
// into a sandbox running behind the proxy.
//
// Both backends render this same list - the bwrap builder through
// SetEnv/SetEnvDefault, the docker/krun backend as `-e` arguments - so a
// variable added here reaches every backend instead of only the one that was
// edited. A variable that works under bwrap and silently does not under
// docker/krun fails as a tool that bypasses the proxy and dies on the egress
// lockdown, which is why the list lives in one place. Same shape as
// internal/egress: one rule set, two application mechanisms.
//
// What legitimately differs per backend stays with the backend: the CA
// certificate destination path (bwrap uses /tmp because /etc/ssl is a read-only
// bind there), the condition under which the CA block applies, and the
// backend-specific PROXY_MODE/PROXY_HOST/PROXY_PORT trio.
package proxyenv

// Var is one environment variable to export into the sandbox.
type Var struct {
	Name  string
	Value string
	// Default is true when a value the user already configured for this name
	// must win over the value here. Each backend applies that with its own
	// precedence mechanism.
	Default bool
}

// noProxyValue keeps the sandbox's own loopback traffic off the proxy.
const noProxyValue = "localhost,127.0.0.1"

// Vars returns the proxy variables in a fixed order, given the proxy URL the
// sandbox reaches the proxy at and the user's extra_env names.
func Vars(proxyURL string, extraEnv []string) []Var {
	vars := []Var{
		// Standard proxy env vars (both cases for broad compatibility)
		{Name: "HTTP_PROXY", Value: proxyURL},
		{Name: "HTTPS_PROXY", Value: proxyURL},
		{Name: "http_proxy", Value: proxyURL},
		{Name: "https_proxy", Value: proxyURL},
		{Name: "NO_PROXY", Value: noProxyValue},
		{Name: "no_proxy", Value: noProxyValue},

		// Tool-specific proxy env vars
		{Name: "YARN_HTTP_PROXY", Value: proxyURL},
		{Name: "YARN_HTTPS_PROXY", Value: proxyURL},

		// Node.js >=24: opt-in for built-in fetch (undici) to honor HTTP(S)_PROXY
		// env vars. Without this, npx-based tools like mcp-remote bypass the proxy
		// and fail with ENETUNREACH.
		{Name: "NODE_USE_ENV_PROXY", Value: "1"},

		// mise's remote version-list lookups (one per `@latest`-style tool spec in
		// a mise config) default to a 20s timeout each. In an egress-locked sandbox
		// a lookup that escapes the proxy path hangs to the full timeout, and a
		// config with several such specs stalls every `mise ls`/install for minutes.
		// Bound the lookups tightly: through the local proxy a working fetch answers
		// well under this, and a blocked one falls back to installed versions 3s in.
		// Only a default, so a user who configured this var keeps their value.
		{Name: "MISE_FETCH_REMOTE_VERSIONS_TIMEOUT", Value: "3s", Default: true},
	}

	// User-defined extra proxy env vars from config
	for _, name := range extraEnv {
		vars = append(vars, Var{Name: name, Value: proxyURL})
	}

	vars = append(vars, Var{Name: "DEVSANDBOX_PROXY", Value: "1"})

	return vars
}

// CAVars returns the CA bundle variables in a fixed order, all pointing at
// caPath - the backend's own destination for the proxy CA certificate - plus the
// user's extra_ca_env names. The caller decides when the CA block applies.
func CAVars(caPath string, extraCAEnv []string) []Var {
	vars := []Var{
		{Name: "REQUESTS_CA_BUNDLE", Value: caPath},
		{Name: "NODE_EXTRA_CA_CERTS", Value: caPath},
		{Name: "CURL_CA_BUNDLE", Value: caPath},
		{Name: "GIT_SSL_CAINFO", Value: caPath},
		{Name: "SSL_CERT_FILE", Value: caPath},
	}

	// User-defined extra CA bundle env vars from config
	for _, name := range extraCAEnv {
		vars = append(vars, Var{Name: name, Value: caPath})
	}

	return vars
}
