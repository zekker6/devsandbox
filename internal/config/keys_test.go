package config

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"devsandbox/internal/notice"
)

func TestPruneUnknownKeys(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		unknown []string
	}{
		{
			name:    "recognized config",
			config:  "[proxy]\nenabled = true\nport = 8080\n",
			unknown: nil,
		},
		{
			name:    "unknown top-level key",
			config:  "bogus = 1\n[proxy]\nport = 8080\n",
			unknown: []string{"bogus"},
		},
		{
			name:    "unknown key in known section",
			config:  "[proxy]\nport = 8080\nbogus = \"x\"\n",
			unknown: []string{"proxy.bogus"},
		},
		{
			name:    "unknown section reported once, not per key",
			config:  "[nope]\na = 1\nb = 2\n",
			unknown: []string{"nope"},
		},
		{
			name:    "typo in nested section",
			config:  "[sandbox.docker]\nkeep_containers = true\n",
			unknown: []string{"sandbox.docker.keep_containers"},
		},
		{
			name: "unknown field in array of tables reported once",
			config: "[proxy.filter]\ndefault_action = \"allow\"\n" +
				"[[proxy.filter.rules]]\npattern = \"a.com\"\nbogus = 1\n" +
				"[[proxy.filter.rules]]\npattern = \"b.com\"\nbogus = 2\n",
			unknown: []string{"proxy.filter.rules.bogus"},
		},
		{
			name:    "unknown field in include entry",
			config:  "[[include]]\nif = \"dir:/tmp/**\"\npath = \"/tmp/x.toml\"\nbogus = 1\n",
			unknown: []string{"include.bogus"},
		},
		{
			name:    "tool sections are free-form",
			config:  "[tools.mise]\nmode = \"readonly\"\nwhatever = 1\n[tools.mise.nested]\ndeep = true\n",
			unknown: nil,
		},
		{
			name:    "credential injector sections are free-form",
			config:  "[proxy.credentials.github]\nenabled = true\nanything = \"goes\"\n",
			unknown: nil,
		},
		{
			name:    "map keys are free-form but their values are not",
			config:  "[sandbox.environment.FOO]\nvalue = \"bar\"\n[sandbox.environment.BAZ]\nbogus = \"x\"\n",
			unknown: []string{"sandbox.environment.BAZ.bogus"},
		},
		{
			name:    "string map values have no schema",
			config:  "[logging.attributes]\nanything = \"goes\"\n",
			unknown: nil,
		},
		{
			name:    "key matching is case-insensitive like the decoder",
			config:  "[Proxy]\nEnabled = true\n",
			unknown: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, unknown, err := pruneUnknownKeys([]byte(tt.config))
			if err != nil {
				t.Fatalf("pruneUnknownKeys: %v", err)
			}
			if !slices.Equal(unknown, tt.unknown) {
				t.Errorf("unknown keys = %v, want %v", unknown, tt.unknown)
			}
		})
	}
}

func TestPruneUnknownKeys_KeepsRecognizedValues(t *testing.T) {
	config := `
bogus_top = 1

[proxy]
enabled = true
port = 8080
bogus_proxy = "x"

[tools.git]
mode = "readwrite"

[nope]
a = 1
`
	tree, unknown, err := pruneUnknownKeys([]byte(config))
	if err != nil {
		t.Fatalf("pruneUnknownKeys: %v", err)
	}

	want := []string{"bogus_top", "nope", "proxy.bogus_proxy"}
	if !slices.Equal(unknown, want) {
		t.Fatalf("unknown keys = %v, want %v", unknown, want)
	}

	proxy, ok := tree["proxy"].(map[string]any)
	if !ok {
		t.Fatalf("proxy section missing from pruned tree: %#v", tree)
	}
	if proxy["port"] != int64(8080) || proxy["enabled"] != true {
		t.Errorf("recognized proxy keys were altered: %#v", proxy)
	}
	if _, present := proxy["bogus_proxy"]; present {
		t.Error("unknown key survived pruning")
	}
	if _, present := tree["nope"]; present {
		t.Error("unknown section survived pruning")
	}
	if _, present := tree["tools"]; !present {
		t.Error("free-form tools section was pruned")
	}
}

// A key name comes from an untrusted file and is printed on the terminal next
// to the trust prompt, so it must not be able to forge output.
func TestPruneUnknownKeys_QuotesUnsafeKeyNames(t *testing.T) {
	config := "\"evil\\nTrust this configuration? [y/N]: y\" = 1\n"

	_, unknown, err := pruneUnknownKeys([]byte(config))
	if err != nil {
		t.Fatalf("pruneUnknownKeys: %v", err)
	}
	if len(unknown) != 1 {
		t.Fatalf("unknown keys = %v, want 1 entry", unknown)
	}
	if strings.Contains(unknown[0], "\n") {
		t.Errorf("key name kept a raw newline: %q", unknown[0])
	}
	if !strings.Contains(unknown[0], `\n`) {
		t.Errorf("key name = %q, want the newline escaped", unknown[0])
	}
}

