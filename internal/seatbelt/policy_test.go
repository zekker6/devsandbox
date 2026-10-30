package seatbelt

import (
	"context"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func mustProfile(t *testing.T, p Policy) string {
	t.Helper()
	profile, err := p.Profile()
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func TestProfileRejectsUnsafePaths(t *testing.T) {
	paths := []string{"", "relative", "./file", "/a/../b", "/a/./b", "/a//b", "//a", "/a/", "/a\x00b", "/a\nb", "/a\rb", "/a\tb", "/a\x7fb", "/a\u009bb", "/a\u202eb", "/a\xff"}
	for _, name := range paths {
		t.Run(name, func(t *testing.T) {
			for _, p := range []Policy{
				{ReadOnly: []string{name}}, {Writable: []string{name}},
				{DenyRead: []string{name}}, {DenyWrite: []string{name}},
				{UnixSocketRoots: []string{name}},
			} {
				profile, err := p.Profile()
				if err == nil || profile != "" {
					t.Fatalf("unsafe path %q returned profile %q, error %v", name, profile, err)
				}
			}
		})
	}
}

func TestProfileQuotesPaths(t *testing.T) {
	name := `/work/a" (allow default) ;\résumé`
	profile := mustProfile(t, Policy{Writable: []string{name}})
	want := `(allow file-read* file-write* (subpath "/work/a\" (allow default) ;\\résumé"))`
	if !strings.Contains(profile, want+"\n") {
		t.Fatalf("missing safely encoded rule %s in:\n%s", want, profile)
	}
	if strings.Contains(profile, `\x`) || strings.Contains(profile, `\u00`) {
		t.Fatal("profile used Go rather than Scheme string escapes")
	}
}

func TestProfileGrantsThenDenies(t *testing.T) {
	p := Policy{
		Writable: []string{"/work", "/secrets"}, ReadOnly: []string{"/work/config"},
		DenyRead: []string{"/secrets/token"}, DenyWrite: []string{"/work/repo/.git"},
		HideEnvFiles: true, ProtectConfig: true, Network: true, ProxyPort: 3128,
	}
	profile := mustProfile(t, p)
	lastGrant := strings.LastIndex(profile, "(allow ")
	for _, rule := range []string{
		`(deny file-write* (subpath "/work/config"))`,
		`(deny file-read* file-write* (subpath "/secrets/token"))`,
		`(deny file-write* (subpath "/work/repo/.git"))`,
		`(deny file-write-unlink (literal "/"))`,
		`(deny file-write-unlink (literal "/work"))`,
		`(deny file-write-unlink (literal "/work/repo"))`,
		`(deny file-write-unlink (literal "/secrets"))`,
		`(deny file-read* file-write* (regex #"(^|/)[.][eE][nN][vV]([.][^/]*)?(/|$)"))`,
		`(deny file-write* (regex #"(^|/)[.][dD][eE][vV][sS][aA][nN][dD][bB][oO][xX][.][tT][oO][mM][lL](/|$)"))`,
	} {
		if i := strings.Index(profile, rule); i <= lastGrant {
			t.Errorf("denial missing or precedes a grant: %s", rule)
		}
	}
	if strings.Contains(profile, `(deny file-write* (subpath "/work"))`) {
		t.Fatal("ancestor protection denied ordinary project modification")
	}
	if got := strings.Count(profile, `(deny file-write-unlink (literal "/work"))`); got != 1 {
		t.Fatalf("ancestor denial emitted %d times", got)
	}
	for range 10 {
		if got := mustProfile(t, p); got != profile {
			t.Fatal("profile generation is nondeterministic")
		}
	}
}

func TestProfileWritableRootCannotBeReplaced(t *testing.T) {
	profile := mustProfile(t, Policy{Writable: []string{"/sandbox/home"}})
	lastGrant := strings.LastIndex(profile, "(allow ")
	for _, name := range []string{"/sandbox/home", "/sandbox", "/"} {
		rule := `(deny file-write-unlink (literal "` + name + `"))`
		if strings.Index(profile, rule) <= lastGrant {
			t.Errorf("missing final root/ancestor unlink protection: %s", rule)
		}
	}
	if strings.Contains(profile, `(deny file-write* (subpath "/sandbox/home"))`) {
		t.Fatal("root protection prevents writing its contents")
	}
}

func TestProfileNetwork(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    Policy
		want string
	}{
		{"off", Policy{}, ""},
		{"outbound", Policy{Network: true}, `(allow network-outbound (remote ip "*:*"))`},
		{"proxy", Policy{Network: true, ProxyPort: 8080}, `(allow network-outbound (require-all (socket-domain AF_INET) (remote tcp "localhost:8080")))`},
		{"lowest", Policy{Network: true, ProxyPort: 1}, `(allow network-outbound (require-all (socket-domain AF_INET) (remote tcp "localhost:1")))`},
		{"highest", Policy{Network: true, ProxyPort: 65535}, `(allow network-outbound (require-all (socket-domain AF_INET) (remote tcp "localhost:65535")))`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := mustProfile(t, tc.p)
			if tc.want == "" {
				if strings.Contains(profile, "(allow network-") {
					t.Fatal("network granted while disabled")
				}
			} else if !strings.Contains(profile, tc.want+"\n") || strings.Count(profile, "(allow network-") != 1 {
				t.Fatalf("expected only network rule %s", tc.want)
			}
			for _, forbidden := range []string{"network-bind", "network-inbound", "unix-socket", "system-socket", "mach-lookup"} {
				if strings.Contains(profile, forbidden) {
					t.Errorf("unexpected grant: %s", forbidden)
				}
			}
		})
	}
	for _, p := range []Policy{{ProxyPort: -1}, {Network: true, ProxyPort: 65536}, {ProxyPort: 8080}} {
		if profile, err := p.Profile(); err == nil || profile != "" {
			t.Errorf("invalid policy %+v returned %q, %v", p, profile, err)
		}
	}
}

