package shellwrap

import (
	"slices"
	"strings"
	"testing"

	"devsandbox/internal/agentid"
)

// wantReservedCommandNames is the full reserved table, spelled out here rather
// than read back from the implementation so a name added to the generator's
// vocabulary has to be added in both places deliberately.
var wantReservedCommandNames = []string{
	"_", "alias", "and", "argparse", "begin", "break", "builtin", "case", "command",
	"continue", "coproc", "declare", "do", "done", "elif", "else", "end", "esac",
	"eval", "exec", "export", "fi", "float", "for", "foreach", "function",
	"functions", "if", "in", "integer", "local", "nocorrect", "noglob", "not", "or",
	"printf", "read", "readonly", "repeat", "return", "select", "set", "source",
	"status", "string", "switch", "test", "then", "time", "type", "typeset",
	"unalias", "unset", "until", "while",
}

func TestReservedCommandNames_PinnedTable(t *testing.T) {
	got := slices.Sorted(slices.Values(reservedCommandNames))
	want := slices.Sorted(slices.Values(wantReservedCommandNames))
	if !slices.Equal(got, want) {
		t.Fatalf("reserved command names = %v, want %v", got, want)
	}
}

func TestValidateCommandName_RejectsReservedNames(t *testing.T) {
	for _, name := range wantReservedCommandNames {
		t.Run(name, func(t *testing.T) {
			err := ValidateCommandName(name)
			if err == nil {
				t.Fatalf("ValidateCommandName(%q) = nil, want error", name)
			}
			if !strings.Contains(err.Error(), "reserved") || !strings.Contains(err.Error(), `"`+name+`"`) {
				t.Errorf("error %q does not name the reserved word %q", err, name)
			}
		})
	}
}

func TestValidateCommandName_RejectsSupportedAgents(t *testing.T) {
	agents := agentid.KnownAgents()
	if len(agents) == 0 {
		t.Fatal("agentid.KnownAgents() is empty")
	}
	for _, name := range agents {
		for _, spelling := range []string{name, strings.ToUpper(name)} {
			t.Run(spelling, func(t *testing.T) {
				err := ValidateCommandName(spelling)
				if err == nil {
					t.Fatalf("ValidateCommandName(%q) = nil, want error", spelling)
				}
				if !strings.Contains(err.Error(), "agent") || !strings.Contains(err.Error(), `"`+spelling+`"`) {
					t.Errorf("error %q does not explain the agent collision for %q", err, spelling)
				}
			})
		}
	}
}

func TestValidateCommandName_Invalid(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr string
	}{
		{"empty", "", "empty command name"},
		{"absolute path", "/usr/bin/npm", "not a path"},
		{"relative path", "bin/npm", "not a path"},
		{"dot slash path", "./npm", "not a path"},
		{"space", "np m", "only [A-Za-z0-9_-] is allowed"},
		{"semicolon", "npm;rm", "only [A-Za-z0-9_-] is allowed"},
		{"dollar", "$npm", "only [A-Za-z0-9_-] is allowed"},
		{"dot", "node.js", "only [A-Za-z0-9_-] is allowed"},
		{"quote", "npm'", "only [A-Za-z0-9_-] is allowed"},
		{"non-ascii", "npmé", "only [A-Za-z0-9_-] is allowed"},
		{"newline", "npm\nrm", "only [A-Za-z0-9_-] is allowed"},
		{"leading hyphen", "-npm", `must not start with "-"`},
		{"bare suffix", "-no-ds", `must not start with "-"`},
		{"reserved suffix", "npm-no-ds", `must not end in "-no-ds"`},
		{"reserved suffix on agent", "claude-no-ds", `must not end in "-no-ds"`},
		{"devsandbox", "devsandbox", "devsandbox"},
		{"devsandbox upper", "DevSandbox", "devsandbox"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCommandName(tt.input)
			if err == nil {
				t.Fatalf("ValidateCommandName(%q) = nil, want error containing %q", tt.input, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("ValidateCommandName(%q) error = %q, want it to contain %q", tt.input, err, tt.wantErr)
			}
			if tt.input != "" && !strings.Contains(err.Error(), `"`+strings.ReplaceAll(tt.input, "\n", `\n`)+`"`) {
				t.Errorf("ValidateCommandName(%q) error = %q, want it to name the offending value", tt.input, err)
			}
		})
	}
}

func TestValidateCommandName_Allowed(t *testing.T) {
	for _, name := range []string{
		"npm", "bun", "node", "npx", "pnpm", "yarn", "deno", "python3", "pip",
		"uv", "cargo", "go", "make", "x", "_tool", "my-tool", "Tool_2",
		// Collide with devsandbox management subcommands; run-command keeps them
		// unambiguous, so they stay valid wrapper names.
		"config", "tools", "sandboxes", "run-agent",
		// Only the exact suffix is reserved.
		"no-ds", "npm-no-dsx", "no-ds-npm",
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateCommandName(name); err != nil {
				t.Errorf("ValidateCommandName(%q) = %v, want nil", name, err)
			}
		})
	}
}
