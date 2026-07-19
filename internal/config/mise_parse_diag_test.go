package config

import (
	"fmt"
	"testing"
)

// TestDiag_ParseE2EMiseSection loads the real e2e config file and reports what
// appears in Tools["mise"], to confirm whether the ignore_global_config option parses.
func TestDiag_ParseE2EMiseSection(t *testing.T) {
	path := "/home/zekker/Code/mine/sandboxing-fun/devsandbox-krun-e2e/.devsandbox.toml"
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom(%s): %v", path, err)
	}
	t.Logf("Tools keys: %v", keysOf(cfg.Tools))
	misceRaw, ok := cfg.Tools["mise"]
	if !ok {
		t.Fatalf("BUG: cfg.Tools has no \"mise\" key after parse; Tools=%#v", cfg.Tools)
	}
	t.Logf("Tools[mise] type=%T value=%#v", misceRaw, misceRaw)
	m, ok := misceRaw.(map[string]any)
	if !ok {
		t.Fatalf("BUG: Tools[mise] is %T not map[string]any", misceRaw)
	}
	t.Logf("Tools[mise][ignore_global_config]=%#v (type %T)", m["ignore_global_config"], m["ignore_global_config"])
	if v, _ := m["ignore_global_config"].(bool); !v {
		t.Fatalf("BUG: ignore_global_config not true after parse")
	}
	t.Log("OK: parsed ignore_global_config=true")
}

func keysOf(m map[string]any) string {
	s := ""
	for k := range m {
		s += fmt.Sprintf("%q ", k)
	}
	return s
}
