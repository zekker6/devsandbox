package config

import (
	"fmt"
	"os"

	"devsandbox/internal/source"
)

// ResolveSandboxEnvironment turns the declarative env source map into a flat
// name→value map suitable for passing to the sandbox. Returns an error if any
// source-resolution fails (e.g. unreadable file). Entries whose source is an
// unset host env var are silently skipped to match env_passthrough semantics.
func ResolveSandboxEnvironment(env map[string]source.Source) (map[string]string, error) {
	if len(env) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(env))
	for name, src := range env {
		// Skip env-sourced vars when the host var is unset.
		if src.Value == "" && src.File == "" && src.Env != "" {
			if _, ok := os.LookupEnv(src.Env); !ok {
				continue
			}
		}
		val, err := src.Resolve()
		if err != nil {
			return nil, fmt.Errorf("sandbox.environment[%q]: %w", name, err)
		}
		out[name] = val
	}
	return out, nil
}
