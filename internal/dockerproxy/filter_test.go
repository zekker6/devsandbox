package dockerproxy

import "testing"

func TestIsAllowed_GET(t *testing.T) {
	tests := []struct {
		path string
	}{
		{"/containers/json"},
		{"/v1.41/containers/json"},
		{"/images/json"},
		{"/info"},
		{"/version"},
		{"/_ping"},
		{"/containers/abc123/json"},
		{"/containers/abc123/logs"},
		{"/networks"},
		{"/volumes"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if !IsAllowed("GET", tt.path) {
				t.Errorf("GET %s should be allowed", tt.path)
			}
		})
	}
}

func TestIsAllowed_HEAD(t *testing.T) {
	tests := []struct {
		path string
	}{
		{"/_ping"},
		{"/v1.41/_ping"},
		{"/containers/abc123/json"},
		{"/info"},
		{"/version"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if !IsAllowed("HEAD", tt.path) {
				t.Errorf("HEAD %s should be allowed", tt.path)
			}
		})
	}
}

func TestIsAllowed_ExecAttach(t *testing.T) {
	tests := []struct {
		method string
		path   string
	}{
		{"POST", "/containers/abc123/exec"},
		{"POST", "/v1.41/containers/abc123/exec"},
		{"POST", "/exec/abc123/start"},
		{"POST", "/v1.41/exec/abc123/start"},
		{"POST", "/containers/abc123/attach"},
		{"POST", "/v1.41/containers/abc123/attach"},
		{"POST", "/containers/my-container_name.1/exec"},
		{"POST", "/containers/My-Container.Name_123/attach"},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			if !IsAllowed(tt.method, tt.path) {
				t.Errorf("%s %s should be allowed", tt.method, tt.path)
			}
		})
	}
}

func TestIsAllowed_Denied(t *testing.T) {
	tests := []struct {
		method string
		path   string
	}{
		{"POST", "/containers/create"},
		{"POST", "/v1.41/containers/create"},
		{"DELETE", "/containers/abc123"},
		{"POST", "/containers/abc123/stop"},
		{"POST", "/containers/abc123/kill"},
		{"POST", "/containers/abc123/restart"},
		{"POST", "/images/create"},
		{"DELETE", "/images/abc123"},
		{"POST", "/build"},
		{"PUT", "/containers/abc123/archive"},
		{"POST", "/networks/create"},
		{"DELETE", "/networks/abc123"},
		{"POST", "/volumes/create"},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			if IsAllowed(tt.method, tt.path) {
				t.Errorf("%s %s should be denied", tt.method, tt.path)
			}
		})
	}
}

func TestDenyReason(t *testing.T) {
	reason := DenyReason("POST", "/containers/create")
	if reason == "" {
		t.Error("expected non-empty deny reason")
	}

	reason = DenyReason("GET", "/containers/json")
	if reason != "" {
		t.Errorf("expected empty deny reason for allowed request, got %q", reason)
	}
}
