package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServerAuth_CONNECT_ClientNegotiation(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("https_proxy", "")

	for _, mitm := range []bool{false, true} {
		name := "transparent"
		if mitm {
			name = "mitm"
		}
		t.Run(name, func(t *testing.T) {
			upstream := &recordingUpstream{}
			ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/repo/") {
					if r.Header.Get("Proxy-Authorization") != "" {
						t.Error("Git upstream received proxy credentials")
					}
					w.Header().Set("Content-Type", "text/plain")
					if strings.HasSuffix(r.URL.Path, "/HEAD") {
						if _, err := io.WriteString(w, "ref: refs/heads/main\n"); err != nil {
							t.Errorf("write HEAD: %v", err)
						}
					}
					return
				}
				upstream.handler()(w, r)
			}))
			defer ts.Close()

			cfg := newTestConfig(shortTempDir(t), 0)
			cfg.MITM = mitm
			srv := startAuthProxy(t, cfg, ts)

			t.Run("curl", func(t *testing.T) {
				curl, err := exec.LookPath("curl")
				if err != nil {
					t.Skip("curl not installed")
				}
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, curl, "-q", "--proxy-anyauth", "--insecure", "--silent", "--show-error", "--fail", ts.URL)
				cmd.Env = append(os.Environ(), "https_proxy="+testProxyURL(srv).String(), "NO_PROXY=", "no_proxy=")
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("curl through proxy: %v\n%s", err, out)
				}
				if string(out) != "ok" {
					t.Errorf("response = %q, want ok", out)
				}
			})

			t.Run("git", func(t *testing.T) {
				git, err := exec.LookPath("git")
				if err != nil {
					t.Skip("git not installed")
				}
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, git, "-c", "http.proxyAuthMethod=anyauth", "ls-remote", ts.URL+"/repo")
				cmd.Dir = t.TempDir()
				cmd.Env = append(os.Environ(), "https_proxy="+testProxyURL(srv).String(), "NO_PROXY=", "no_proxy=",
					"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+filepath.Join(t.TempDir(), "gitconfig"),
					"GIT_CONFIG_COUNT=0", "GIT_CONFIG_PARAMETERS=", "GIT_PROXY_AUTHMETHOD=anyauth",
					"GIT_SSL_NO_VERIFY=1", "GIT_TERMINAL_PROMPT=0")
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git through proxy: %v\n%s", err, out)
				}
			})

			_, auth := upstream.snapshot()
			if len(auth) != 0 {
				t.Errorf("upstream received proxy credentials: %v", auth)
			}
		})
	}
}
