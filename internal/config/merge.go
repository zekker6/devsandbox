// internal/config/merge.go
package config

// mergeConfigs merges overlay config into base config.
// Scalars: overlay wins if non-zero
// Maps: deep merge
// Arrays: concatenate (overlay first for higher priority)
func mergeConfigs(base, overlay *Config) *Config {
	if overlay == nil {
		return base
	}
	if base == nil {
		return overlay
	}

	result := *base // shallow copy

	// Proxy settings (pointer: overlay wins if set)
	if overlay.Proxy.Enabled != nil {
		result.Proxy.Enabled = overlay.Proxy.Enabled
	}
	if overlay.Proxy.Port != 0 {
		result.Proxy.Port = overlay.Proxy.Port
	}

	// Proxy filter
	if overlay.Proxy.Filter.DefaultAction != "" {
		result.Proxy.Filter.DefaultAction = overlay.Proxy.Filter.DefaultAction
	}
	if overlay.Proxy.Filter.AskTimeout != 0 {
		result.Proxy.Filter.AskTimeout = overlay.Proxy.Filter.AskTimeout
	}
	if overlay.Proxy.Filter.CacheDecisions != nil {
		result.Proxy.Filter.CacheDecisions = overlay.Proxy.Filter.CacheDecisions
	}

	// Rules: prepend overlay rules (higher priority)
	if len(overlay.Proxy.Filter.Rules) > 0 {
		result.Proxy.Filter.Rules = append(
			overlay.Proxy.Filter.Rules,
			result.Proxy.Filter.Rules...,
		)
	}

	// Sandbox settings
	if overlay.Sandbox.BasePath != "" {
		result.Sandbox.BasePath = overlay.Sandbox.BasePath
	}
	if overlay.Sandbox.ConfigVisibility != "" {
		result.Sandbox.ConfigVisibility = overlay.Sandbox.ConfigVisibility
	}
	if overlay.Sandbox.Isolation != "" {
		result.Sandbox.Isolation = overlay.Sandbox.Isolation
	}

	// Sandbox Docker settings
	if overlay.Sandbox.Docker.Dockerfile != "" {
		result.Sandbox.Docker.Dockerfile = overlay.Sandbox.Docker.Dockerfile
	}
	if overlay.Sandbox.Docker.KeepContainer != nil {
		result.Sandbox.Docker.KeepContainer = overlay.Sandbox.Docker.KeepContainer
	}
	if overlay.Sandbox.Docker.Resources.Memory != "" {
		result.Sandbox.Docker.Resources.Memory = overlay.Sandbox.Docker.Resources.Memory
	}
	if overlay.Sandbox.Docker.Resources.CPUs != "" {
		result.Sandbox.Docker.Resources.CPUs = overlay.Sandbox.Docker.Resources.CPUs
	}

	// Sandbox mount rules: prepend overlay rules (higher priority)
	if len(overlay.Sandbox.Mounts.Rules) > 0 {
		result.Sandbox.Mounts.Rules = append(
			overlay.Sandbox.Mounts.Rules,
			result.Sandbox.Mounts.Rules...,
		)
	}

	// Port forwarding settings
	if overlay.PortForwarding.Enabled != nil {
		result.PortForwarding.Enabled = overlay.PortForwarding.Enabled
	}
	if len(overlay.PortForwarding.Rules) > 0 {
		result.PortForwarding.Rules = append(
			overlay.PortForwarding.Rules,
			result.PortForwarding.Rules...,
		)
	}

	// Overlay settings
	if overlay.Overlay.Enabled != nil {
		result.Overlay.Enabled = overlay.Overlay.Enabled
	}

	// Proxy credentials: deep merge (same pattern as tools)
	result.Proxy.Credentials = mergeToolsConfig(base.Proxy.Credentials, overlay.Proxy.Credentials)

	// Tools: deep merge
	result.Tools = mergeToolsConfig(base.Tools, overlay.Tools)

	// Logging: merge receivers (append), merge attributes
	if len(overlay.Logging.Receivers) > 0 {
		result.Logging.Receivers = append(
			result.Logging.Receivers,
			overlay.Logging.Receivers...,
		)
	}
	result.Logging.Attributes = mergeStringMap(
		base.Logging.Attributes,
		overlay.Logging.Attributes,
	)

	// Include: not merged (only from global config)
	// result.Include stays from base

	return &result
}

// mergeToolsConfig deep-merges tool configurations.
func mergeToolsConfig(base, overlay map[string]any) map[string]any {
	if base == nil && overlay == nil {
		return nil
	}
	if base == nil {
		return overlay
	}
	if overlay == nil {
		return base
	}

	result := make(map[string]any)
	// Copy base
	for k, v := range base {
		result[k] = v
	}
	// Merge overlay
	for k, v := range overlay {
		if baseVal, exists := result[k]; exists {
			// Deep merge if both are maps
			baseMap, baseOk := baseVal.(map[string]any)
			overlayMap, overlayOk := v.(map[string]any)
			if baseOk && overlayOk {
				result[k] = mergeAnyMap(baseMap, overlayMap)
				continue
			}
		}
		result[k] = v
	}
	return result
}

// mergeAnyMap merges two map[string]any, overlay wins for conflicts.
func mergeAnyMap(base, overlay map[string]any) map[string]any {
	result := make(map[string]any)
	for k, v := range base {
		result[k] = v
	}
	for k, v := range overlay {
		result[k] = v
	}
	return result
}

// mergeStringMap merges two string maps, overlay wins for conflicts.
func mergeStringMap(base, overlay map[string]string) map[string]string {
	if base == nil && overlay == nil {
		return nil
	}
	result := make(map[string]string)
	for k, v := range base {
		result[k] = v
	}
	for k, v := range overlay {
		result[k] = v
	}
	return result
}
