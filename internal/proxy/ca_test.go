package proxy

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestCreateCA(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ca-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := NewConfig(tmpDir, DefaultProxyPort)

	ca, err := CreateCA(cfg)
	if err != nil {
		t.Fatalf("CreateCA failed: %v", err)
	}

	if ca.Certificate == nil {
		t.Error("CA certificate is nil")
	}

	if ca.PrivateKey == nil {
		t.Error("CA private key is nil")
	}

	if !ca.Certificate.IsCA {
		t.Error("Certificate should be a CA")
	}

	if ca.Certificate.Subject.CommonName != "DevSandbox Proxy CA" {
		t.Errorf("unexpected CN: %s", ca.Certificate.Subject.CommonName)
	}

	// Verify files were created
	if _, err := os.Stat(cfg.CACertPath); os.IsNotExist(err) {
		t.Error("CA certificate file not created")
	}

	if _, err := os.Stat(cfg.CAKeyPath); os.IsNotExist(err) {
		t.Error("CA key file not created")
	}

	// Verify key file permissions
	info, err := os.Stat(cfg.CAKeyPath)
	if err != nil {
		t.Fatalf("failed to stat key file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key file has wrong permissions: %o", info.Mode().Perm())
	}
}

func TestLoadCA(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ca-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := NewConfig(tmpDir, DefaultProxyPort)

	// Create CA first
	originalCA, err := CreateCA(cfg)
	if err != nil {
		t.Fatalf("CreateCA failed: %v", err)
	}

	// Load it back
	loadedCA, err := LoadCA(cfg)
	if err != nil {
		t.Fatalf("LoadCA failed: %v", err)
	}

	// Compare
	if loadedCA.Certificate.SerialNumber.Cmp(originalCA.Certificate.SerialNumber) != 0 {
		t.Error("Serial numbers don't match")
	}

	if loadedCA.Certificate.Subject.CommonName != originalCA.Certificate.Subject.CommonName {
		t.Error("Common names don't match")
	}
}

func TestLoadOrCreateCA_Creates(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ca-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := NewConfig(tmpDir, DefaultProxyPort)

	ca, err := LoadOrCreateCA(cfg)
	if err != nil {
		t.Fatalf("LoadOrCreateCA failed: %v", err)
	}

	if ca.Certificate == nil {
		t.Error("CA certificate is nil")
	}
}

func TestLoadOrCreateCA_Loads(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ca-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := NewConfig(tmpDir, DefaultProxyPort)

	// Create first
	ca1, err := LoadOrCreateCA(cfg)
	if err != nil {
		t.Fatalf("first LoadOrCreateCA failed: %v", err)
	}

	// Load second time - should get same CA
	ca2, err := LoadOrCreateCA(cfg)
	if err != nil {
		t.Fatalf("second LoadOrCreateCA failed: %v", err)
	}

	if ca1.Certificate.SerialNumber.Cmp(ca2.Certificate.SerialNumber) != 0 {
		t.Error("Should have loaded same CA, but got different serial numbers")
	}
}

// Concurrent launches of one project share a sandbox root and so one CA file.
// A launch whose proxy signs with a CA other than the one on disk fails every
// TLS handshake, because its sandbox trusts the file, not the proxy's memory.
func TestLoadOrCreateCA_ConcurrentCallersAgree(t *testing.T) {
	cfg := NewConfig(t.TempDir(), DefaultProxyPort)

	const callers = 4
	cas := make([]*CA, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Go(func() {
			cas[i], errs[i] = LoadOrCreateCA(cfg)
		})
	}
	wg.Wait()

	disk, err := LoadCA(cfg)
	if err != nil {
		t.Fatalf("LoadCA after concurrent creation: %v", err)
	}
	for i := range callers {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if !bytes.Equal(cas[i].CertPEM, disk.CertPEM) {
			t.Errorf("caller %d holds a CA that differs from the one on disk", i)
		}
	}
}

