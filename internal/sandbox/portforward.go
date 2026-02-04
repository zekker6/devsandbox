package sandbox

import (
	"fmt"

	"devsandbox/internal/config"
)

// BuildPastaPortArgs converts port forwarding rules to pasta command-line arguments.
//
// For inbound (host->sandbox):
//   - TCP: --tcp-ports host:sandbox
//   - UDP: --udp-ports host:sandbox
//
// For outbound (sandbox->host):
//   - TCP: -T port (sandbox connects to gateway IP to reach host)
//   - UDP: -U port
func BuildPastaPortArgs(rules []config.PortForwardingRule) []string {
	if len(rules) == 0 {
		return nil
	}

	var args []string
	for _, rule := range rules {
		switch rule.Direction {
		case "inbound":
			portSpec := fmt.Sprintf("%d:%d", rule.HostPort, rule.SandboxPort)
			if rule.Protocol == "udp" {
				args = append(args, "--udp-ports", portSpec)
			} else {
				args = append(args, "--tcp-ports", portSpec)
			}
		case "outbound":
			port := fmt.Sprintf("%d", rule.HostPort)
			if rule.Protocol == "udp" {
				args = append(args, "-U", port)
			} else {
				args = append(args, "-T", port)
			}
		}
	}
	return args
}
