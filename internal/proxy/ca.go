package proxy

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"devsandbox/internal/fsutil"
)

const (
	rsaKeyBits     = 4096
	caValidityDays = 3650 // 10 years
	caLockFile     = ".lock"
)

type CA struct {
	Certificate *x509.Certificate
	PrivateKey  *rsa.PrivateKey
	CertPEM     []byte
}

// LoadOrCreateCA returns the sandbox's CA, creating it on first use.
//
// Concurrent launches of one project share the CA files, so the existence check
// and the creation run under an exclusive lock. Without it, two launches that
// both find no CA each generate one: the last writer wins on disk, and the
// other launch's proxy signs with a CA its sandbox does not trust, failing
// every TLS handshake until that session restarts.
func LoadOrCreateCA(cfg *Config) (*CA, error) {
	if err := cfg.EnsureCADir(); err != nil {
		return nil, fmt.Errorf("failed to create CA directory: %w", err)
	}
	lock, err := fsutil.AcquireFileLock(filepath.Join(cfg.CADir, caLockFile))
	if err != nil {
		return nil, fmt.Errorf("failed to lock CA directory: %w", err)
	}
	defer func() { _ = lock.Release() }()

	if cfg.CAExists() {
		return LoadCA(cfg)
	}
	return CreateCA(cfg)
}

func LoadCA(cfg *Config) (*CA, error) {
	certPEM, err := os.ReadFile(cfg.CACertPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read CA certificate: %w", err)
	}

	keyPEM, err := os.ReadFile(cfg.CAKeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read CA key: %w", err)
	}

	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, fmt.Errorf("failed to decode CA certificate PEM")
	}

	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse CA certificate: %w", err)
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, fmt.Errorf("failed to decode CA key PEM")
	}

	key, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse CA key: %w", err)
	}

	// A key that does not match the certificate signs leaf certificates no
	// client can verify, so every session would fail TLS with no clear cause.
	if !key.PublicKey.Equal(cert.PublicKey) {
		return nil, fmt.Errorf("CA key does not match CA certificate; remove %s to generate a new CA", cfg.CADir)
	}

	return &CA{
		Certificate: cert,
		PrivateKey:  key,
		CertPEM:     certPEM,
	}, nil
}

func CreateCA(cfg *Config) (*CA, error) {
	if err := cfg.EnsureCADir(); err != nil {
		return nil, fmt.Errorf("failed to create CA directory: %w", err)
	}

	privateKey, err := rsa.GenerateKey(rand.Reader, rsaKeyBits)
	if err != nil {
		return nil, fmt.Errorf("failed to generate CA key: %w", err)
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("failed to generate serial number: %w", err)
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization:       []string{"DevSandbox"},
			OrganizationalUnit: []string{"Proxy CA"},
			CommonName:         "DevSandbox Proxy CA",
		},
		NotBefore:             now,
		NotAfter:              now.AddDate(0, 0, caValidityDays),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create CA certificate: %w", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("failed to parse created certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	})

	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	})

	// Key first, certificate last: CAExists needs both files, so a reader never
	// sees a complete-looking pair before the certificate is in place, and each
	// rename leaves a file either absent or whole.
	if err := fsutil.WriteFileAtomic(cfg.CAKeyPath, keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("failed to write CA key: %w", err)
	}

	if err := fsutil.WriteFileAtomic(cfg.CACertPath, certPEM, 0o644); err != nil {
		return nil, fmt.Errorf("failed to write CA certificate: %w", err)
	}

	return &CA{
		Certificate: cert,
		PrivateKey:  privateKey,
		CertPEM:     certPEM,
	}, nil
}

func (ca *CA) SignCertificate(host string) ([]byte, []byte, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate server key: %w", err)
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate serial number: %w", err)
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"DevSandbox"},
			CommonName:   host,
		},
		NotBefore:             now,
		NotAfter:              now.AddDate(0, 0, 365),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{host},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, ca.Certificate, &privateKey.PublicKey, ca.PrivateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to sign certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	})

	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	})

	return certPEM, keyPEM, nil
}
