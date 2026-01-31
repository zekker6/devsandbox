package tools

import "sort"

var registry = make(map[string]Tool)

// Register adds a tool to the global registry.
// Tools should call this in their init() function.
func Register(t Tool) {
	registry[t.Name()] = t
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