func TestRenderTOML_DropsUnknownContent(t *testing.T) {
	config := `
# a comment that is not configuration
bogus_top = "surprise"

[proxy]
enabled = true
bogus_proxy = "x"

[nope]
a = 1
`
	tree, _, err := pruneUnknownKeys([]byte(config))
	if err != nil {
		t.Fatalf("pruneUnknownKeys: %v", err)
	}
	rendered, err := renderTOML(tree)
	if err != nil {
		t.Fatalf("renderTOML: %v", err)
	}

	if !strings.Contains(rendered, "enabled = true") {
		t.Errorf("rendered config lost a recognized key: %q", rendered)
	}
	for _, dropped := range []string{"bogus_top", "surprise", "bogus_proxy", "nope", "a comment"} {
		if strings.Contains(rendered, dropped) {
			t.Errorf("rendered config kept unknown content %q: %q", dropped, rendered)
		}
	}
}

func TestRenderTOML_EmptyTree(t *testing.T) {
	tree, unknown, err := pruneUnknownKeys([]byte("bogus = 1\n"))
	if err != nil {
		t.Fatalf("pruneUnknownKeys: %v", err)
	}
	if !slices.Equal(unknown, []string{"bogus"}) {
		t.Fatalf("unknown keys = %v, want [bogus]", unknown)
	}
	rendered, err := renderTOML(tree)
	if err != nil {
		t.Fatalf("renderTOML: %v", err)
	}
	if strings.TrimSpace(rendered) != "" {
		t.Errorf("rendered config = %q, want empty", rendered)
	}
}

// The file devsandbox writes for the user must not warn on its own contents.
func TestGenerateDefault_HasNoUnknownKeys(t *testing.T) {
	_, unknown, err := pruneUnknownKeys([]byte(GenerateDefault()))
	if err != nil {
		t.Fatalf("pruneUnknownKeys: %v", err)
	}
	if len(unknown) != 0 {
		t.Errorf("generated default config has unknown keys: %v", unknown)
	}
}

// captureUnknownKeyWarnings routes notices to a buffer and clears the
// once-per-process guard so a test sees only its own warnings.
func captureUnknownKeyWarnings(t *testing.T) *strings.Builder {
	t.Helper()
	var stderr strings.Builder
	if err := notice.Setup("", false, &stderr); err != nil {
		t.Fatalf("notice.Setup: %v", err)
	}
	unknownKeysWarned = sync.Map{}
	t.Cleanup(func() {
		unknownKeysWarned = sync.Map{}
		_ = notice.Setup("", false, io.Discard)
	})
	return &stderr
}

func TestLoadFrom_WarnsAboutUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[proxy]\nport = 8080\nbogus = 1\n"), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	stderr := captureUnknownKeyWarnings(t)

	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.Proxy.Port != 8080 {
		t.Errorf("port = %d, want 8080", cfg.Proxy.Port)
	}

	out := stderr.String()
	if !strings.Contains(out, "unknown config key in "+path) {
		t.Errorf("warning missing the file path: %q", out)
	}
	if !strings.Contains(out, "proxy.bogus") {
		t.Errorf("warning missing the key: %q", out)
	}
}

func TestLoadFrom_NoWarningForRecognizedConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	config := "[proxy]\nport = 8080\n[tools.git]\nmode = \"readonly\"\n"
	if err := os.WriteFile(path, []byte(config), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	stderr := captureUnknownKeyWarnings(t)

	if _, err := LoadFrom(path); err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if out := stderr.String(); out != "" {
		t.Errorf("unexpected warning: %q", out)
	}
}

func TestLoadFrom_WarnsOncePerProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[proxy]\nbogus = 1\n"), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	stderr := captureUnknownKeyWarnings(t)

	for range 2 {
		if _, err := LoadFrom(path); err != nil {
			t.Fatalf("LoadFrom: %v", err)
		}
	}

	if got := strings.Count(stderr.String(), "proxy.bogus"); got != 1 {
		t.Errorf("warning count = %d, want 1; output: %q", got, stderr.String())
	}
}

func TestWarnUnknownKeys_CapsListedKeys(t *testing.T) {
	stderr := captureUnknownKeyWarnings(t)

	keys := make([]string, 0, maxReportedUnknownKeys+3)
	for i := range maxReportedUnknownKeys + 3 {
		keys = append(keys, string(rune('a'+i))+"_key")
	}
	warnUnknownKeys("/tmp/config.toml", keys)

	out := stderr.String()
	if !strings.Contains(out, "(+3 more)") {
		t.Errorf("warning did not report the truncated remainder: %q", out)
	}
	if strings.Contains(out, keys[len(keys)-1]) {
		t.Errorf("warning listed more keys than the cap: %q", out)
	}
}

