package isolator

import (
	"errors"
	"io"
	"os/user"
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

// TestCheckSystemPasta asserts the doctor row reports the resolved path when the
// system pasta binary is present and a warn-with-remediation when it is not. The
// binary is distinct from the pasta devsandbox embeds for bwrap: podman only
// sees one on PATH.
func TestCheckSystemPasta(t *testing.T) {
	tests := []struct {
		name    string
		present bool
		wantOK  bool
	}{
		{name: "pasta present", present: true, wantOK: true},
		{name: "pasta missing", present: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkSystemPasta(func(string) (string, error) {
				if tt.present {
					return "/usr/bin/pasta", nil
				}
				return "", errors.New("not found")
			})
			if got.Name != "system pasta" {
				t.Errorf("Name = %q, want %q", got.Name, "system pasta")
			}
			if got.OK != tt.wantOK {
				t.Fatalf("OK = %v, want %v (summary %q)", got.OK, tt.wantOK, got.Summary)
			}
			if tt.wantOK {
				if got.Summary != "/usr/bin/pasta" {
					t.Errorf("Summary = %q, want the resolved path", got.Summary)
				}
				if got.Hint != "" {
					t.Errorf("Hint = %q, want empty when satisfied", got.Hint)
				}
				return
			}
			if !strings.Contains(got.Hint, "passt") {
				t.Errorf("Hint %q should name the passt package", got.Hint)
			}
		})
	}
}

// TestSubIDMapped covers the pure /etc/subuid and /etc/subgid parser: a matching
// allocation by name or numeric id counts, everything else (missing owner, empty
// file, malformed or zero-count lines, comments) does not, and a stray line never
// masks a valid entry further down.
func TestSubIDMapped(t *testing.T) {
	owners := []string{"zekker", "1000"}

	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{name: "entry present", content: "zekker:100000:65536\n", want: true},
		{name: "entry present without trailing newline", content: "zekker:100000:65536", want: true},
		{name: "entry keyed by numeric id", content: "1000:100000:65536\n", want: true},
		{name: "other user only", content: "someone:100000:65536\nroot:0:1\n"},
		{name: "empty file", content: ""},
		{name: "blanks and comments only", content: "\n# zekker:100000:65536\n   \n"},
		{name: "too few fields", content: "zekker:100000\n"},
		{name: "too many fields", content: "zekker:100000:65536:extra\n"},
		{name: "non numeric start", content: "zekker:start:65536\n"},
		{name: "non numeric count", content: "zekker:100000:many\n"},
		{name: "zero count", content: "zekker:100000:0\n"},
		{name: "malformed line before a valid one", content: "zekker:bogus\n\nzekker:100000:65536\n", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := subIDMapped(strings.NewReader(tt.content), owners)
			if err != nil {
				t.Fatalf("subIDMapped returned error: %v", err)
			}
			if got != tt.want {
				t.Errorf("subIDMapped(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}

// TestCheckRootlessIDMapping asserts the probe verdict for each state podman can
// find the subordinate id databases in, including an unreadable file (reported,
// never swallowed) and root (which maps no subordinate ids and must not be
// warned about).
func TestCheckRootlessIDMapping(t *testing.T) {
	const mapping = "zekker:100000:65536\n"

	currentUser := func(uid string) func() (*user.User, error) {
		return func() (*user.User, error) { return &user.User{Uid: uid, Username: "zekker"}, nil }
	}
	openFiles := func(files map[string]string) func(string) (io.ReadCloser, error) {
		return func(path string) (io.ReadCloser, error) {
			content, ok := files[path]
			if !ok {
				return nil, errors.New("no such file or directory")
			}
			return io.NopCloser(strings.NewReader(content)), nil
		}
	}

	tests := []struct {
		name        string
		uid         string
		userErr     error
		files       map[string]string
		wantOK      bool
		wantSummary string
	}{
		{
			name:   "both databases map the user",
			uid:    "1000",
			files:  map[string]string{subUIDPath: mapping, subGIDPath: mapping},
			wantOK: true,
		},
		{
			name:        "subuid entry missing",
			uid:         "1000",
			files:       map[string]string{subUIDPath: "someone:100000:65536\n", subGIDPath: mapping},
			wantSummary: subUIDPath,
		},
		{
			name:        "subgid entry missing",
			uid:         "1000",
			files:       map[string]string{subUIDPath: mapping, subGIDPath: ""},
			wantSummary: subGIDPath,
		},
		{
			name:        "database unreadable",
			uid:         "1000",
			files:       nil,
			wantSummary: "no such file",
		},
		{
			name:        "root needs no subordinate ranges",
			uid:         "0",
			files:       nil,
			wantOK:      true,
			wantSummary: "root",
		},
		{
			name:        "current user unresolvable",
			userErr:     errors.New("lookup failed"),
			wantSummary: "lookup failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup := currentUser(tt.uid)
			if tt.userErr != nil {
				lookup = func() (*user.User, error) { return nil, tt.userErr }
			}
			got := checkRootlessIDMapping(lookup, openFiles(tt.files))
			if got.Name != rootlessIDMappingName {
				t.Errorf("Name = %q, want %q", got.Name, rootlessIDMappingName)
			}
			if got.OK != tt.wantOK {
				t.Fatalf("OK = %v, want %v (summary %q)", got.OK, tt.wantOK, got.Summary)
			}
			if tt.wantSummary != "" && !strings.Contains(got.Summary, tt.wantSummary) {
				t.Errorf("Summary = %q, want it to mention %q", got.Summary, tt.wantSummary)
			}
			if tt.wantOK {
				if got.Hint != "" {
					t.Errorf("Hint = %q, want empty when satisfied", got.Hint)
				}
				return
			}
			for _, want := range []string{"usermod", "--add-subuids", "--add-subgids", "keep-id"} {
				if !strings.Contains(got.Hint, want) {
					t.Errorf("Hint missing %q, got: %q", want, got.Hint)
				}
			}
		})
	}
}

// TestKrunAdvisoryProbesDegradeToWarnings guards the opt-in invariant at the
// source: with every prerequisite missing both probes still return a structured
// MicroVMCheck carrying remediation, so doctor renders a warn row instead of
// failing a host that never runs krun.
func TestKrunAdvisoryProbesDegradeToWarnings(t *testing.T) {
	missing := func(string) (string, error) { return "", errors.New("not found") }
	noFiles := func(string) (io.ReadCloser, error) { return nil, errors.New("no such file or directory") }
	asUser := func() (*user.User, error) { return &user.User{Uid: "1000", Username: "zekker"}, nil }

	checks := []MicroVMCheck{
		checkSystemPasta(missing),
		checkRootlessIDMapping(asUser, noFiles),
	}
	for _, c := range checks {
		if c.OK {
			t.Errorf("probe %q reported OK with nothing installed", c.Name)
		}
		if c.Name == "" || c.Summary == "" || c.Hint == "" {
			t.Errorf("probe %+v must carry a name, summary, and remediation hint", c)
		}
	}
}
