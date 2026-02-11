package network

import "devsandbox/internal/embed"

const (
	// PastaGatewayIP is the default gateway IP for pasta network isolation.
	// This IP is mapped to the host's 127.0.0.1 via --map-host-loopback.
	PastaGatewayIP = "10.0.2.2"
)

// Pasta implements the Provider interface using pasta (from passt package).
// Pasta provides user-mode networking for unprivileged network namespaces.
// When used with bwrap, it creates an isolated network where all traffic
// must go through the gateway IP (10.0.2.2), which maps to the host's loopback.
type Pasta struct{}

// NewPasta creates a new pasta provider
func NewPasta() *Pasta {
	return &Pasta{}
}

// Name returns the provider name
func (p *Pasta) Name() string {
	return "pasta"
}

// Available checks if pasta is available (embedded or system-installed)
func (p *Pasta) Available() bool {
	_, err := embed.PastaPath()
	return err == nil
}

// GatewayIP returns the gateway IP for pasta.
// This IP (10.0.2.2) is mapped to the host's 127.0.0.1 via --map-host-loopback.
func (p *Pasta) GatewayIP() string {
	return PastaGatewayIP
}

// NetworkIsolated returns true as pasta provides full network namespace isolation.
// All traffic from the sandbox must go through pasta's virtual network interface.
func (p *Pasta) NetworkIsolated() bool {
	return true
}