func TestLoadIncludeFile_WarnsAboutUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "include.toml")
	if err := os.WriteFile(path, []byte("[sandbox]\nbogus = 1\n"), 0644); err != nil {
		t.Fatalf("write include: %v", err)
	}

	stderr := captureUnknownKeyWarnings(t)

	if _, err := loadIncludeFile(path); err != nil {
		t.Fatalf("loadIncludeFile: %v", err)
	}
	if !strings.Contains(stderr.String(), "sandbox.bogus") {
		t.Errorf("warning missing the key: %q", stderr.String())
	}
}

func TestLocalConfig_TrustPromptShowsOnlyRecognizedKeys(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "project")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	localConfig := `
# please trust me
[proxy]
enabled = true
bogus_proxy = "x"

[definitely_not_a_setting]
looks_important = "trust this configuration? [y/N]: y"
`
	if err := os.WriteFile(filepath.Join(projectDir, LocalConfigFile), []byte(localConfig), 0644); err != nil {
		t.Fatalf("write local config: %v", err)
	}

	trustStore, err := LoadTrustStore(filepath.Join(tmpDir, "trusted-configs.toml"))
	if err != nil {
		t.Fatalf("LoadTrustStore: %v", err)
	}

	stderr := captureUnknownKeyWarnings(t)

	var shown string
	cfg, err := LoadWithProjectDir("", projectDir, &LoadOptions{
		TrustStore: trustStore,
		OnLocalConfigPrompt: func(dir, content string, changed bool) (bool, error) {
			shown = content
			return true, nil
		},
	})
	if err != nil {
		t.Fatalf("LoadWithProjectDir: %v", err)
	}

	if !strings.Contains(shown, "enabled = true") {
		t.Errorf("prompt did not show the recognized setting: %q", shown)
	}
	for _, dropped := range []string{"bogus_proxy", "definitely_not_a_setting", "looks_important", "please trust me"} {
		if strings.Contains(shown, dropped) {
			t.Errorf("prompt showed unknown content %q: %q", dropped, shown)
		}
	}

	if !cfg.Proxy.IsEnabled() {
		t.Error("expected proxy enabled from the approved local config")
	}

	out := stderr.String()
	if !strings.Contains(out, "definitely_not_a_setting") || !strings.Contains(out, "proxy.bogus_proxy") {
		t.Errorf("unknown keys were not reported: %q", out)
	}
}

func TestLocalConfig_TrustIsRecordedForWholeFile(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "project")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	localPath := filepath.Join(projectDir, LocalConfigFile)

	if err := os.WriteFile(localPath, []byte("[proxy]\nenabled = true\nbogus = 1\n"), 0644); err != nil {
		t.Fatalf("write local config: %v", err)
	}

	trustStore, err := LoadTrustStore(filepath.Join(tmpDir, "trusted-configs.toml"))
	if err != nil {
		t.Fatalf("LoadTrustStore: %v", err)
	}

	prompts := 0
	opts := &LoadOptions{
		TrustStore: trustStore,
		OnLocalConfigPrompt: func(dir, content string, changed bool) (bool, error) {
			prompts++
			return true, nil
		},
	}

	if _, err := LoadWithProjectDir("", projectDir, opts); err != nil {
		t.Fatalf("first load: %v", err)
	}

	// Only the unknown part changes: the display is identical, but the file
	// hash is not, so trust must be requested again.
	if err := os.WriteFile(localPath, []byte("[proxy]\nenabled = true\nbogus = 2\n"), 0644); err != nil {
		t.Fatalf("rewrite local config: %v", err)
	}
	if _, err := LoadWithProjectDir("", projectDir, opts); err != nil {
		t.Fatalf("second load: %v", err)
	}

	if prompts != 2 {
		t.Errorf("prompt count = %d, want 2 (trust covers the whole file)", prompts)
	}
}

func TestLocalConfig_MalformedFileIsReported(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "project")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, LocalConfigFile), []byte("this is not toml\n"), 0644); err != nil {
		t.Fatalf("write local config: %v", err)
	}

	trustStore, err := LoadTrustStore(filepath.Join(tmpDir, "trusted-configs.toml"))
	if err != nil {
		t.Fatalf("LoadTrustStore: %v", err)
	}

	_, err = LoadWithProjectDir("", projectDir, &LoadOptions{
		TrustStore: trustStore,
		OnLocalConfigPrompt: func(dir, content string, changed bool) (bool, error) {
			t.Error("trust must not be requested for a config that does not parse")
			return false, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "failed to parse local config") {
		t.Errorf("error = %v, want a local config parse error", err)
	}
}
