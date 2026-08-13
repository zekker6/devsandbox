package tools

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"devsandbox/internal/config"
	"devsandbox/internal/notice"
)

func TestToolSchema(t *testing.T) {
	schema, ok := toolSchema("git")
	if !ok {
		t.Fatal("git has no schema")
	}
	if schema != reflect.TypeFor[gitConfig]() {
		t.Errorf("git schema = %v, want gitConfig", schema)
	}

	// A tool that mounts but reads no settings of its own takes mount_mode
	// and nothing else.
	if schema, ok := toolSchema("nvim"); !ok || schema != reflect.TypeFor[MountModeConfig]() {
		t.Errorf("nvim schema = %v (ok=%v), want MountModeConfig", schema, ok)
	}

	// A tool that neither mounts nor reads settings takes nothing at all.
	if schema, ok := toolSchema("go"); !ok || schema != reflect.TypeFor[Config]() {
		t.Errorf("go schema = %v (ok=%v), want the empty Config", schema, ok)
	}

	if _, ok := toolSchema("gti"); ok {
		t.Error("a misspelled tool name resolved to a schema")
	}
}

// A tool that reads settings without declaring their type turns every one of
// its keys into a reported unknown key, so the two must be implemented
// together.
func TestToolSchema_ConfigurableToolsDeclareTheirType(t *testing.T) {
	for _, tool := range All() {
		if _, configurable := tool.(ToolWithConfig); !configurable {
			continue
		}
		if _, declares := tool.(ToolWithConfigType); !declares {
			t.Errorf("%s implements ToolWithConfig but not ToolWithConfigType", tool.Name())
		}
	}
}

// A tool whose mounts mount_mode resolves must accept the key, or configuring
// it the documented way is reported as a typo. Asked of the tool rather than of
// a list here, so a tool that grows bindings cannot drift out of it.
func TestToolSchema_ToolsThatMountAcceptMountMode(t *testing.T) {
	home, sandboxHome := t.TempDir(), t.TempDir()

	for _, tool := range All() {
		schema, ok := toolSchema(tool.Name())
		if !ok {
			t.Errorf("%s has no schema", tool.Name())
			continue
		}
		if !contributesMounts(tool, home, sandboxHome) {
			// Cannot prove it never will - several tools mount only inside a
			// terminal session this test does not run in - so the reverse is
			// left unasserted.
			continue
		}
		if _, found := schema.FieldByName("MountMode"); !found {
			t.Errorf("%s mounts something but its config %v does not embed MountModeConfig, so mount_mode reads as unknown", tool.Name(), schema)
		}
	}
}

// contributesMounts reports whether a tool can produce a mount that the
// per-tool mount mode resolves or "disabled" drops.
func contributesMounts(tool Tool, home, sandboxHome string) bool {
	if len(tool.Bindings(home, sandboxHome)) > 0 {
		return true
	}
	if _, ok := tool.(ToolWithSharedTmp); ok {
		return true
	}
	if dockerTool, ok := tool.(ToolWithDocker); ok {
		return len(dockerTool.DockerBindings(home, sandboxHome)) > 0
	}
	return false
}

// The two tools that mount nothing must not accept a setting nothing reads: a
// section copied from another tool would otherwise look applied.
func TestToolSchema_ToolsThatMountNothingRejectMountMode(t *testing.T) {
	for _, name := range []string{"docker", "go"} {
		schema, ok := toolSchema(name)
		if !ok {
			t.Fatalf("%s has no schema", name)
		}
		if _, found := schema.FieldByName("MountMode"); found {
			t.Errorf("%s mounts nothing but its config %v accepts mount_mode", name, schema)
		}
	}
}

// A setting the tool cannot read is worse than a rejected config: the sandbox
// starts having applied a default the user did not write.
func TestLoadFrom_RejectsMistypedToolSetting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[tools.mise]\nignore_global_config = \"yes\"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := config.LoadFrom(path)
	if err == nil {
		t.Fatal("a mistyped tool setting was accepted")
	}
	if !strings.Contains(err.Error(), "ignore_global_config: expected a boolean, got a string") {
		t.Errorf("error = %q, want it to name the key and both types", err)
	}
}

func TestGeneratedConfig_HasNoUnknownToolKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(config.GenerateDefault()), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var stderr bytes.Buffer
	if err := notice.Setup("", true, &stderr); err != nil {
		t.Fatalf("notice.Setup: %v", err)
	}
	t.Cleanup(func() { _ = notice.Setup("", false, nil) })

	if _, err := config.LoadFrom(path); err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if strings.Contains(stderr.String(), "unknown config key") {
		t.Errorf("the generated config reports unknown keys: %q", stderr.String())
	}
}
