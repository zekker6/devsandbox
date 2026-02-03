// Package config provides configuration file support for devsandbox.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

// TrustedConfig represents a trusted local config file.
type TrustedConfig struct {
	Path  string    `toml:"path"`
	Hash  string    `toml:"hash"`
	Added time.Time `toml:"added"`
}

// TrustStore manages trusted local configurations.
type TrustStore struct {
	path    string          // not serialized, set during load
	Trusted []TrustedConfig `toml:"trusted"`
}

// TrustStorePath returns the default path to the trust store.
func TrustStorePath() string {
	return filepath.Join(configDir(), "trusted-configs.toml")
}

// LoadTrustStore loads the trust store from the given path.
// Returns an empty store if the file doesn't exist.
func LoadTrustStore(path string) (*TrustStore, error) {
	store := &TrustStore{path: path}

	if path == "" {
		return store, nil
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read trust store: %w", err)
	}

	if err := toml.Unmarshal(data, store); err != nil {
		return nil, fmt.Errorf("failed to parse trust store: %w", err)
	}

	return store, nil
}

// Save writes the trust store to its path.
func (s *TrustStore) Save() error {
	if s.path == "" {
		return fmt.Errorf("trust store has no path set")
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	f, err := os.Create(s.path)
	if err != nil {
		return fmt.Errorf("failed to create trust store: %w", err)
	}

	encoder := toml.NewEncoder(f)
	encodeErr := encoder.Encode(s)

	closeErr := f.Close()
	if encodeErr != nil {
		return fmt.Errorf("failed to write trust store: %w", encodeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("failed to close trust store: %w", closeErr)
	}

	return nil
}

// Path returns the trust store's file path.
func (s *TrustStore) Path() string {
	return s.path
}

// IsTrusted checks if the given path is trusted with the given hash.
func (s *TrustStore) IsTrusted(path, hash string) bool {
	for _, tc := range s.Trusted {
		if tc.Path == path && tc.Hash == hash {
			return true
		}
	}
	return false
}

// GetTrusted returns the trusted config for the given path, or nil if not found.
func (s *TrustStore) GetTrusted(path string) *TrustedConfig {
	for i := range s.Trusted {
		if s.Trusted[i].Path == path {
			return &s.Trusted[i]
		}
	}
	return nil
}

// AddTrust adds or updates a trusted config.
func (s *TrustStore) AddTrust(path, hash string) {
	now := time.Now().UTC()

	// Update existing entry
	for i := range s.Trusted {
		if s.Trusted[i].Path == path {
			s.Trusted[i].Hash = hash
			s.Trusted[i].Added = now
			return
		}
	}

	// Add new entry
	s.Trusted = append(s.Trusted, TrustedConfig{
		Path:  path,
		Hash:  hash,
		Added: now,
	})
}

// RemoveTrust removes trust for the given path.
// Returns true if an entry was removed.
func (s *TrustStore) RemoveTrust(path string) bool {
	for i := range s.Trusted {
		if s.Trusted[i].Path == path {
			s.Trusted = append(s.Trusted[:i], s.Trusted[i+1:]...)
			return true
		}
	}
	return false
}

// HashFile computes the SHA256 hash of a file.
func HashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}
