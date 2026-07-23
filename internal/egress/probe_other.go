//go:build !linux

package egress

import "errors"

// Probe is unavailable off Linux: the check applies the rule set inside a
// throwaway user + network namespace, and neither the namespaces nor
// nft/iptables exist elsewhere. It returns an error rather than reporting
// success, so a caller can never read "no error" as "the lockdown is
// enforceable here". No backend applies this lockdown off Linux - bwrap does not
// run there and krun proxy mode is refused - so nothing legitimately calls this.
func Probe(t Tools, l Lockdown) error {
	return &ProbeError{
		Stage: ProbeStageNamespace,
		Err:   errors.New("the egress lockdown probe is only supported on Linux"),
	}
}
