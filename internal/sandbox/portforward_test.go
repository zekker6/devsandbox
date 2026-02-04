package sandbox

import (
	"testing"

	"devsandbox/internal/config"
)

func TestBuildPastaPortArgs(t *testing.T) {
	tests := []struct {
		name     string
		rules    []config.PortForwardingRule
		expected []string
	}{
		{
			name:     "empty_rules",
			rules:    nil,
			expected: nil,
		},
		{
			name: "inbound_tcp",
			rules: []config.PortForwardingRule{
				{Direction: "inbound", Protocol: "tcp", HostPort: 3000, SandboxPort: 8080},
			},
			expected: []string{"--tcp-ports", "3000:8080"},
		},
		{
			name: "inbound_udp",
			rules: []config.PortForwardingRule{
				{Direction: "inbound", Protocol: "udp", HostPort: 5000, SandboxPort: 5000},
			},
			expected: []string{"--udp-ports", "5000:5000"},
		},
		{
			name: "outbound_tcp",
			rules: []config.PortForwardingRule{
				{Direction: "outbound", Protocol: "tcp", HostPort: 5432, SandboxPort: 5432},
			},
			expected: []string{"-T", "5432"},
		},
		{
			name: "outbound_udp",
			rules: []config.PortForwardingRule{
				{Direction: "outbound", Protocol: "udp", HostPort: 53, SandboxPort: 53},
			},
			expected: []string{"-U", "53"},
		},
		{
			name: "multiple_rules",
			rules: []config.PortForwardingRule{
				{Direction: "inbound", Protocol: "tcp", HostPort: 3000, SandboxPort: 3000},
				{Direction: "inbound", Protocol: "tcp", HostPort: 8080, SandboxPort: 8080},
				{Direction: "outbound", Protocol: "tcp", HostPort: 5432, SandboxPort: 5432},
			},
			expected: []string{
				"--tcp-ports", "3000:3000",
				"--tcp-ports", "8080:8080",
				"-T", "5432",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildPastaPortArgs(tt.rules)
			if len(got) != len(tt.expected) {
				t.Errorf("expected %d args, got %d: %v", len(tt.expected), len(got), got)
				return
			}
			for i, arg := range got {
				if arg != tt.expected[i] {
					t.Errorf("arg[%d]: expected %q, got %q", i, tt.expected[i], arg)
				}
			}
		})
	}
}
