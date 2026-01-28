package network

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

const (
	slirpCommand   = "slirp4netns"
	slirpGatewayIP = "10.0.2.2" // Default gateway for slirp4netns
)

// Slirp implements the Provider interface using slirp4netns
type Slirp struct {
	cmd     *exec.Cmd
	mu      sync.Mutex
	running bool
}

// NewSlirp creates a new slirp4netns provider
func NewSlirp() *Slirp {
	return &Slirp{}
}

// Name returns the provider name
func (s *Slirp) Name() string {
	return "slirp4netns"
}

// Available checks if slirp4netns is installed
func (s *Slirp) Available() bool {
	_, err := exec.LookPath(slirpCommand)
	return err == nil
}

// Start launches slirp4netns for the given network namespace
func (s *Slirp) Start(nsPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("slirp4netns already running")
	}

	// slirp4netns arguments:
	// --configure: Configure network in namespace
	// --mtu=65520: MTU for tap device
	// --disable-host-loopback: Security measure (we use --map-host-loopback via API)
	// PID tap0: PID of namespace and tap device name
	//
	// Note: slirp4netns needs the PID, not namespace path
	// We extract PID from the nsPath (/proc/PID/ns/net)
	args := []string{
		"--configure",
		"--mtu=65520",
		"--disable-host-loopback",
		nsPath, // slirp4netns can take namespace path directly with newer versions
		"tap0",
	}

	s.cmd = exec.Command(slirpCommand, args...)
	s.cmd.Stdout = os.Stdout
	s.cmd.Stderr = os.Stderr

	// Set process group so we can kill the whole group
	s.cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	if err := s.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start slirp4netns: %w", err)
	}

	s.running = true
	return nil
}

// Stop terminates slirp4netns
func (s *Slirp) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running || s.cmd == nil || s.cmd.Process == nil {
		return nil
	}

	// Kill the process group
	pgid, err := syscall.Getpgid(s.cmd.Process.Pid)
	if err == nil {
		syscall.Kill(-pgid, syscall.SIGTERM)
	} else {
		s.cmd.Process.Kill()
	}

	s.cmd.Wait()
	s.running = false
	return nil
}

// GatewayIP returns the gateway IP for slirp4netns
func (s *Slirp) GatewayIP() string {
	return slirpGatewayIP
}

// Running returns true if slirp4netns is running
func (s *Slirp) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}
