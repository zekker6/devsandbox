package egress

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// testTools is the resolved-binary set the rule builders are given in tests.
// Every emitted argv[0] must be one of these absolute paths - never a bare name.
var testTools = Tools{IP: "/usr/sbin/ip", Firewall: "/usr/sbin/nft", Backend: BackendNft}

var testIptablesTools = Tools{IP: "/usr/sbin/ip", Firewall: "/usr/sbin/iptables", Backend: BackendIptables}

// testReadyFile is the marker path the rendering-only tests supply. Script
// refuses a lockdown without one, so it is part of every valid test lockdown;
// the tests that actually run the script use a path under their own t.TempDir()
// instead, because for them whether the file appears is the assertion.
const testReadyFile = "/run/devsandbox-test/applied"

func TestRouteCommands(t *testing.T) {
	tests := []struct {
		name    string
		gateway string
		dev     string
		want    [][]string
	}{
		{
			name:    "gateway and tap device",
			gateway: "10.0.2.2",
			dev:     "enp5s0",
			want: [][]string{
				{"/usr/sbin/ip", "route", "add", "10.0.2.2/32", "dev", "enp5s0"},
				{"/usr/sbin/ip", "route", "del", "default"},
			},
		},
		{
			name:    "alternate gateway and device substituted",
			gateway: "192.168.99.1",
			dev:     "eth0",
			want: [][]string{
				{"/usr/sbin/ip", "route", "add", "192.168.99.1/32", "dev", "eth0"},
				{"/usr/sbin/ip", "route", "del", "default"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RouteCommands(testTools, tt.gateway, tt.dev)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("RouteCommands(%q, %q) = %v, want %v", tt.gateway, tt.dev, got, tt.want)
			}
		})
	}
}

// TestRouteCommands_Ordering asserts the add-route command precedes the
// del-default command so the gateway stays reachable while the default route is
// removed.
func TestRouteCommands_Ordering(t *testing.T) {
	cmds := RouteCommands(testTools, "10.0.2.2", "enp5s0")
	if len(cmds) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(cmds))
	}
	if cmds[0][1] != "route" || cmds[0][2] != "add" {
		t.Errorf("first command must be an `ip route add`, got %v", cmds[0])
	}
	if cmds[1][1] != "route" || cmds[1][2] != "del" {
		t.Errorf("second command must be an `ip route del`, got %v", cmds[1])
	}
}

func lookPathFor(present ...string) func(string) (string, error) {
	set := map[string]bool{}
	for _, p := range present {
		set[p] = true
	}
	return func(name string) (string, error) {
		if set[name] {
			return "/usr/sbin/" + name, nil
		}
		return "", errors.New("not found")
	}
}

// lookPathOnlyAbsolute simulates a host where the PATH lookup fails for every
// bare name (the /usr/sbin-not-on-PATH case) and only the explicit sbin probe,
// which passes an absolute path, finds anything.
func lookPathOnlyAbsolute(present ...string) func(string) (string, error) {
	set := map[string]bool{}
	for _, p := range present {
		set[p] = true
	}
	return func(name string) (string, error) {
		if !strings.Contains(name, "/") {
			return "", errors.New("not in PATH")
		}
		if set[name] {
			return name, nil
		}
		return "", errors.New("not found")
	}
}

// TestResolveTools asserts the binaries resolve to absolute paths and that a
// missing one aborts with the named error rather than degrading to a bare name
// that would silently fail to apply the lockdown.
func TestResolveTools(t *testing.T) {
	tests := []struct {
		name        string
		lookPath    func(string) (string, error)
		wantIP      string
		wantFW      string
		wantBackend Backend
		wantErr     error
	}{
		{
			name:        "ip and nft from PATH",
			lookPath:    lookPathFor("ip", "nft", "iptables"),
			wantIP:      "/usr/sbin/ip",
			wantFW:      "/usr/sbin/nft",
			wantBackend: BackendNft,
		},
		{
			name:        "iptables fallback",
			lookPath:    lookPathFor("ip", "iptables"),
			wantIP:      "/usr/sbin/ip",
			wantFW:      "/usr/sbin/iptables",
			wantBackend: BackendIptables,
		},
		{
			name:     "missing ip aborts",
			lookPath: lookPathFor("nft", "iptables"),
			wantErr:  ErrNoIPBinary,
		},
		{
			name:     "missing both firewall binaries aborts",
			lookPath: lookPathFor("ip"),
			wantErr:  ErrNoFirewallBackend,
		},
		{
			name:        "sbin fallback when PATH lookup fails",
			lookPath:    lookPathOnlyAbsolute("/sbin/ip", "/usr/sbin/nft"),
			wantIP:      "/sbin/ip",
			wantFW:      "/usr/sbin/nft",
			wantBackend: BackendNft,
		},
		{
			name: "relative PATH hit is rejected in favor of the sbin probe",
			lookPath: func(name string) (string, error) {
				switch name {
				case "ip", "nft":
					return "./" + name, nil
				case "/usr/sbin/ip", "/usr/sbin/nft":
					return name, nil
				}
				return "", errors.New("not found")
			},
			wantIP:      "/usr/sbin/ip",
			wantFW:      "/usr/sbin/nft",
			wantBackend: BackendNft,
		},
		{
			name: "relative-only hit aborts rather than emitting a relative path",
			lookPath: func(name string) (string, error) {
				if !strings.Contains(name, "/") || strings.HasPrefix(name, "./") {
					return "./" + name, nil
				}
				return "", errors.New("not found")
			},
			wantErr: ErrNoIPBinary,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveTools(tt.lookPath)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("ResolveTools error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.IP != tt.wantIP || got.Firewall != tt.wantFW || got.Backend != tt.wantBackend {
				t.Errorf("ResolveTools = %+v, want {IP:%s Firewall:%s Backend:%v}", got, tt.wantIP, tt.wantFW, tt.wantBackend)
			}
		})
	}
}

