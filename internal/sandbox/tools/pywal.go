package tools

import (
	"os"
	"path/filepath"
)

func init() {
	Register(&Pywal{})
}

// Pywal provides pywal (wal) terminal color scheme support.
// Mounts the color cache read-only for terminal theming.
type Pywal struct{}

func (p *Pywal) Name() string {
	return "pywal"
}

func (p *Pywal) Available(homeDir string) bool {
	// Check if wal cache exists
	walCache := filepath.Join(homeDir, ".cache", "wal")
	_, err := os.Stat(walCache)
	return err == nil
}

func (p *Pywal) Bindings(homeDir, sandboxHome string) []Binding {
	return []Binding{
		{
			Source:   filepath.Join(homeDir, ".cache", "wal"),
			ReadOnly: true,
			Optional: true,
		},
	}
}

func (p *Pywal) Environment(homeDir, sandboxHome string) []EnvVar {
	return nil
}

func (p *Pywal) ShellInit(shell string) string {
	return ""
}