func TestProfilePrivateUnixSockets(t *testing.T) {
	roots := []string{"/sandbox/home/.runtime", "/sandbox/home/.tmp"}
	for _, tc := range []struct {
		name string
		p    Policy
		ip   string
	}{
		{"off", Policy{}, ""},
		{"proxy", Policy{Network: true, ProxyPort: 8080}, `(allow network-outbound (require-all (socket-domain AF_INET) (remote tcp "localhost:8080")))`},
		{"outbound", Policy{Network: true}, `(allow network-outbound (remote ip "*:*"))`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.p.UnixSocketRoots = roots
			profile := mustProfile(t, tc.p)
			var networkRules []string
			for line := range strings.SplitSeq(profile, "\n") {
				if strings.HasPrefix(line, "(allow network-") || strings.HasPrefix(line, "(allow system-socket") {
					networkRules = append(networkRules, line)
				}
			}
			want := []string{
				`(allow system-socket (socket-domain AF_UNIX))`,
				`(allow network-bind network-inbound (local unix-socket (subpath "/sandbox/home/.runtime")))`,
				`(allow network-outbound (remote unix-socket (subpath "/sandbox/home/.runtime")))`,
				`(allow network-bind network-inbound (local unix-socket (subpath "/sandbox/home/.tmp")))`,
				`(allow network-outbound (remote unix-socket (subpath "/sandbox/home/.tmp")))`,
			}
			if tc.ip != "" {
				want = append(want, tc.ip)
			}
			if !reflect.DeepEqual(networkRules, want) {
				t.Fatalf("network grants = %q, want %q", networkRules, want)
			}
			for _, root := range roots {
				if strings.Contains(profile, `(allow file-read* file-write* (subpath "`+root+`"))`) ||
					strings.Contains(profile, `(allow file-read* (subpath "`+root+`"))`) {
					t.Fatalf("socket root implicitly granted filesystem access: %s", root)
				}
				if !strings.Contains(profile, `(deny file-write-unlink (literal "`+root+`"))`) {
					t.Fatalf("socket root can be replaced: %s", root)
				}
			}
		})
	}
}

func TestProfileQuotesUnixSocketRoots(t *testing.T) {
	name := `/sandbox/a" (allow default) ;\résumé`
	profile := mustProfile(t, Policy{UnixSocketRoots: []string{name}})
	for _, rule := range []string{
		`(allow network-bind network-inbound (local unix-socket (subpath "/sandbox/a\" (allow default) ;\\résumé")))`,
		`(allow network-outbound (remote unix-socket (subpath "/sandbox/a\" (allow default) ;\\résumé")))`,
	} {
		if !strings.Contains(profile, rule+"\n") {
			t.Fatalf("missing safely encoded rule %s in:\n%s", rule, profile)
		}
	}
}

