// Package ownersettings loads/saves the owner's local YAML settings file,
// shared by cmd/keygen (writes it) and cmd/primogenitor (reads/updates it).
package ownersettings

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Settings struct {
	NetworkID                   string `yaml:"network_id"`
	OwnerPrivateKey             string `yaml:"owner_private_key"`
	MetricsEncryptionPrivateKey string `yaml:"metrics_encryption_private_key"`
	NodeAddress                 string `yaml:"node_address"`
}

func Load(path string) (*Settings, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("ownersettings: read %s: %w", path, err)
	}
	var s Settings
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("ownersettings: parse %s: %w", path, err)
	}
	return &s, nil
}

func (s *Settings) Save(path string) error {
	data, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Errorf("ownersettings: marshal: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("ownersettings: write %s: %w", path, err)
	}
	return os.Chmod(path, 0o600)
}

func (s *Settings) OwnerKeyPair() (ed25519.PrivateKey, error) {
	raw, err := base64.StdEncoding.DecodeString(s.OwnerPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("ownersettings: decode owner_private_key: %w", err)
	}
	// ed25519.Sign panics (not a returned error) if the key isn't exactly
	// this length, so a truncated/corrupted config value must be caught
	// here — at load time, with a clear message — not at the first Sign call.
	if len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("ownersettings: owner_private_key must be %d bytes, got %d", ed25519.PrivateKeySize, len(raw))
	}
	return ed25519.PrivateKey(raw), nil
}

func (s *Settings) MetricsPrivateKey() ([32]byte, error) {
	var key [32]byte
	raw, err := base64.StdEncoding.DecodeString(s.MetricsEncryptionPrivateKey)
	if err != nil {
		return key, fmt.Errorf("ownersettings: decode metrics_encryption_private_key: %w", err)
	}
	if len(raw) != 32 {
		return key, fmt.Errorf("ownersettings: metrics_encryption_private_key must be 32 bytes, got %d", len(raw))
	}
	copy(key[:], raw)
	return key, nil
}