// TestDetectBackend asserts nft is preferred over iptables, iptables is the
// fallback, and neither present resolves to BackendNone (which callers turn into
// a fail-closed error).
func TestDetectBackend(t *testing.T) {
	tests := []struct {
		name     string
		lookPath func(string) (string, error)
		want     Backend
		wantPath string
	}{
		{name: "nft preferred over iptables", lookPath: lookPathFor("nft", "iptables"), want: BackendNft, wantPath: "/usr/sbin/nft"},
		{name: "nft only", lookPath: lookPathFor("nft"), want: BackendNft, wantPath: "/usr/sbin/nft"},
		{name: "iptables fallback", lookPath: lookPathFor("iptables"), want: BackendIptables, wantPath: "/usr/sbin/iptables"},
		{name: "neither present", lookPath: lookPathFor(), want: BackendNone, wantPath: ""},
		{name: "sbin probe when PATH lookup fails", lookPath: lookPathOnlyAbsolute("/sbin/iptables"), want: BackendIptables, wantPath: "/sbin/iptables"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotPath := DetectBackend(tt.lookPath)
			if got != tt.want {
				t.Errorf("DetectBackend backend = %v, want %v", got, tt.want)
			}
			if gotPath != tt.wantPath {
				t.Errorf("DetectBackend path = %q, want %q", gotPath, tt.wantPath)
			}
		})
	}
}

