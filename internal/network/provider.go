package network

import "errors"

var ErrNoNetworkProvider = errors.New("no network provider available (need pasta or slirp4netns)")

// Provider defines the interface for user-mode network providers
type Provider interface {
	// Name returns the provider name
	Name() string

	// Available checks if the provider is installed and usable
	Available() bool

	// Start launches the network provider for the given namespace
	// nsPath is the path to the network namespace (e.g., /proc/PID/ns/net)
	Start(nsPath string) error

	// Stop terminates the network provider
	Stop() error

	// GatewayIP returns the gateway IP address accessible from the namespace
	GatewayIP() string

	// Running returns true if the provider is currently running
	Running() bool
}

// SelectProvider returns the best available network provider
// Prefers pasta over slirp4netns
func SelectProvider() (Provider, error) {
	pasta := NewPasta()
	if pasta.Available() {
		return pasta, nil
	}

	slirp := NewSlirp()
	if slirp.Available() {
		return slirp, nil
	}

	return nil, ErrNoNetworkProvider
}

// CheckAnyProviderAvailable returns true if any network provider is available
func CheckAnyProviderAvailable() bool {
	pasta := NewPasta()
	if pasta.Available() {
		return true
	}

	slirp := NewSlirp()
	return slirp.Available()
}
