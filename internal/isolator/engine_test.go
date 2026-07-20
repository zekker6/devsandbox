package isolator

import (
	"runtime"
	"strings"
	"testing"
)

// TestMicroVMArchSupported asserts the pure OS/arch gate: macOS needs Apple
// Silicon because libkrun's backend there is Hypervisor.framework, while Linux
// carries no architecture restriction (it gates on /dev/kvm instead).
func TestMicroVMArchSupported(t *testing.T) {
	tests := []struct {
		name    string
		goos    string
		goarch  string
		wantErr bool
	}{
		{name: "apple silicon mac supported", goos: "darwin", goarch: "arm64"},
		{name: "intel mac refused", goos: "darwin", goarch: "amd64", wantErr: true},
		{name: "linux amd64 supported", goos: "linux", goarch: "amd64"},
		{name: "linux arm64 supported", goos: "linux", goarch: "arm64"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := microVMArchSupported(tt.goos, tt.goarch)
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("microVMArchSupported(%q, %q) = %v, want nil", tt.goos, tt.goarch, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("microVMArchSupported(%q, %q) = nil, want fail-fast error", tt.goos, tt.goarch)
			}
			msg := err.Error()
			for _, want := range []string{"arm64", "Apple Silicon", tt.goarch, "--isolation=docker"} {
				if !strings.Contains(msg, want) {
					t.Errorf("error message missing %q, got: %q", want, msg)
				}
			}
		})
	}
}

// TestCheckMicroVMArchRow asserts the arch verdict reaches CheckMicroVM without
// double-wrapping the helper's message: the row carries a concise summary plus
// the helper text verbatim as remediation, and leads the slice so Available()
// reports the unusable hardware before any missing tool.
func TestCheckMicroVMArchRow(t *testing.T) {
	checks := CheckMicroVM()
	if len(checks) == 0 {
		t.Fatal("CheckMicroVM returned no checks")
	}

	archErr := microVMArchSupported(runtime.GOOS, runtime.GOARCH)
	first := checks[0]

	if archErr == nil {
		if first.Name == "platform" && !first.OK {
			t.Errorf("host %s/%s is supported but CheckMicroVM reported %+v", runtime.GOOS, runtime.GOARCH, first)
		}
		return
	}

	if first.Name != "platform" || first.OK {
		t.Fatalf("expected a leading failing platform row on %s/%s, got %+v", runtime.GOOS, runtime.GOARCH, first)
	}
	if first.Hint != archErr.Error() {
		t.Errorf("platform Hint = %q, want the helper message verbatim %q", first.Hint, archErr.Error())
	}
	if !strings.Contains(first.Summary, runtime.GOARCH) {
		t.Errorf("platform Summary %q should name the host architecture %q", first.Summary, runtime.GOARCH)
	}
	if strings.Contains(first.Summary, first.Hint) {
		t.Errorf("platform Summary %q duplicates the remediation Hint", first.Summary)
	}
}
