package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestLoadWithProjectDir_PortForwardingAutoDetect(t *testing.T) {
	for _, source := range []string{"include", "trusted local"} {
		t.Run(source, func(t *testing.T) {
			for _, tt := range []struct {
				name         string
				baseAuto     bool
				overlay      string
				wantAuto     bool
				wantInterval string
				wantPorts    []int
			}{
				{
					name:         "enable and override interval",
					overlay:      "auto_detect = true\nscan_interval = \"3s\"\nexclude_ports = [443, 8080, 8080]\n",
					wantAuto:     true,
					wantInterval: "3s",
					wantPorts:    []int{22, 443, 8080},
				},
				{
					name:         "explicit false overrides true",
					baseAuto:     true,
					overlay:      "auto_detect = false\n",
					wantInterval: "5s",
					wantPorts:    []int{22, 443},
				},
				{
					name:         "inherit omitted fields",
					baseAuto:     true,
					overlay:      "exclude_ports = [8080, 22]\n",
					wantAuto:     true,
					wantInterval: "5s",
					wantPorts:    []int{22, 443, 8080},
				},
				{
					name:         "empty values preserve inherited fields",
					baseAuto:     true,
					overlay:      "scan_interval = \"\"\nexclude_ports = []\n",
					wantAuto:     true,
					wantInterval: "5s",
					wantPorts:    []int{22, 443},
				},
			} {
				t.Run(tt.name, func(t *testing.T) {
					dir := t.TempDir()
					globalPath := filepath.Join(dir, "global.toml")
					overlayPath := filepath.Join(dir, LocalConfigFile)
					global := fmt.Sprintf("[port_forwarding]\nenabled = true\nauto_detect = %t\nscan_interval = \"5s\"\nexclude_ports = [443, 22]\n", tt.baseAuto)
					opts := &LoadOptions{}
					if source == "include" {
						overlayPath = filepath.Join(dir, "include.toml")
						global += fmt.Sprintf("\n[[include]]\nif = %q\npath = %q\n", "dir:"+dir, overlayPath)
					}
					if err := os.WriteFile(globalPath, []byte(global), 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(overlayPath, []byte("[port_forwarding]\n"+tt.overlay), 0o600); err != nil {
						t.Fatal(err)
					}
					if source == "trusted local" {
						hash, err := HashFile(overlayPath)
						if err != nil {
							t.Fatal(err)
						}
						opts.TrustStore = &TrustStore{}
						opts.TrustStore.AddTrust(dir, hash)
					}

					cfg, err := LoadWithProjectDir(globalPath, dir, opts)
					if err != nil {
						t.Fatalf("LoadWithProjectDir: %v", err)
					}
					got := cfg.PortForwarding
					if !got.IsEnabled() {
						t.Error("enabled was not inherited")
					}
					if got.IsAutoDetectEnabled() != tt.wantAuto {
						t.Errorf("auto_detect = %v, want %v", got.IsAutoDetectEnabled(), tt.wantAuto)
					}
					if got.ScanInterval != tt.wantInterval {
						t.Errorf("scan_interval = %q, want %q", got.ScanInterval, tt.wantInterval)
					}
					if !slices.Equal(got.ExcludePorts, tt.wantPorts) {
						t.Errorf("exclude_ports = %v, want %v", got.ExcludePorts, tt.wantPorts)
					}
				})
			}
		})
	}
}

func TestMergeConfigs_ExcludePortsDoesNotMutateSources(t *testing.T) {
	for _, tt := range []struct {
		name    string
		base    []int
		overlay []int
		want    []int
	}{
		{"both", []int{443, 22, 443}, []int{8080, 443, 8080}, []int{22, 443, 8080}},
		{"base only", []int{443, 22, 443}, nil, []int{22, 443}},
		{"overlay only", nil, []int{8080, 443, 8080}, []int{443, 8080}},
		{"neither", nil, nil, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Spare capacity exposes append-based merges that overwrite source storage.
			baseStorage := append(slices.Clone(tt.base), -1, -2, -3, -4)
			overlayStorage := append(slices.Clone(tt.overlay), -5, -6, -7, -8)
			baseBefore := slices.Clone(baseStorage)
			overlayBefore := slices.Clone(overlayStorage)
			base := &Config{PortForwarding: PortForwardingConfig{ExcludePorts: baseStorage[:len(tt.base)]}}
			overlay := &Config{PortForwarding: PortForwardingConfig{ExcludePorts: overlayStorage[:len(tt.overlay)]}}

			merged := mergeConfigs(base, overlay)
			if !slices.Equal(merged.PortForwarding.ExcludePorts, tt.want) {
				t.Fatalf("exclude_ports = %v, want %v", merged.PortForwarding.ExcludePorts, tt.want)
			}
			if len(merged.PortForwarding.ExcludePorts) > 0 {
				merged.PortForwarding.ExcludePorts[0] = 9999
			}
			if !slices.Equal(baseStorage, baseBefore) {
				t.Errorf("base storage changed: got %v, want %v", baseStorage, baseBefore)
			}
			if !slices.Equal(overlayStorage, overlayBefore) {
				t.Errorf("overlay storage changed: got %v, want %v", overlayStorage, overlayBefore)
			}
		})
	}
}
