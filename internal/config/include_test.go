package config

import (
	"os"
	"path/filepath"
	"testing"
)

func Test_matchDirPattern(t *testing.T) {
	home, _ := os.UserHomeDir()

	tests := []struct {
		name       string
		pattern    string
		projectDir string
		want       bool
	}{
		{
			name:       "exact match",
			pattern:    "dir:/home/user/work/project",
			projectDir: "/home/user/work/project",
			want:       true,
		},
		{
			name:       "glob single star",
			pattern:    "dir:/home/user/work/*",
			projectDir: "/home/user/work/project",
			want:       true,
		},
		{
			name:       "glob double star recursive",
			pattern:    "dir:/home/user/work/**",
			projectDir: "/home/user/work/nested/deep/project",
			want:       true,
		},
		{
			name:       "no match",
			pattern:    "dir:/home/user/work/**",
			projectDir: "/home/user/personal/project",
			want:       false,
		},
		{
			name:       "tilde expansion",
			pattern:    "dir:~/work/**",
			projectDir: filepath.Join(home, "work/project"),
			want:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := matchDirPattern(tt.pattern, tt.projectDir)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("matchDirPattern(%q, %q) = %v, want %v",
					tt.pattern, tt.projectDir, got, tt.want)
			}
		})
	}
}

func Test_matchDirPattern_InvalidPattern(t *testing.T) {
	// Invalid glob pattern
	_, err := matchDirPattern("dir:[invalid", "/some/path")
	if err == nil {
		t.Error("expected error for invalid glob pattern")
	}

	// Missing dir: prefix
	_, err = matchDirPattern("/home/user/work/**", "/some/path")
	if err == nil {
		t.Error("expected error for missing dir: prefix")
	}
}

func Test_parseIncludeCondition(t *testing.T) {
	tests := []struct {
		input     string
		wantType  string
		wantValue string
		wantErr   bool
	}{
		{"dir:~/work/**", "dir", "~/work/**", false},
		{"dir:/absolute/path", "dir", "/absolute/path", false},
		{"invalid", "", "", true},
		{"unknown:value", "", "", true},
		{"dir:", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			gotType, gotValue, err := parseIncludeCondition(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotType != tt.wantType || gotValue != tt.wantValue {
				t.Errorf("got (%q, %q), want (%q, %q)",
					gotType, gotValue, tt.wantType, tt.wantValue)
			}
		})
	}
}

func Test_getMatchingIncludes(t *testing.T) {
	includes := []Include{
		{If: "dir:/work/**", Path: "/config/work.toml"},
		{If: "dir:/personal/**", Path: "/config/personal.toml"},
		{If: "dir:/work/special", Path: "/config/special.toml"},
	}

	tests := []struct {
		name       string
		projectDir string
		wantPaths  []string
	}{
		{
			name:       "matches work includes",
			projectDir: "/work/project",
			wantPaths:  []string{"/config/work.toml"},
		},
		{
			name:       "matches personal includes",
			projectDir: "/personal/myproject",
			wantPaths:  []string{"/config/personal.toml"},
		},
		{
			name:       "matches multiple includes",
			projectDir: "/work/special",
			wantPaths:  []string{"/config/work.toml", "/config/special.toml"},
		},
		{
			name:       "no matches",
			projectDir: "/other/path",
			wantPaths:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := getMatchingIncludes(includes, tt.projectDir)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(got) != len(tt.wantPaths) {
				t.Fatalf("got %d includes, want %d", len(got), len(tt.wantPaths))
			}

			for i, inc := range got {
				if inc.Path != tt.wantPaths[i] {
					t.Errorf("include[%d].Path = %q, want %q", i, inc.Path, tt.wantPaths[i])
				}
			}
		})
	}
}

func Test_getMatchingIncludes_InvalidCondition(t *testing.T) {
	includes := []Include{
		{If: "invalid-condition", Path: "/config/test.toml"},
	}

	_, err := getMatchingIncludes(includes, "/some/path")
	if err == nil {
		t.Error("expected error for invalid condition")
	}
}
