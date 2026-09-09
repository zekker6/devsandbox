package tools

import (
	"errors"
	"os"
	"testing"
)

func TestClaude_DisabledSetupDoesNotSeedHostConfig(t *testing.T) {
	home, sandboxHome, dst := seedFixture(t, `{"hostSecret":"private"}`)
	c := &Claude{}
	c.Configure(GlobalConfig{}, map[string]any{"mount_mode": "disabled"})
	if err := c.Setup(home, sandboxHome); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disabled Claude seeded host configuration: %v", err)
	}
	if seeds := c.HomeSeedFiles(); len(seeds) != 0 {
		t.Fatalf("disabled Claude declared container seeds: %v", seeds)
	}
	c.Configure(GlobalConfig{}, nil)
	if err := c.Setup(home, sandboxHome); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("re-enabled singleton did not seed configuration: %v", err)
	}
}