// TestFirewallCommands asserts the deny-by-default firewall rules: the chain
// drops by default and accepts only established/related return traffic, loopback,
// and TCP to gateway:proxyPort. This is the guard against the LAN/metadata
// exposure route surgery alone leaves open (the connected subnet route survives
// `ip route del default`) and the host-loopback exposure (--map-host-loopback
// maps ALL ports of the gateway to host 127.0.0.1).
func TestFirewallCommands(t *testing.T) {
	tests := []struct {
		name     string
		tools    Tools
		lockdown Lockdown
		want     [][]string
		wantErr  bool
	}{
		{
			name:     "nft deny-by-default, allow only proxy port",
			tools:    testTools,
			lockdown: Lockdown{Enabled: true, Gateway: "10.0.2.2", ProxyPort: 8080},
			want: [][]string{
				{"/usr/sbin/nft", "add", "table", "ip", "devsandbox_egress"},
				{"/usr/sbin/nft", "add", "chain", "ip", "devsandbox_egress", "output", "{ type filter hook output priority 0 ; policy drop ; }"},
				{"/usr/sbin/nft", "add", "rule", "ip", "devsandbox_egress", "output", "ct", "state", "established,related", "accept"},
				{"/usr/sbin/nft", "add", "rule", "ip", "devsandbox_egress", "output", "oif", "lo", "accept"},
				{"/usr/sbin/nft", "add", "rule", "ip", "devsandbox_egress", "output", "ip", "daddr", "10.0.2.2", "tcp", "dport", "8080", "accept"},
			},
		},
		{
			name:     "iptables fallback deny-by-default, allow only proxy port",
			tools:    testIptablesTools,
			lockdown: Lockdown{Enabled: true, Gateway: "192.168.99.1", ProxyPort: 9090},
			want: [][]string{
				{"/usr/sbin/iptables", "-A", "OUTPUT", "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"},
				{"/usr/sbin/iptables", "-A", "OUTPUT", "-o", "lo", "-j", "ACCEPT"},
				{"/usr/sbin/iptables", "-A", "OUTPUT", "-d", "192.168.99.1", "-p", "tcp", "--dport", "9090", "-j", "ACCEPT"},
				{"/usr/sbin/iptables", "-P", "OUTPUT", "DROP"},
			},
		},
		{
			name:  "nft accepts each configured outbound forward, and nothing wider",
			tools: testTools,
			lockdown: Lockdown{Enabled: true, Gateway: "10.0.2.2", ProxyPort: 8080, Forwards: []Forward{
				{Port: 5432},
				{Port: 5353, UDP: true},
			}},
			want: [][]string{
				{"/usr/sbin/nft", "add", "table", "ip", "devsandbox_egress"},
				{"/usr/sbin/nft", "add", "chain", "ip", "devsandbox_egress", "output", "{ type filter hook output priority 0 ; policy drop ; }"},
				{"/usr/sbin/nft", "add", "rule", "ip", "devsandbox_egress", "output", "ct", "state", "established,related", "accept"},
				{"/usr/sbin/nft", "add", "rule", "ip", "devsandbox_egress", "output", "oif", "lo", "accept"},
				{"/usr/sbin/nft", "add", "rule", "ip", "devsandbox_egress", "output", "ip", "daddr", "10.0.2.2", "tcp", "dport", "8080", "accept"},
				{"/usr/sbin/nft", "add", "rule", "ip", "devsandbox_egress", "output", "ip", "daddr", "10.0.2.2", "tcp", "dport", "5432", "accept"},
				{"/usr/sbin/nft", "add", "rule", "ip", "devsandbox_egress", "output", "ip", "daddr", "10.0.2.2", "udp", "dport", "5353", "accept"},
			},
		},
		{
			// The forward accepts must land before the policy flips to DROP, or
			// iptables would drop the forwarded connection in the window between.
			name:  "iptables accepts each configured outbound forward before the drop policy",
			tools: testIptablesTools,
			lockdown: Lockdown{Enabled: true, Gateway: "10.0.2.2", ProxyPort: 8080, Forwards: []Forward{
				{Port: 5432},
				{Port: 5353, UDP: true},
			}},
			want: [][]string{
				{"/usr/sbin/iptables", "-A", "OUTPUT", "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"},
				{"/usr/sbin/iptables", "-A", "OUTPUT", "-o", "lo", "-j", "ACCEPT"},
				{"/usr/sbin/iptables", "-A", "OUTPUT", "-d", "10.0.2.2", "-p", "tcp", "--dport", "8080", "-j", "ACCEPT"},
				{"/usr/sbin/iptables", "-A", "OUTPUT", "-d", "10.0.2.2", "-p", "tcp", "--dport", "5432", "-j", "ACCEPT"},
				{"/usr/sbin/iptables", "-A", "OUTPUT", "-d", "10.0.2.2", "-p", "udp", "--dport", "5353", "-j", "ACCEPT"},
				{"/usr/sbin/iptables", "-P", "OUTPUT", "DROP"},
			},
		},
		{
			name:     "no backend fails closed",
			tools:    Tools{IP: "/usr/sbin/ip"},
			lockdown: Lockdown{Enabled: true, Gateway: "10.0.2.2", ProxyPort: 8080},
			wantErr:  true,
		},
		{
			name:     "nft forward port out of range fails closed",
			tools:    testTools,
			lockdown: Lockdown{Enabled: true, Gateway: "10.0.2.2", ProxyPort: 8080, Forwards: []Forward{{Port: 0}}},
			wantErr:  true,
		},
		{
			name:     "iptables forward port out of range fails closed",
			tools:    testIptablesTools,
			lockdown: Lockdown{Enabled: true, Gateway: "10.0.2.2", ProxyPort: 8080, Forwards: []Forward{{Port: 70000}}},
			wantErr:  true,
		},
		{
			name:     "backend without a resolved binary fails closed",
			tools:    Tools{IP: "/usr/sbin/ip", Backend: BackendNft},
			lockdown: Lockdown{Enabled: true, Gateway: "10.0.2.2", ProxyPort: 8080},
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FirewallCommands(tt.tools, tt.lockdown)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error for missing firewall backend, got nil (gateway would stay reachable on all ports)")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("FirewallCommands = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestFirewallCommands_DenyByDefault asserts the firewall denies by default
// rather than enumerating what to drop: the chain has a drop policy (nft
// `policy drop` / iptables `-P OUTPUT DROP`), and the only new-connection accept
// naming the gateway is scoped to the proxy TCP port - there is no blanket accept
// of the whole gateway that would re-open the host-loopback exposure. This is what
// makes the lockdown structural: a destination is reachable only if a rule names
// it, so the LAN, cloud metadata, and non-proxy gateway ports are all closed
// without being individually enumerated.
func TestFirewallCommands_DenyByDefault(t *testing.T) {
	// Configured forwards are part of this property, not an exception to it:
	// each one widens the ruleset by exactly one port of the gateway, so every
	// gateway accept must still name a port, and only a port that was asked for.
	lockdown := Lockdown{Enabled: true, Gateway: "10.0.2.2", ProxyPort: 8080, Forwards: []Forward{
		{Port: 5432},
		{Port: 5353, UDP: true},
	}}
	allowedPorts := map[string]bool{"8080": true, "5432": true, "5353": true}

	for _, tools := range []Tools{testTools, testIptablesTools} {
		backend := tools.Backend
		cmds, err := FirewallCommands(tools, lockdown)
		if err != nil {
			t.Fatalf("backend %v: unexpected error: %v", backend, err)
		}

		hasDefaultDrop := false
		hasPortAccept := false
		for _, c := range cmds {
			j := strings.Join(c, " ")
			// nft encodes the default drop in the chain policy; iptables in the
			// OUTPUT chain policy. Either establishes deny-by-default.
			if strings.Contains(j, "policy drop") || strings.Contains(j, "-P OUTPUT DROP") {
				hasDefaultDrop = true
			}
			// The gateway may only be accepted on a named port. A gateway accept
			// without the port would re-expose every host-loopback service.
			gatewayAccept := strings.Contains(j, "10.0.2.2") &&
				(strings.Contains(j, "accept") || strings.Contains(j, "ACCEPT"))
			if !gatewayAccept {
				continue
			}
			scoped := false
			for port := range allowedPorts {
				if strings.Contains(j, port) {
					scoped = true
					if port == "8080" {
						hasPortAccept = true
					}
				}
			}
			if !scoped {
				t.Errorf("backend %v: gateway accepted without a port scope: %v", backend, c)
			}
		}
		if !hasDefaultDrop {
			t.Errorf("backend %v: firewall must deny by default (no drop policy found): %v", backend, cmds)
		}
		if !hasPortAccept {
			t.Errorf("backend %v: no accept for the gateway on the proxy port: %v", backend, cmds)
		}
	}
}

// TestFirewallCommands_NoForwardsAddsNothing asserts an empty Forwards list
// produces exactly the base rule set. The lockdown is the same control whether
// or not port forwarding is configured; a forward may only ever add rules, never
// change the ones that are always there.
func TestFirewallCommands_NoForwardsAddsNothing(t *testing.T) {
	for _, tools := range []Tools{testTools, testIptablesTools} {
		base, err := FirewallCommands(tools, Lockdown{Enabled: true, Gateway: "10.0.2.2", ProxyPort: 8080})
		if err != nil {
			t.Fatalf("backend %v: unexpected error: %v", tools.Backend, err)
		}
		for _, name := range []string{"nil", "empty"} {
			l := Lockdown{Enabled: true, Gateway: "10.0.2.2", ProxyPort: 8080}
			if name == "empty" {
				l.Forwards = []Forward{}
			}
			got, err := FirewallCommands(tools, l)
			if err != nil {
				t.Fatalf("backend %v (%s forwards): unexpected error: %v", tools.Backend, name, err)
			}
			if !reflect.DeepEqual(got, base) {
				t.Errorf("backend %v (%s forwards): rule set changed without a configured forward:\ngot  %v\nwant %v",
					tools.Backend, name, got, base)
			}
		}
	}
}

// TestNoAutoForwardArgs asserts the lockdown turns off pasta's automatic
// namespace->init forwarding - the one direct path to the host's own loopback
// services that the firewall's mandatory `oif lo accept` cannot filter - while
// never overriding a forward the user explicitly configured.
func TestNoAutoForwardArgs(t *testing.T) {
	tests := []struct {
		name       string
		configured []string
		want       []string
	}{
		{name: "nothing configured", want: []string{"-T", "none", "-U", "none"}},
		{name: "tcp configured", configured: []string{"-T", "5000"}, want: []string{"-U", "none"}},
		{name: "udp configured", configured: []string{"-U", "5000"}, want: []string{"-T", "none"}},
		{name: "both configured", configured: []string{"-T", "5000", "-U", "5001"}, want: nil},
		{
			name:       "inbound forwarding is not namespace->init",
			configured: []string{"--tcp-ports", "3000:3000", "--udp-ports", "3001:3001"},
			want:       []string{"-T", "none", "-U", "none"},
		},
		{
			name:       "long spellings are recognised too",
			configured: []string{"--tcp-ns", "5000", "--udp-ns", "5001"},
			want:       nil,
		},
		{
			// getopt_long takes the argument attached as well as separated, and
			// pasta honours the last occurrence - so an unrecognised spelling
			// would have `none` appended after it and revoke the user's forward.
			name:       "attached long-form arguments are recognised",
			configured: []string{"--tcp-ns=5000", "--udp-ns=5001"},
			want:       nil,
		},
		{
			name:       "attached short-form arguments are recognised",
			configured: []string{"-T5000", "-U5001"},
			want:       nil,
		},
		{
			name:       "attached form pins only its own protocol",
			configured: []string{"-T5000"},
			want:       []string{"-U", "none"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NoAutoForwardArgs(tt.configured); !slices.Equal(got, tt.want) {
				t.Errorf("NoAutoForwardArgs(%v) = %v, want %v", tt.configured, got, tt.want)
			}
		})
	}
}

// TestFirewallCommands_ForwardsAreAdditive asserts a configured forward only
// appends: the base rules keep their content and their order, so a forward
// cannot weaken the established/related, loopback, or proxy-port rules, and on
// iptables cannot displace the trailing policy flip.
func TestFirewallCommands_ForwardsAreAdditive(t *testing.T) {
	for _, tools := range []Tools{testTools, testIptablesTools} {
		base, err := FirewallCommands(tools, Lockdown{Enabled: true, Gateway: "10.0.2.2", ProxyPort: 8080})
		if err != nil {
			t.Fatalf("backend %v: unexpected error: %v", tools.Backend, err)
		}
		got, err := FirewallCommands(tools, Lockdown{
			Enabled: true, Gateway: "10.0.2.2", ProxyPort: 8080,
			Forwards: []Forward{{Port: 5432}},
		})
		if err != nil {
			t.Fatalf("backend %v: unexpected error: %v", tools.Backend, err)
		}
		if len(got) != len(base)+1 {
			t.Fatalf("backend %v: one forward must add exactly one rule, got %d rules for %d base rules: %v",
				tools.Backend, len(got), len(base), got)
		}

		switch tools.Backend {
		case BackendIptables:
			// The policy flip must stay last.
			if !reflect.DeepEqual(got[:len(base)-1], base[:len(base)-1]) {
				t.Errorf("backend %v: base accepts changed: got %v, want prefix %v", tools.Backend, got, base)
			}
			if !reflect.DeepEqual(got[len(got)-1], base[len(base)-1]) {
				t.Errorf("backend %v: drop policy is not the last rule: %v", tools.Backend, got)
			}
		default:
			if !reflect.DeepEqual(got[:len(base)], base) {
				t.Errorf("backend %v: base rules changed: got %v, want prefix %v", tools.Backend, got, base)
			}
		}
	}
}

// TestScript_ForwardAccepts asserts the forward accepts reach the rendered
// wrapper prologue, after the route surgery and before the exec, so the bwrap
// backend gets the same forward handling krun does.
func TestScript_ForwardAccepts(t *testing.T) {
	script, err := Script(testTools, Lockdown{
		Enabled: true, Gateway: "10.0.2.2", ProxyPort: 8080, ReadyFile: testReadyFile,
		Forwards: []Forward{{Port: 5432}, {Port: 5353, UDP: true}},
	})
	if err != nil {
		t.Fatalf("Script: %v", err)
	}

	for _, want := range []string{
		`'daddr' '10.0.2.2' 'tcp' 'dport' '5432' 'accept'`,
		`'daddr' '10.0.2.2' 'udp' 'dport' '5353' 'accept'`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("rendered script is missing the forward accept %s:\n%s", want, script)
		}
	}
	if idx, execIdx := strings.Index(script, "'5432'"), strings.Index(script, `exec "$@"`); idx > execIdx {
		t.Errorf("forward accept rendered after the exec:\n%s", script)
	}
}

// TestScript_RejectsInvalidForward asserts a forward the firewall binary would
// refuse is caught while rendering, not mid-script inside the namespace where
// the only signal is an aborted launch.
func TestScript_RejectsInvalidForward(t *testing.T) {
	for _, port := range []int{0, -1, 70000} {
		_, err := Script(testTools, Lockdown{
			Enabled: true, Gateway: "10.0.2.2", ProxyPort: 8080, ReadyFile: testReadyFile,
			Forwards: []Forward{{Port: port}},
		})
		if err == nil {
			t.Errorf("forward port %d must be rejected rather than rendered", port)
		}
	}
}

// TestCommands_NoBareBinaryNames asserts every emitted command invokes an
// ABSOLUTE path and that no argv element is a bare binary name. A bare `ip` or
// `nft` resolves against a PATH neither the bwrap wrapper script nor nsenter
// inherits from this process, so on a host where iproute2/nftables live only in
// /usr/sbin the command would simply not run - a security control that silently
// does not apply.
func TestCommands_NoBareBinaryNames(t *testing.T) {
	bare := map[string]bool{"ip": true, "nft": true, "iptables": true}

	for _, tools := range []Tools{testTools, testIptablesTools} {
		cmds := RouteCommands(tools, "10.0.2.2", "enp5s0")
		fw, err := FirewallCommands(tools, Lockdown{Enabled: true, Gateway: "10.0.2.2", ProxyPort: 8080})
		if err != nil {
			t.Fatalf("backend %v: unexpected error: %v", tools.Backend, err)
		}
		// Only argv[0] is checked: nft's rules legitimately carry "ip" as the
		// address FAMILY keyword ("add table ip devsandbox_egress"), which is not
		// a binary name.
		for _, c := range append(cmds, fw...) {
			if bare[c[0]] || !strings.HasPrefix(c[0], "/") {
				t.Errorf("backend %v: command must invoke an absolute path, got %q in %v", tools.Backend, c[0], c)
			}
		}
	}
}

// TestFirewallCommands_NoDNSException asserts DNS is not excepted. Allowing :53
// to the gateway would re-open a DNS-tunnel exfiltration channel; the proxy
// resolves hostnames itself, so nothing in the sandbox needs direct DNS.
func TestFirewallCommands_NoDNSException(t *testing.T) {
	for _, tools := range []Tools{testTools, testIptablesTools} {
		cmds, err := FirewallCommands(tools, Lockdown{Enabled: true, Gateway: "10.0.2.2", ProxyPort: 8080})
		if err != nil {
			t.Fatalf("backend %v: unexpected error: %v", tools.Backend, err)
		}
		for _, c := range cmds {
			for _, a := range c {
				if a == "53" {
					t.Errorf("backend %v: DNS must not be excepted: %v", tools.Backend, c)
				}
			}
		}
	}
}

// TestScript_FailClosed asserts the rendered prologue never swallows a failure:
// no discarded stderr, an explicit abort on every mutation, and an exec that is
// reachable only when every step succeeded.
func TestScript_FailClosed(t *testing.T) {
	script, err := Script(testTools, Lockdown{Enabled: true, Gateway: "10.0.2.2", ProxyPort: 8080, ReadyFile: testReadyFile})
	if err != nil {
		t.Fatalf("Script: unexpected error: %v", err)
	}

	// sleepProbeLine is the one sanctioned stderr discard - its exit status is
	// the signal, not its message. Every other line must surface its diagnostic,
	// so the assertion runs against the script with that line removed rather
	// than being dropped.
	if !strings.Contains(script, sleepProbeLine) {
		t.Fatalf("sleep probe line missing; update this test if it moved:\n%s", script)
	}
	if strings.Contains(strings.Replace(script, sleepProbeLine, "", 1), "/dev/null") {
		t.Errorf("script must not discard errors:\n%s", script)
	}
	if !strings.HasPrefix(script, "set -e\n") {
		t.Errorf("script must start with set -e:\n%s", script)
	}
	if strings.Count(script, "exec ") != 1 {
		t.Errorf("expected exactly one exec, got %d:\n%s", strings.Count(script, "exec "), script)
	}

	lines := strings.Split(strings.TrimRight(script, "\n"), "\n")
	if last := lines[len(lines)-1]; last != `exec "$@"` {
		t.Errorf("exec must be the last line, got %q", last)
	}

	// Every namespace-mutating line aborts explicitly rather than relying on
	// set -e alone, so the failure carries the lockdown exit code the caller
	// maps back to a named error.
	// Every rendered command line (they start with the quoted absolute path of
	// the binary they invoke) aborts explicitly rather than relying on set -e
	// alone, so the failure carries the lockdown exit code the caller maps back
	// to a named error.
	mutating := 0
	for _, line := range lines {
		if !strings.HasPrefix(line, "'/") {
			continue
		}
		mutating++
		if !strings.Contains(line, "|| fail ") {
			t.Errorf("mutating line must abort on failure: %q", line)
		}
	}
	if mutating != 7 {
		t.Errorf("expected 7 mutating commands (2 route + 5 nft), got %d:\n%s", mutating, script)
	}
}

// TestScript_Ordering pins the security-relevant sequence: the gateway route is
// added before the default route is deleted (so the proxy stays reachable), and
// the firewall rules follow both.
func TestScript_Ordering(t *testing.T) {
	for _, tools := range []Tools{testTools, testIptablesTools} {
		script, err := Script(tools, Lockdown{Enabled: true, Gateway: "10.0.2.2", ProxyPort: 8080, ReadyFile: testReadyFile})
		if err != nil {
			t.Fatalf("backend %v: unexpected error: %v", tools.Backend, err)
		}

		// Every argv element is quoted individually, so the route commands are
		// matched in their rendered form rather than as prose.
		add := strings.Index(script, `'route' 'add'`)
		del := strings.Index(script, `'route' 'del'`)
		exec := strings.Index(script, `exec "$@"`)
		fw := strings.Index(script, shellQuote(tools.Firewall))
		if add < 0 || del < 0 || fw < 0 {
			t.Fatalf("backend %v: script missing expected commands:\n%s", tools.Backend, script)
		}
		if add > del {
			t.Errorf("backend %v: gateway route must be added before the default route is deleted:\n%s", tools.Backend, script)
		}
		if fw < del {
			t.Errorf("backend %v: firewall rules must follow the route surgery:\n%s", tools.Backend, script)
		}
		if exec < fw {
			t.Errorf("backend %v: exec must follow the firewall rules:\n%s", tools.Backend, script)
		}
	}
}

// TestScript_DeviceDiscovery asserts the device lookup retries rather than
// failing on the first empty read, and that the awk program exits after the
// first match so multiple default routes cannot yield a multi-line device name.
func TestScript_DeviceDiscovery(t *testing.T) {
	script, err := Script(testTools, Lockdown{Enabled: true, Gateway: "10.0.2.2", ProxyPort: 8080, ReadyFile: testReadyFile})
	if err != nil {
		t.Fatalf("Script: unexpected error: %v", err)
	}

	if !strings.Contains(script, "naps=50\n") || !strings.Contains(script, "while [ \"$i\" -lt \"$naps\" ]") {
		t.Errorf("device lookup must retry:\n%s", script)
	}
	if !strings.Contains(script, "naptime=0.1\n") || !strings.Contains(script, "sleep \"$naptime\"") {
		t.Errorf("retry loop must back off between attempts:\n%s", script)
	}
	// A `sleep` that only takes the integer operand POSIX requires must not turn
	// the back-off into an aborted launch, so the prologue tests the fractional
	// form once and drops to whole seconds over the same wall-clock budget.
	if !strings.Contains(script, sleepProbeLine) {
		t.Errorf("retry back-off must fall back to an integer sleep:\n%s", script)
	}
	if !strings.Contains(script, "exit}}") {
		t.Errorf("awk program must exit after the first dev match:\n%s", script)
	}
	if !strings.Contains(script, `'dev' "$dev"`) {
		t.Errorf("route add must use the discovered device variable, not a literal:\n%s", script)
	}
	if !strings.Contains(script, `if [ -z "$dev" ]; then fail `) {
		t.Errorf("an undiscoverable device must abort the script:\n%s", script)
	}
}

// TestScript_FailureDiagnostics asserts every abort is greppable and exits with
// the named lockdown code, which is what lets the caller distinguish a failed
// lockdown from a workload exit status.
func TestScript_FailureDiagnostics(t *testing.T) {
	script, err := Script(testTools, Lockdown{Enabled: true, Gateway: "10.0.2.2", ProxyPort: 8080, ReadyFile: testReadyFile})
	if err != nil {
		t.Fatalf("Script: unexpected error: %v", err)
	}

	if !strings.Contains(script, "devsandbox: egress lockdown:") {
		t.Errorf("script must emit a greppable stderr prefix:\n%s", script)
	}
	if !strings.Contains(script, ">&2") {
		t.Errorf("diagnostics must go to stderr:\n%s", script)
	}
	if !strings.Contains(script, "exit 78") {
		t.Errorf("script must abort with the lockdown exit code:\n%s", script)
	}
	if LockdownExitCode != 78 {
		t.Errorf("LockdownExitCode = %d, want 78", LockdownExitCode)
	}
}

// TestScript_RulesSurviveQuoting asserts the nft chain specification - which
// contains spaces, braces and semicolons - reaches the shell as a single word.
// Unquoted, `{ type filter hook output priority 0 ; policy drop ; }` would split
// into a dozen words and the DROP policy would never be installed.
func TestScript_RulesSurviveQuoting(t *testing.T) {
	script, err := Script(testTools, Lockdown{Enabled: true, Gateway: "10.0.2.2", ProxyPort: 8080, ReadyFile: testReadyFile})
	if err != nil {
		t.Fatalf("Script: unexpected error: %v", err)
	}

	want := `'{ type filter hook output priority 0 ; policy drop ; }'`
	if !strings.Contains(script, want) {
		t.Errorf("nft policy-drop argument must survive quoting as one word, want %s in:\n%s", want, script)
	}
}

// TestScript_MarkerFollowsEveryRule pins where the marker is written: after the
// last rule and before the exec. Written earlier it would claim a lockdown that
// had not finished applying; written by the workload's own side of the exec it
// could not be written at all. Its position IS its meaning.
func TestScript_MarkerFollowsEveryRule(t *testing.T) {
	script, err := Script(testTools, Lockdown{Enabled: true, Gateway: "10.0.2.2", ProxyPort: 8080, ReadyFile: testReadyFile})
	if err != nil {
		t.Fatalf("Script: unexpected error: %v", err)
	}

	marker := strings.Index(script, ": > "+shellQuote(testReadyFile))
	if marker < 0 {
		t.Fatalf("rendered script writes no marker file:\n%s", script)
	}
	if !strings.Contains(script[marker:], "|| fail ") {
		t.Errorf("a marker that cannot be written must abort the launch, not be skipped:\n%s", script)
	}
	if last := strings.LastIndex(script, shellQuote(testTools.Firewall)); last > marker {
		t.Errorf("marker written before the last firewall rule:\n%s", script)
	}
	if last := strings.LastIndex(script, `'route' 'del'`); last > marker {
		t.Errorf("marker written before the route surgery:\n%s", script)
	}
	if execIdx := strings.Index(script, `exec "$@"`); execIdx < marker {
		t.Errorf("marker written after the exec, where the prologue no longer runs:\n%s", script)
	}
}

// TestLockdownApplied covers the reader side of the marker, including the empty
// path: a caller with no marker path has no signal, and the fail-closed reading
// of a missing signal is "the lockdown did not apply".
func TestLockdownApplied(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "applied")
	if err := os.WriteFile(present, nil, 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	if !LockdownApplied(present) {
		t.Errorf("LockdownApplied(%q) = false for an existing marker", present)
	}
	if LockdownApplied(filepath.Join(dir, "absent")) {
		t.Error("LockdownApplied() = true for a marker that was never written")
	}
	if LockdownApplied("") {
		t.Error("LockdownApplied(\"\") = true; a missing signal must read as not applied")
	}
}

// TestScript_Rejects asserts a lockdown that cannot produce a meaningful rule
// set is refused at render time rather than rendered into a script that fails
// mid-way with an opaque error - or, worse, installs a rule permitting nothing.
func TestScript_Rejects(t *testing.T) {
	tests := []struct {
		name     string
		tools    Tools
		lockdown Lockdown
		wantErr  error
	}{
		{
			name:     "zero proxy port",
			tools:    testTools,
			lockdown: Lockdown{Enabled: true, Gateway: "10.0.2.2", ProxyPort: 0, ReadyFile: testReadyFile},
		},
		{
			name:     "negative proxy port",
			tools:    testTools,
			lockdown: Lockdown{Enabled: true, Gateway: "10.0.2.2", ProxyPort: -1, ReadyFile: testReadyFile},
		},
		{
			name:     "out of range proxy port",
			tools:    testTools,
			lockdown: Lockdown{Enabled: true, Gateway: "10.0.2.2", ProxyPort: 65536, ReadyFile: testReadyFile},
		},
		{
			name:     "empty gateway",
			tools:    testTools,
			lockdown: Lockdown{Enabled: true, Gateway: "", ProxyPort: 8080, ReadyFile: testReadyFile},
		},
		{
			name:     "disabled lockdown",
			tools:    testTools,
			lockdown: Lockdown{Enabled: false, Gateway: "10.0.2.2", ProxyPort: 8080, ReadyFile: testReadyFile},
		},
		{
			name:     "unresolved ip binary",
			tools:    Tools{Firewall: "/usr/sbin/nft", Backend: BackendNft},
			lockdown: Lockdown{Enabled: true, Gateway: "10.0.2.2", ProxyPort: 8080, ReadyFile: testReadyFile},
			wantErr:  ErrNoIPBinary,
		},
		{
			name:     "no firewall backend",
			tools:    Tools{IP: "/usr/sbin/ip", Backend: BackendNone},
			lockdown: Lockdown{Enabled: true, Gateway: "10.0.2.2", ProxyPort: 8080, ReadyFile: testReadyFile},
			wantErr:  ErrNoFirewallBackend,
		},
		{
			// Without a marker path the script's caller has only the exit status
			// to go on, and 78 is then indistinguishable from a workload that
			// chose it - the ambiguity the marker exists to remove. Rendering
			// anyway would reintroduce it silently.
			name:     "no marker path",
			tools:    testTools,
			lockdown: Lockdown{Enabled: true, Gateway: "10.0.2.2", ProxyPort: 8080},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			script, err := Script(tt.tools, tt.lockdown)
			if err == nil {
				t.Fatalf("expected an error, got script:\n%s", script)
			}
			if script != "" {
				t.Errorf("expected no script on error, got:\n%s", script)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "safe word is still quoted", in: "accept", want: `'accept'`},
		{name: "empty string", in: "", want: `''`},
		{name: "spaces", in: "ct state established,related", want: `'ct state established,related'`},
		{name: "semicolons and braces", in: "{ policy drop ; }", want: `'{ policy drop ; }'`},
		{name: "embedded single quote", in: "it's", want: `'it'\''s'`},
		{name: "only a single quote", in: "'", want: `''\'''`},
		{name: "command substitution is inert", in: "$(id)", want: `'$(id)'`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shellQuote(tt.in); got != tt.want {
				t.Errorf("shellQuote(%q) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

// writeFakeBin writes an executable stub and returns its absolute path.
func writeFakeBin(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// TestScript_ShellSyntax asserts the rendered prologue is valid POSIX shell.
// A quoting bug that produced a syntax error would abort every proxy launch.
func TestScript_ShellSyntax(t *testing.T) {
	for _, tools := range []Tools{testTools, testIptablesTools} {
		script, err := Script(tools, Lockdown{Enabled: true, Gateway: "10.0.2.2", ProxyPort: 8080, ReadyFile: testReadyFile})
		if err != nil {
			t.Fatalf("backend %v: unexpected error: %v", tools.Backend, err)
		}
		cmd := exec.Command("sh", "-n", "-c", script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("backend %v: rendered script is not valid shell: %v\n%s\n%s", tools.Backend, err, out, script)
		}
	}
}

// TestScript_Executes runs the rendered prologue against stub binaries. It is
// the end-to-end check that the quoting survives a real shell: the nft chain
// specification must arrive as ONE argument, the discovered device must be
// substituted, and exec must run the trailing command.
func TestScript_Executes(t *testing.T) {
	dir := t.TempDir()
	argLog := filepath.Join(dir, "args")
	ip := writeFakeBin(t, dir, "ip", `
if [ "$1" = "-o" ]; then echo "default via 10.0.2.2 dev tap0 proto static"; exit 0; fi
printf '%s\n' "ip|$*" >> `+argLog+`
`)
	nft := writeFakeBin(t, dir, "nft", `
printf '%s\n' "nft|$#|$3" >> `+argLog+`
`)

	ready := filepath.Join(dir, "applied")
	script, err := Script(Tools{IP: ip, Firewall: nft, Backend: BackendNft},
		Lockdown{Enabled: true, Gateway: "10.0.2.2", ProxyPort: 8080, ReadyFile: ready})
	if err != nil {
		t.Fatalf("Script: unexpected error: %v", err)
	}

	out, err := exec.Command("sh", "-c", script, "_", "echo", "workload-ran").CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\n%s\n%s", err, out, script)
	}
	if !strings.Contains(string(out), "workload-ran") {
		t.Errorf("script did not exec the trailing command, output: %q", out)
	}

	logged, err := os.ReadFile(argLog)
	if err != nil {
		t.Fatalf("read arg log: %v", err)
	}
	got := string(logged)
	if !strings.Contains(got, "ip|route add 10.0.2.2/32 dev tap0") {
		t.Errorf("device discovered from `ip route` must be substituted, got:\n%s", got)
	}
	if !strings.Contains(got, "ip|route del default") {
		t.Errorf("default route must be deleted, got:\n%s", got)
	}
	// `add chain ip devsandbox_egress output { ... }` is 6 arguments only if the
	// brace expression stays one word; unquoted it would split into far more.
	if !strings.Contains(got, "nft|6|ip") {
		t.Errorf("nft chain spec must reach the binary as a single argument, got:\n%s", got)
	}
	if _, err := os.Stat(ready); err != nil {
		t.Errorf("marker file missing after a successful prologue: %v; without it the caller cannot tell a workload that exits %d from an aborted lockdown", err, LockdownExitCode)
	}
}

// TestScript_AbortsBeforeExec asserts a failing lockdown command stops the
// script with the named exit code and a greppable diagnostic, never reaching the
// workload. This is the property that makes the bwrap lockdown fail-closed.
func TestScript_AbortsBeforeExec(t *testing.T) {
	dir := t.TempDir()
	ip := writeFakeBin(t, dir, "ip", `
if [ "$1" = "-o" ]; then echo "default via 10.0.2.2 dev tap0"; exit 0; fi
exit 0
`)
	nft := writeFakeBin(t, dir, "nft", "exit 1\n")

	ready := filepath.Join(dir, "applied")
	script, err := Script(Tools{IP: ip, Firewall: nft, Backend: BackendNft},
		Lockdown{Enabled: true, Gateway: "10.0.2.2", ProxyPort: 8080, ReadyFile: ready})
	if err != nil {
		t.Fatalf("Script: unexpected error: %v", err)
	}

	out, err := exec.Command("sh", "-c", script, "_", "echo", "workload-ran").CombinedOutput()
	if err == nil {
		t.Fatalf("expected the script to abort, got success:\n%s", out)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != LockdownExitCode {
		t.Errorf("expected exit %d, got %v", LockdownExitCode, err)
	}
	if strings.Contains(string(out), "workload-ran") {
		t.Errorf("workload must not run after a failed lockdown, output: %q", out)
	}
	if !strings.Contains(string(out), "devsandbox: egress lockdown:") {
		t.Errorf("abort must be diagnosable, output: %q", out)
	}
	// The marker is what tells the caller this 78 is an abort and not the
	// workload's own status. A marker left behind by a failed lockdown would
	// make the abort read as a normal command exit.
	if _, err := os.Stat(ready); err == nil {
		t.Errorf("marker file exists after an aborted lockdown; the abort would be reported as a workload exit %d", LockdownExitCode)
	}
	if LockdownApplied(ready) {
		t.Errorf("LockdownApplied(%q) must be false after an abort", ready)
	}
}

// TestScript_AbortsWithoutDefaultRoute asserts an undiscoverable device aborts
// rather than silently launching with egress open - the original bug.
func TestScript_AbortsWithoutDefaultRoute(t *testing.T) {
	dir := t.TempDir()
	ip := writeFakeBin(t, dir, "ip", "exit 0\n") // no default route ever appears
	nft := writeFakeBin(t, dir, "nft", "exit 0\n")

	ready := filepath.Join(dir, "applied")
	script, err := Script(Tools{IP: ip, Firewall: nft, Backend: BackendNft},
		Lockdown{Enabled: true, Gateway: "10.0.2.2", ProxyPort: 8080, ReadyFile: ready})
	if err != nil {
		t.Fatalf("Script: unexpected error: %v", err)
	}
	// Keep the test fast: the production retry budget is 5s, which is fine for a
	// launch but not for a unit test, so shorten the sleep in the rendered loop.
	script = strings.ReplaceAll(script, "sleep \"$naptime\"\n", "true\n")

	out, err := exec.Command("sh", "-c", script, "_", "echo", "workload-ran").CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != LockdownExitCode {
		t.Fatalf("expected exit %d, got %v (%s)", LockdownExitCode, err, out)
	}
	if strings.Contains(string(out), "workload-ran") {
		t.Errorf("workload must not run when the device is undiscoverable, output: %q", out)
	}
	if !strings.Contains(string(out), "no default route device") {
		t.Errorf("abort must name the missing device, output: %q", out)
	}
}

// TestScript_AbortAlwaysCarriesTheLockdownExitCode asserts EVERY pre-exec
// failure leaves with LockdownExitCode, not just the ones guarded by `|| fail`.
// The device lookup runs `awk` and `sleep` through PATH, and `set -e` on its own
// exits with the failing command's status - 127 for a shell that cannot find
// them. The caller maps only LockdownExitCode back to a named lockdown error, so
// any other status is reported as if the sandboxed program had exited with it:
// an unapplied security control disguised as a workload result.
func TestScript_AbortAlwaysCarriesTheLockdownExitCode(t *testing.T) {
	dir := t.TempDir()
	ip := writeFakeBin(t, dir, "ip", `
if [ "$1" = "-o" ]; then echo "default via 10.0.2.2 dev tap0"; exit 0; fi
exit 0
`)
	nft := writeFakeBin(t, dir, "nft", "exit 0\n")

	ready := filepath.Join(dir, "applied")
	script, err := Script(Tools{IP: ip, Firewall: nft, Backend: BackendNft},
		Lockdown{Enabled: true, Gateway: "10.0.2.2", ProxyPort: 8080, ReadyFile: ready})
	if err != nil {
		t.Fatalf("Script: unexpected error: %v", err)
	}

	// An empty PATH is how a wrapper launched from a login or multiplexer shell
	// can look; it makes the bare `awk` in the device lookup unresolvable.
	cmd := exec.Command("sh", "-c", script, "_", "echo", "workload-ran")
	cmd.Env = append(os.Environ(), "PATH=/devsandbox-nonexistent")
	out, err := cmd.CombinedOutput()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected the script to abort, got %v (%s)", err, out)
	}
	if exitErr.ExitCode() != LockdownExitCode {
		t.Errorf("exit code = %d, want %d; any other status is read as the workload's own (%s)",
			exitErr.ExitCode(), LockdownExitCode, out)
	}
	if strings.Contains(string(out), "workload-ran") {
		t.Errorf("workload must not run after an aborted lockdown, output: %q", out)
	}
	if !strings.Contains(string(out), "devsandbox: egress lockdown:") {
		t.Errorf("abort must be diagnosable, output: %q", out)
	}
}

// TestScript_ExecKeepsTheWorkloadStatus asserts the abort trap is cleared before
// the exec: the sandboxed program's own exit status must reach the caller
// untouched, or every workload failure would be reported as a lockdown abort.
func TestScript_ExecKeepsTheWorkloadStatus(t *testing.T) {
	dir := t.TempDir()
	ip := writeFakeBin(t, dir, "ip", `
if [ "$1" = "-o" ]; then echo "default via 10.0.2.2 dev tap0"; exit 0; fi
exit 0
`)
	nft := writeFakeBin(t, dir, "nft", "exit 0\n")

	ready := filepath.Join(dir, "applied")
	script, err := Script(Tools{IP: ip, Firewall: nft, Backend: BackendNft},
		Lockdown{Enabled: true, Gateway: "10.0.2.2", ProxyPort: 8080, ReadyFile: ready})
	if err != nil {
		t.Fatalf("Script: unexpected error: %v", err)
	}

	out, err := exec.Command("sh", "-c", script, "_", "sh", "-c", "exit 3").CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected the workload's status, got %v (%s)", err, out)
	}
	if exitErr.ExitCode() != 3 {
		t.Errorf("exit code = %d, want the workload's own 3 (%s)", exitErr.ExitCode(), out)
	}
	if strings.Contains(string(out), "devsandbox: egress lockdown:") {
		t.Errorf("a workload exit must not be reported as a lockdown abort, output: %q", out)
	}
}

// TestScript_FailureDiagnosticNamesTheDiscoveredDevice asserts the one command
// that depends on runtime discovery names the device it actually used, not the
// internal placeholder. That message is what the docs and ErrEgressLockdown
// point the user at, so a sentinel there is a dead end.
func TestScript_FailureDiagnosticNamesTheDiscoveredDevice(t *testing.T) {
	dir := t.TempDir()
	ip := writeFakeBin(t, dir, "ip", `
if [ "$1" = "-o" ]; then echo "default via 10.0.2.2 dev tap0"; exit 0; fi
exit 1
`)
	nft := writeFakeBin(t, dir, "nft", "exit 0\n")

	ready := filepath.Join(dir, "applied")
	script, err := Script(Tools{IP: ip, Firewall: nft, Backend: BackendNft},
		Lockdown{Enabled: true, Gateway: "10.0.2.2", ProxyPort: 8080, ReadyFile: ready})
	if err != nil {
		t.Fatalf("Script: unexpected error: %v", err)
	}
	if strings.Contains(script, scriptDevPlaceholder) {
		t.Errorf("rendered script leaks the device placeholder:\n%s", script)
	}

	out, _ := exec.Command("sh", "-c", script, "_", "echo", "workload-ran").CombinedOutput()
	if !strings.Contains(string(out), "dev tap0") {
		t.Errorf("abort message must name the discovered device, output: %q", out)
	}
}
