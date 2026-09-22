package chronicle

import (
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func saveSettings(t *testing.T, s *Settings, path string) {
	t.Helper()
	data, err := yaml.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSettings_RoundTrip(t *testing.T) {
	want := &Settings{
		NetworkID:                   "net-1",
		MetricsEncryptionPublicKey:  base64.StdEncoding.EncodeToString(make([]byte, 32)),
		MetricsEncryptionPrivateKey: base64.StdEncoding.EncodeToString(make([]byte, 32)),
		NodeAddress:                 "127.0.0.1:9000",
	}
	path := filepath.Join(t.TempDir(), "chronicle-settings.yaml")
	saveSettings(t, want, path)

	got, err := LoadSettings(path)
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if *got != *want {
		t.Errorf("round trip mismatch: got %+v, want %+v", got, want)
	}
}

func TestMetricsKeyPair_InvalidBase64(t *testing.T) {
	s := &Settings{MetricsEncryptionPublicKey: "!!!not-base64", MetricsEncryptionPrivateKey: base64.StdEncoding.EncodeToString(make([]byte, 32))}
	if _, _, err := s.MetricsKeyPair(); err == nil {
		t.Error("MetricsKeyPair: want error for invalid public key base64, got nil")
	}
}

func TestMetricsKeyPair_WrongLength(t *testing.T) {
	s := &Settings{
		MetricsEncryptionPublicKey:  base64.StdEncoding.EncodeToString([]byte("too-short")),
		MetricsEncryptionPrivateKey: base64.StdEncoding.EncodeToString(make([]byte, 32)),
	}
	if _, _, err := s.MetricsKeyPair(); err == nil {
		t.Error("MetricsKeyPair: want error for wrong-length public key, got nil")
	}
}

func TestMetricsKeyPair_ValidRoundTrip(t *testing.T) {
	var wantPub, wantPriv [32]byte
	if _, err := rand.Read(wantPub[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(wantPriv[:]); err != nil {
		t.Fatal(err)
	}
	s := &Settings{
		MetricsEncryptionPublicKey:  base64.StdEncoding.EncodeToString(wantPub[:]),
		MetricsEncryptionPrivateKey: base64.StdEncoding.EncodeToString(wantPriv[:]),
	}
	pub, priv, err := s.MetricsKeyPair()
	if err != nil {
		t.Fatalf("MetricsKeyPair: %v", err)
	}
	if pub != wantPub || priv != wantPriv {
		t.Error("MetricsKeyPair: round trip mismatch")
	}
}