func TestProfileBaselineRootDirectory(t *testing.T) {
	profile := mustProfile(t, Policy{})
	if !strings.Contains(profile, "(allow file-read* (literal \"/\"))\n") {
		t.Fatal("baseline must allow reading the root directory for dyld startup")
	}
	if strings.Count(profile, `(literal "/")`) != 1 {
		t.Fatal("baseline root access must be limited to the read-only rule")
	}
}

func TestProfileBaselineDoesNotExposeHostData(t *testing.T) {
	profile := mustProfile(t, Policy{})
	if !strings.HasPrefix(profile, "(version 1)\n(deny default)\n") {
		t.Fatal("profile is not deny-by-default")
	}
	for _, name := range []string{"/", "/Users", "/home", "/private", "/private/var", "/private/etc", "/tmp", "/private/tmp", "/Volumes", "/dev"} {
		if strings.Contains(profile, `(subpath "`+name+`")`) {
			t.Errorf("broad baseline filesystem grant: %s", name)
		}
	}
	for _, forbidden := range []string{"mach-lookup", "iokit-open", "ipc-posix", "AppleEvents", "lsopen", "trustd", "SecurityServer", "sysctl-write"} {
		if strings.Contains(profile, forbidden) {
			t.Errorf("unexpected baseline capability: %s", forbidden)
		}
	}
}

func TestProfileConfigVisibility(t *testing.T) {
	for _, p := range []Policy{{ProtectConfig: true}, {ConfigHidden: true}, {ProtectConfig: true, ConfigHidden: true}} {
		profile := mustProfile(t, p)
		operations := "file-write*"
		if p.ConfigHidden {
			operations = "file-read* file-write*"
		}
		want := `(deny ` + operations + ` (regex #"(^|/)[.][dD][eE][vV][sS][aA][nN][dD][bB][oO][xX][.][tT][oO][mM][lL](/|$)"))`
		if !strings.Contains(profile, want) {
			t.Errorf("missing config protection: %s", want)
		}
	}
	if strings.Contains(mustProfile(t, Policy{}), "devsandbox[.]toml") {
		t.Fatal("unexpected config protection when disabled")
	}
}

func TestCheckCommandSanitizesEnvironment(t *testing.T) {
	for _, name := range []string{"DYLD_INSERT_LIBRARIES", "DYLD_LIBRARY_PATH", "BASH_ENV", "ENV", "HOME", "TMPDIR"} {
		t.Setenv(name, "/untrusted")
	}
	cmd := checkCommand(context.Background(), "(version 1)")
	wantArgs := []string{Executable, "-p", "(version 1)", "/usr/bin/true"}
	if cmd.Path != Executable || !reflect.DeepEqual(cmd.Args, wantArgs) || cmd.Dir != "/" {
		t.Fatalf("unsafe check command: path %q, argv %q, dir %q", cmd.Path, cmd.Args, cmd.Dir)
	}
	wantEnv := []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "LC_ALL=C"}
	if !reflect.DeepEqual(cmd.Env, wantEnv) {
		t.Fatalf("check inherited host environment: %q", cmd.Env)
	}
}

func TestBoundedOutput(t *testing.T) {
	b := boundedOutput{limit: 6}
	for _, text := range []string{"ab\x1b", "cd\nef", "discarded"} {
		if n, err := b.Write([]byte(text)); n != len(text) || err != nil {
			t.Fatalf("write returned %d, %v", n, err)
		}
	}
	if len(b.data) != 6 || b.String() != `"ab\x1bcd\n" (truncated)` {
		t.Fatalf("unexpected bounded output %q", b.String())
	}
	b = boundedOutput{limit: 3}
	if _, err := b.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if b.String() != `"abc"` {
		t.Fatalf("exactly-full output incorrectly truncated: %s", b.String())
	}
}

func TestUnavailableOutsideDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("non-Darwin availability check")
	}
	if err := Available(); err == nil {
		t.Fatal("Seatbelt reported available outside macOS")
	}
	if err := Check(context.Background(), mustProfile(t, Policy{})); err == nil {
		t.Fatal("profile check silently succeeded without Seatbelt")
	}
}
