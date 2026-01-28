package network

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

const (
	pastaCommand   = "pasta"
	pastaGatewayIP = "10.0.2.2" // Default gateway for pasta
)

// Pasta implements the Provider interface using pasta (from passt package)
type Pasta struct {
	cmd     *exec.Cmd
	mu      sync.Mutex
	running bool
}

// NewPasta creates a new pasta provider
func NewPasta() *Pasta {
	return &Pasta{}
}

// Name returns the provider name
func (p *Pasta) Name() string {
	return "pasta"
}

// Available checks if pasta is installed
func (p *Pasta) Available() bool {
	_, err := exec.LookPath(pastaCommand)
	return err == nil
}

// Start launches pasta for the given network namespace
func (p *Pasta) Start(nsPath string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.running {
		return fmt.Errorf("pasta already running")
	}

	// pasta arguments:
	// --netns PATH: Use existing network namespace
	// --map-host-loopback: Allow access to host's loopback
	// -f: Run in foreground
	args := []string{
		"--netns", nsPath,
		"--map-host-loopback",
		"-f",
	}

	p.cmd = exec.Command(pastaCommand, args...)
	p.cmd.Stdout = os.Stdout
	p.cmd.Stderr = os.Stderr

	// Set process group so we can kill the whole group
	p.cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	if err := p.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start pasta: %w", err)
	}

	p.running = true
	return nil
}

// Stop terminates pasta
func (p *Pasta) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.running || p.cmd == nil || p.cmd.Process == nil {
		return nil
	}

	// Kill the process group
	pgid, err := syscall.Getpgid(p.cmd.Process.Pid)
	if err == nil {
		syscall.Kill(-pgid, syscall.SIGTERM)
	} else {
		p.cmd.Process.Kill()
	}

	p.cmd.Wait()
	p.running = false
	return nil
}

// GatewayIP returns the gateway IP for pasta
func (p *Pasta) GatewayIP() string {
	return pastaGatewayIP
}

// Running returns true if pasta is running
func (p *Pasta) Running() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}
