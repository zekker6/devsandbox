//go:build !linux

package cgroups

import (
	"strings"
	"testing"
)

func TestPreflight_NonLinuxRejectsLimits(t *testing.T) {
	tests := []struct {
		name   string
		limits Limits
	}{
		{name: "memory", limits: Limits{Memory: "4g"}},
		{name: "cpus", limits: Limits{CPUs: "2"}},
		{name: "pids", limits: Limits{PIDs: 64}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Preflight(tt.limits)
			if err == nil {
				t.Fatal("Preflight() = nil, want an error on a non-Linux platform")
			}
			if !strings.Contains(err.Error(), "only supported on Linux") {
				t.Errorf("Preflight() error = %q, want it to name the Linux requirement", err)
			}
		})
	}
}
