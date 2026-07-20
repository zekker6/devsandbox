package tools

import (
	"testing"

	"devsandbox/internal/cmdpattern"
	"devsandbox/internal/herdrproxy"
)

func TestBindingCategory_Values(t *testing.T) {
	// Verify all category constants are distinct and non-empty
	categories := []BindingCategory{
		CategoryConfig, CategoryCache, CategoryData, CategoryState, CategoryRuntime,
	}
	seen := make(map[BindingCategory]bool)
	for _, c := range categories {
		if c == "" {
			t.Errorf("category should not be empty")
		}
		if seen[c] {
			t.Errorf("duplicate category: %s", c)
		}
		seen[c] = true
	}
}

func TestCacheMount_FullPath(t *testing.T) {
	cm := CacheMount{
		Name:   "mise",
		EnvVar: "MISE_DATA_DIR",
	}

	path := cm.FullPath()
	if path != "/cache/mise" {
		t.Errorf("FullPath() = %q, want %q", path, "/cache/mise")
	}
}

func TestCacheMount_FullPath_Nested(t *testing.T) {
	cm := CacheMount{
		Name:   "go/mod",
		EnvVar: "GOMODCACHE",
	}

	path := cm.FullPath()
	if path != "/cache/go/mod" {
		t.Errorf("FullPath() = %q, want %q", path, "/cache/go/mod")
	}
}

// herdrStubTool implements both herdr interfaces so the compiler proves the
// method sets are satisfiable by a real tool.
type herdrStubTool struct{ Tool }

func (herdrStubTool) Name() string                        { return "herdr-stub" }
func (herdrStubTool) Description() string                 { return "stub" }
func (herdrStubTool) Available(string) bool               { return true }
func (herdrStubTool) Bindings(string, string) []Binding   { return nil }
func (herdrStubTool) Environment(string, string) []EnvVar { return nil }
func (herdrStubTool) ShellInit(string) string             { return "" }
func (herdrStubTool) HerdrCapabilities() []herdrproxy.Capability {
	return []herdrproxy.Capability{herdrproxy.CapLaunchOverlay}
}
func (herdrStubTool) HerdrLaunchScript() cmdpattern.ScriptPattern {
	return cmdpattern.ScriptPattern{Shebangs: []string{"#!/bin/sh"}}
}

func TestHerdrInterfacesAreSatisfiable(t *testing.T) {
	var stub any = herdrStubTool{}

	req, ok := stub.(ToolWithHerdrRequirements)
	if !ok {
		t.Fatal("stub does not satisfy ToolWithHerdrRequirements")
	}
	if caps := req.HerdrCapabilities(); len(caps) != 1 || caps[0] != herdrproxy.CapLaunchOverlay {
		t.Errorf("HerdrCapabilities() = %v, want [launch_overlay]", caps)
	}

	script, ok := stub.(ToolWithHerdrLaunchScript)
	if !ok {
		t.Fatal("stub does not satisfy ToolWithHerdrLaunchScript")
	}
	if got := script.HerdrLaunchScript(); len(got.Shebangs) != 1 {
		t.Errorf("HerdrLaunchScript() = %+v, want the declared shebang allowlist", got)
	}
}

// TestKittyToolDoesNotSatisfyHerdrInterfaces keeps the two proxies' declarations
// distinct: a tool must opt into herdr explicitly rather than inheriting access
// from its kitty declaration.
func TestKittyToolDoesNotSatisfyHerdrInterfaces(t *testing.T) {
	var k any = &Kitty{}
	if _, ok := k.(ToolWithHerdrRequirements); ok {
		t.Error("Kitty satisfies ToolWithHerdrRequirements; herdr access must be declared separately")
	}
}
