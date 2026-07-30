package main

import (
	"testing"

	"devsandbox/internal/sandbox"
)

// The listing is where an OOM kill becomes visible: the sandbox is gone and its
// session file with it, so a status column that dropped the record would leave the
// user with the same silent disappearance the record exists to explain.
func TestFormatSandboxStatus(t *testing.T) {
	tests := []struct {
		name string
		meta *sandbox.Metadata
		want string
	}{
		{
			name: "a plain idle sandbox has no status",
			meta: &sandbox.Metadata{},
			want: "",
		},
		{
			name: "a killed sandbox is marked",
			meta: &sandbox.Metadata{LastOOM: &sandbox.OOMRecord{Kills: 1, Fatal: true}},
			want: "oom-killed",
		},
		{
			name: "kills inside a surviving sandbox are counted",
			meta: &sandbox.Metadata{LastOOM: &sandbox.OOMRecord{Kills: 2}},
			want: "oom-kills(2)",
		},
		{
			name: "a running session that lost a process shows both",
			meta: &sandbox.Metadata{Active: true, LastOOM: &sandbox.OOMRecord{Kills: 1}},
			want: "active, oom-kills(1)",
		},
		{
			name: "the pre-existing states are unchanged",
			meta: &sandbox.Metadata{
				Orphaned:  true,
				Active:    true,
				Isolation: sandbox.IsolationDocker,
				State:     "exited",
			},
			want: "orphaned, active, exited",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatSandboxStatus(tt.meta); got != tt.want {
				t.Errorf("formatSandboxStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}
