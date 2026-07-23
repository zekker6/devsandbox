//go:build linux

package egress

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// Probe reports whether this host can apply the lockdown, by applying the FULL
// rendered firewall rule set inside a throwaway user + network namespace.
//
// Applying the whole rule set is the point. A cheaper check - resolving the
// binary, or reading /sys/module/nf_tables - passes on a host that has nf_tables
// but not nf_conntrack, and the rules match on `ct state established,related`.
// That host would then fail mid-lockdown: for bwrap inside the wrapper script,
// where the only symptom is an exit code, and for krun after the guest has
// already booted.
//
// The namespace mirrors where the lockdown really runs. Both backends apply the
// rules as root in a rootless user namespace (pasta's), where module autoload is
// not permitted - so a probe that ran with the caller's own privileges could
// succeed on a host where the launch cannot. The namespace is torn down when the
// probe process exits, and nothing outside it is touched.
func Probe(t Tools, l Lockdown) error {
	script, err := probeScript(t, l)
	if err != nil {
		return &ProbeError{Stage: ProbeStageTools, Err: err}
	}

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	// WaitDelay is what makes probeTimeout a real bound. Stderr is a buffer, so
	// os/exec pipes it and Wait blocks until every writer closes the pipe -
	// while the context cancellation kills only /bin/sh, not an nft/iptables
	// grandchild wedged on netlink that still holds the inherited fd. Without
	// this, the deadline would fire and Wait would block forever, hanging both
	// `doctor` and the launch preflight on exactly the wedged binary the
	// timeout exists to bound.
	cmd.WaitDelay = probeWaitDelay
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
		// setgroups must stay denied in a rootless user namespace; the kernel
		// refuses the gid map otherwise.
		GidMappingsEnableSetgroups: false,
	}

	// A namespace that cannot be created is reported by Start (the clone happens
	// before the exec), which is what separates "unprivileged user namespaces are
	// unavailable" from "the rules were refused".
	if err := cmd.Start(); err != nil {
		return &ProbeError{Stage: ProbeStageNamespace, Err: err}
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return &ProbeError{Stage: ProbeStageRules, Err: fmt.Errorf("timed out after %s", probeTimeout)}
		}
		return &ProbeError{Stage: ProbeStageRules, Detail: firstLine(stderr.String()), Err: err}
	}
	return nil
}

// firstLine returns the first non-empty line of s. nft prints its diagnostic
// first and the offending rule after it, and only the diagnostic fits a doctor
// table cell.
func firstLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}