func TestLoadCA_RejectsMismatchedKey(t *testing.T) {
	cfgA := NewConfig(t.TempDir(), DefaultProxyPort)
	cfgB := NewConfig(t.TempDir(), DefaultProxyPort)
	if _, err := CreateCA(cfgA); err != nil {
		t.Fatalf("CreateCA A: %v", err)
	}
	if _, err := CreateCA(cfgB); err != nil {
		t.Fatalf("CreateCA B: %v", err)
	}
	keyB, err := os.ReadFile(cfgB.CAKeyPath)
	if err != nil {
		t.Fatalf("read key B: %v", err)
	}
	if err := os.WriteFile(cfgA.CAKeyPath, keyB, 0o600); err != nil {
		t.Fatalf("write key B over A: %v", err)
	}

	_, err = LoadCA(cfgA)
	if err == nil {
		t.Fatal("LoadCA accepted a key that does not match the certificate")
	}
	if !strings.Contains(err.Error(), cfgA.CADir) {
		t.Errorf("error does not name the CA directory to remove: %v", err)
	}
}

func TestCreateCA_LeavesNoTempFiles(t *testing.T) {
	cfg := NewConfig(t.TempDir(), DefaultProxyPort)
	if _, err := LoadOrCreateCA(cfg); err != nil {
		t.Fatalf("LoadOrCreateCA: %v", err)
	}
	entries, err := os.ReadDir(cfg.CADir)
	if err != nil {
		t.Fatalf("read CA dir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestSignCertificate(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ca-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := NewConfig(tmpDir, DefaultProxyPort)

	ca, err := CreateCA(cfg)
	if err != nil {
		t.Fatalf("CreateCA failed: %v", err)
	}

	certPEM, keyPEM, err := ca.SignCertificate("example.com")
	if err != nil {
		t.Fatalf("SignCertificate failed: %v", err)
	}

	if len(certPEM) == 0 {
		t.Error("certificate PEM is empty")
	}

	if len(keyPEM) == 0 {
		t.Error("key PEM is empty")
	}

	// Parse and verify the certificate
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("failed to decode certificate PEM")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("failed to parse certificate: %v", err)
	}

	if cert.Subject.CommonName != "example.com" {
		t.Errorf("unexpected CN: %s", cert.Subject.CommonName)
	}

	if len(cert.DNSNames) == 0 || cert.DNSNames[0] != "example.com" {
		t.Errorf("unexpected DNS names: %v", cert.DNSNames)
	}

	// Verify the certificate is signed by our CA
	roots := x509.NewCertPool()
	roots.AddCert(ca.Certificate)

	opts := x509.VerifyOptions{
		Roots: roots,
	}

	if _, err := cert.Verify(opts); err != nil {
		t.Errorf("certificate verification failed: %v", err)
	}
}

func TestCAExists(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ca-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := NewConfig(tmpDir, DefaultProxyPort)

	if cfg.CAExists() {
		t.Error("CAExists should return false before creation")
	}

	if _, err := CreateCA(cfg); err != nil {
		t.Fatalf("CreateCA failed: %v", err)
	}

	if !cfg.CAExists() {
		t.Error("CAExists should return true after creation")
	}

	// Remove just the cert
	_ = os.Remove(cfg.CACertPath)

	if cfg.CAExists() {
		t.Error("CAExists should return false with missing cert")
	}
}

func TestEnsureCADir(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ca-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	caDir := filepath.Join(tmpDir, "nested", "ca", "dir")
	cfg := &Config{
		CADir: caDir,
	}

	if err := cfg.EnsureCADir(); err != nil {
		t.Fatalf("EnsureCADir failed: %v", err)
	}

	info, err := os.Stat(caDir)
	if err != nil {
		t.Fatalf("CA dir not created: %v", err)
	}

	if !info.IsDir() {
		t.Error("CA dir is not a directory")
	}

	if info.Mode().Perm() != 0o700 {
		t.Errorf("CA dir has wrong permissions: %o", info.Mode().Perm())
	}
}
