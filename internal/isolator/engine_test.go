package isolator

import (
	"errors"
	"io"
	"os/user"
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
		// The helper encodes the architecture restriction only; rejecting an
		// unsupported OS is CheckMicroVM's default case. Hardening the helper to
		// reject unknown OSes here would make CheckMicroVM emit two "platform" rows.
		{name: "unknown os deferred to CheckMicroVM", goos: "windows", goarch: "amd64"},
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

// TestMicroVMArchCheck asserts the shape of the row CheckMicroVM emits for the
// architecture verdict, on every OS/arch pair rather than only the host's: the
// row is named "platform", fails, names the architecture in the one-line summary,
// and carries the helper text verbatim as remediation without duplicating it into
// the summary. Supported pairs must emit no row at all.
func TestMicroVMArchCheck(t *testing.T) {
	for _, pair := range []struct{ goos, goarch string }{
		{"darwin", "arm64"},
		{"linux", "amd64"},
		{"linux", "arm64"},
	} {
		t.Run(pair.goos+"/"+pair.goarch+" emits no row", func(t *testing.T) {
			row, ok := microVMArchCheck(pair.goos, pair.goarch)
			if !ok {
				t.Fatalf("microVMArchCheck(%q, %q) reported a gap: %+v", pair.goos, pair.goarch, row)
			}
			if row != (MicroVMCheck{}) {
				t.Errorf("supported pair produced a non-zero row: %+v", row)
			}
		})
	}

	t.Run("darwin/amd64 emits a failing platform row", func(t *testing.T) {
		row, ok := microVMArchCheck("darwin", "amd64")
		if ok {
			t.Fatal("microVMArchCheck(darwin, amd64) reported no gap")
		}
		if row.Name != "platform" {
			t.Errorf("Name = %q, want %q", row.Name, "platform")
		}
		if row.OK {
			t.Error("OK = true, want false")
		}
		if !strings.Contains(row.Summary, "amd64") {
			t.Errorf("Summary %q should name the unsupported architecture", row.Summary)
		}
		want := microVMArchSupported("darwin", "amd64").Error()
		if row.Hint != want {
			t.Errorf("Hint = %q, want the helper message verbatim %q", row.Hint, want)
		}
		if strings.Contains(row.Summary, row.Hint) {
			t.Errorf("Summary %q duplicates the remediation Hint", row.Summary)
		}
	})
}

// TestFirstMicroVMGapPrefersArchitecture pins the ordering CheckMicroVM's row
// placement exists for: with both the hardware and podman unusable, the fail-fast
// error names the architecture, so a user on an Intel Mac is not sent to install
// tools that will never help.
func TestFirstMicroVMGapPrefersArchitecture(t *testing.T) {
	archRow, ok := microVMArchCheck("darwin", "amd64")
	if ok {
		t.Fatal("microVMArchCheck(darwin, amd64) reported no gap")
	}
	checks := []MicroVMCheck{
		archRow,
		{Name: "podman", OK: false, Summary: "podman not found", Hint: "install podman"},
	}

	err := firstMicroVMGap(checks)
	if err == nil {
		t.Fatal("firstMicroVMGap returned nil with two failing checks")
	}
	msg := err.Error()
	for _, want := range []string{"amd64", "Apple Silicon", "--isolation=docker"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q, got: %q", want, msg)
		}
	}
	if strings.Contains(msg, "podman") {
		t.Errorf("error reported the podman gap ahead of the unusable hardware: %q", msg)
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

	// A mid-read failure must propagate, not be reported as "no range": the file
	// exists and may well map the user on a line the scanner never reached.
	t.Run("read error propagates", func(t *testing.T) {
		readErr := errors.New("input/output error")
		got, err := subIDMapped(io.MultiReader(strings.NewReader("someone:100000:65536\n"), errReader{readErr}), owners)
		if !errors.Is(err, readErr) {
			t.Fatalf("subIDMapped error = %v, want %v", err, readErr)
		}
		if got {
			t.Error("subIDMapped reported a mapping despite failing to read the file")
		}
	})
}

// errReader fails every read, standing in for a mid-read I/O error on a file
// that opened successfully.
type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// errCloser wraps a reader whose Close fails, standing in for a file that read
// cleanly but could not be closed.
type errCloser struct {
	io.Reader
	err error
}

func (c errCloser) Close() error { return c.err }

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
		open        func(string) (io.ReadCloser, error)
		wantOK      bool
		wantSummary string
		wantHint    []string
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
			name:    "current user unresolvable",
			userErr: errors.New("lookup failed"),
			// The remediation is the name resolution, not subordinate ranges:
			// adding ranges cannot help an account the runtime cannot resolve.
			wantSummary: "lookup failed",
			wantHint:    []string{"/etc/passwd", "LDAP/SSSD"},
		},
		{
			name: "read error is reported, not read as a missing range",
			uid:  "1000",
			open: func(string) (io.ReadCloser, error) {
				return io.NopCloser(errReader{errors.New("input/output error")}), nil
			},
			wantSummary: "input/output error",
		},
		{
			name: "close error is reported",
			uid:  "1000",
			open: func(string) (io.ReadCloser, error) {
				return errCloser{Reader: strings.NewReader(mapping), err: errors.New("close failed")}, nil
			},
			wantSummary: "close failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup := currentUser(tt.uid)
			if tt.userErr != nil {
				lookup = func() (*user.User, error) { return nil, tt.userErr }
			}
			open := tt.open
			if open == nil {
				open = openFiles(tt.files)
			}
			got := checkRootlessIDMapping(lookup, open)
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
			wantHint := tt.wantHint
			if wantHint == nil {
				wantHint = []string{"usermod", "--add-subuids", "--add-subgids", "keep-id"}
			}
			for _, want := range wantHint {
				if !strings.Contains(got.Hint, want) {
					t.Errorf("Hint missing %q, got: %q", want, got.Hint)
				}
			}
		})
	}
}
