package overlay

import "testing"

func TestSafePath(t *testing.T) {
	tests := []struct {
		name    string
		dest    string
		want    string
		wantErr bool
	}{
		{"simple absolute path", "/home/zekker/.claude/projects", "home_zekker_.claude_projects", false},
		{"root path", "/", "", false},
		{"single segment", "/foo", "foo", false},
		{"trailing slash normalized", "/foo/bar/", "foo_bar", false},
		{"relative path rejected", "foo/bar", "", true},
		{"path traversal rejected", "/foo/../bar", "", true},
		{"empty path rejected", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SafePath(tt.dest)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("SafePath(%q) = %q, want error", tt.dest, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("SafePath(%q) unexpected error: %v", tt.dest, err)
			}
			if got != tt.want {
				t.Errorf("SafePath(%q) = %q, want %q", tt.dest, got, tt.want)
			}
		})
	}
}
