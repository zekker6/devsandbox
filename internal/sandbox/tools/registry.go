package tools

import (
	"reflect"
	"sort"

	"devsandbox/internal/config"
	"devsandbox/internal/notice"
)

var registry = make(map[string]Tool)

// init teaches the config loader what a [tools.<name>] section holds, so a typo
// there is reported and a value of the wrong type is rejected instead of both
// being ignored.
func init() {
	config.RegisterFreeFormSchema("tools", toolSchema)
}

// toolSchema returns the type [tools.<name>] decodes into. The registry is read
// when a config is loaded rather than at init, so tool registration order does
// not matter.
func toolSchema(name string) (reflect.Type, bool) {
	tool, ok := registry[name]
	if !ok {
		return nil, false
	}
	if configurable, ok := tool.(ToolWithConfigType); ok {
		return configurable.ConfigType(), true
	}
	// A tool that declares nothing takes no settings at all - not even
	// mount_mode, which does nothing for a tool that mounts nothing. A tool
	// that does mount embeds Mounting to say so.
	return reflect.TypeFor[Config](), true
}

// decodeConfig fills cfg from a tool's [tools.<name>] section.
//
// The loader rejects a section that does not fit before any tool sees it, so an
// error here means Configure was handed a section that never went through it.
// The tool keeps its defaults rather than applying the part that did decode,
// and says so.
func decodeConfig(name string, toolCfg map[string]any, cfg any) {
	if err := config.DecodeSection(toolCfg, cfg); err != nil {
		notice.Warn("tools.%s: %v; ignoring this section", name, err)
	}
}

// Register adds a tool to the global registry.
// Tools should call this in their init() function.
func Register(t Tool) {
	registry[t.Name()] = t
}

// Unregister removes a tool by name. Used by tests to inject/remove mock tools.
func Unregister(name string) {
	delete(registry, name)
}

// All returns all registered tools, sorted by name for deterministic ordering.
func All() []Tool {
	tools := make([]Tool, 0, len(registry))
	for _, t := range registry {
		tools = append(tools, t)
	}
	// Sort by name for consistent ordering
	sort.Slice(tools, func(i, j int) bool {
		return tools[i].Name() < tools[j].Name()
	})
	return tools
}

// Get returns a tool by name, or nil if not found.
func Get(name string) Tool {
	return registry[name]
}

// Available returns all tools that are available on this system.
func Available(homeDir string) []Tool {
	var available []Tool
	for _, t := range All() {
		if t.Available(homeDir) {
			available = append(available, t)
		}
	}
	return available
}

// CollectCacheMounts returns all cache mounts from registered tools.
// Uses All() instead of Available() because Docker containers provide their
// own tool binaries — host availability is irrelevant for cache mounts.
// Only tools that implement ToolWithCache are included.
func CollectCacheMounts() []CacheMount {
	var mounts []CacheMount
	for _, tool := range All() {
		if cacheTool, ok := tool.(ToolWithCache); ok {
			mounts = append(mounts, cacheTool.CacheMounts()...)
		}
	}
	return mounts
}
