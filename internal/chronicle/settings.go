// Package chronicle implements the Chronicle observer: a client-side pull of
// the node's metrics tree, decryption, and export tools.
package chronicle

import (
	"encoding/base64"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Settings struct {
	NetworkID                   string `yaml:"network_id"`
	MetricsEncryptionPublicKey  string `yaml:"metrics_encryption_public_key"`
	MetricsEncryptionPrivateKey string `yaml:"metrics_encryption_private_key"`
	NodeAddress                 string `yaml:"node_address"`
}

func LoadSettings(path string) (*Settings, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("chronicle: read %s: %w", path, err)
	}
	var s Settings
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("chronicle: parse %s: %w", path, err)
	}
	return &s, nil
}

func (s *Settings) MetricsKeyPair() (pub, priv [32]byte, err error) {
	pubRaw, err := base64.StdEncoding.DecodeString(s.MetricsEncryptionPublicKey)
	if err != nil {
		return pub, priv, fmt.Errorf("chronicle: decode metrics_encryption_public_key: %w", err)
	}
	privRaw, err := base64.StdEncoding.DecodeString(s.MetricsEncryptionPrivateKey)
	if err != nil {
		return pub, priv, fmt.Errorf("chronicle: decode metrics_encryption_private_key: %w", err)
	}
	if len(pubRaw) != 32 || len(privRaw) != 32 {
		return pub, priv, fmt.Errorf("chronicle: metrics keys must be 32 bytes")
	}
	copy(pub[:], pubRaw)
	copy(priv[:], privRaw)
	return pub, priv, nil
}
