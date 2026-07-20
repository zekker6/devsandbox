package kittyproxy

import "testing"

// TestAliasesReExportMatchers guards the shim in pattern.go. The matchers
// themselves are tested in internal/cmdpattern; what this file proves is that
// kittyproxy still re-exports them correctly, because
// internal/sandbox/tools/revdiff.go builds its launch patterns through these
// names. A broken or omitted alias would otherwise surface only as a compile
// error in a different package.
func TestAliasesReExportMatchers(t *testing.T) {
	inner := CommandPattern{Program: "revdiff", ArgsMatcher: MatchAny()}

	tests := []struct {
		name    string
		matcher func([]string) bool
		args    []string
		want    bool
	}{
		{
			name:    "shell exec accepts wrapped inner command",
			matcher: MatchShellExec(inner),
			args:    []string{"-c", "revdiff --output=/tmp/out"},
			want:    true,
		},
		{
			name:    "shell exec rejects chained command",
			matcher: MatchShellExec(inner),
			args:    []string{"-c", "revdiff && rm -rf /"},
			want:    false,
		},
		{
			name:    "sentinel form accepts touch tail",
			matcher: MatchShellExecSentinel(inner),
			args:    []string{"-c", "'revdiff' '--output=/tmp/o'; touch '/tmp/sentinel'"},
			want:    true,
		},
		{
			name:    "sentinel form rejects non-touch tail",
			matcher: MatchShellExecSentinel(inner),
			args:    []string{"-c", "'revdiff' '--output=/tmp/o'; rm '/tmp/sentinel'"},
			want:    false,
		},
		{
			name:    "env sentinel form accepts /usr/bin/env prefix",
			matcher: MatchShellExecEnvSentinel(inner),
			args:    []string{"-c", "/usr/bin/env 'EDITOR=nvim' 'revdiff' '--output=/tmp/o'; touch '/tmp/sentinel'"},
			want:    true,
		},
		{
			name:    "env sentinel form rejects lowercase env name",
			matcher: MatchShellExecEnvSentinel(inner),
			args:    []string{"-c", "/usr/bin/env 'editor=nvim' 'revdiff' '--output=/tmp/o'; touch '/tmp/sentinel'"},
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.matcher(tt.args); got != tt.want {
				t.Errorf("matcher(%q) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

// TestNewOwnedSetAliasIsIntKeyed pins the OwnedSet alias to the int
// instantiation kitty needs; a change to string would compile here but break
// ExtractLaunchedWindowID's callers.
func TestNewOwnedSetAliasIsIntKeyed(t *testing.T) {
	owned := NewOwnedSet()
	owned.Add(42)

	if !owned.Contains(42) {
		t.Error("Contains(42) = false after Add(42), want true")
	}
	if owned.Contains(7) {
		t.Error("Contains(7) = true, want false")
	}
}
