package network

import (
	"testing"
)

func TestPastaAvailable(t *testing.T) {
	p := NewPasta()

	// Just test that it doesn't panic
	available := p.Available()
	t.Logf("pasta available: %v", available)
}

func TestSlirpAvailable(t *testing.T) {
	s := NewSlirp()

	// Just test that it doesn't panic
	available := s.Available()
	t.Logf("slirp4netns available: %v", available)
}

func TestSelectProvider(t *testing.T) {
	provider, err := SelectProvider()

	if err == ErrNoNetworkProvider {
		t.Skip("No network provider available")
	}

	if err != nil {
		t.Fatalf("SelectProvider failed: %v", err)
	}

	if provider == nil {
		t.Fatal("provider is nil")
	}

	t.Logf("Selected provider: %s", provider.Name())
}

func TestCheckAnyProviderAvailable(t *testing.T) {
	available := CheckAnyProviderAvailable()
	t.Logf("Any provider available: %v", available)
}

func TestPastaGatewayIP(t *testing.T) {
	p := NewPasta()
	ip := p.GatewayIP()

	if ip != "10.0.2.2" {
		t.Errorf("unexpected gateway IP: %s", ip)
	}
}

func TestSlirpGatewayIP(t *testing.T) {
	s := NewSlirp()
	ip := s.GatewayIP()

	if ip != "10.0.2.2" {
		t.Errorf("unexpected gateway IP: %s", ip)
	}
}

func TestPastaName(t *testing.T) {
	p := NewPasta()
	if p.Name() != "pasta" {
		t.Errorf("unexpected name: %s", p.Name())
	}
}

func TestSlirpName(t *testing.T) {
	s := NewSlirp()
	if s.Name() != "slirp4netns" {
		t.Errorf("unexpected name: %s", s.Name())
	}
}

func TestPastaNotRunningByDefault(t *testing.T) {
	p := NewPasta()
	if p.Running() {
		t.Error("pasta should not be running by default")
	}
}

func TestSlirpNotRunningByDefault(t *testing.T) {
	s := NewSlirp()
	if s.Running() {
		t.Error("slirp4netns should not be running by default")
	}
}

func TestPastaStopWhenNotRunning(t *testing.T) {
	p := NewPasta()
	// Should not error when stopping something that isn't running
	if err := p.Stop(); err != nil {
		t.Errorf("Stop failed: %v", err)
	}
}

func TestSlirpStopWhenNotRunning(t *testing.T) {
	s := NewSlirp()
	// Should not error when stopping something that isn't running
	if err := s.Stop(); err != nil {
		t.Errorf("Stop failed: %v", err)
	}
}
