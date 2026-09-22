package ownersettings

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoad_RoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_ = pub

	want := &Settings{
		NetworkID:                   "net-1",
		OwnerPrivateKey:             base64.StdEncoding.EncodeToString(priv),
		MetricsEncryptionPrivateKey: base64.StdEncoding.EncodeToString(make([]byte, 32)),
		NodeAddress:                 "127.0.0.1:9000",
	}

	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := want.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if *got != *want {
		t.Errorf("round trip mismatch: got %+v, want %+v", got, want)
	}
}

func TestSave_FileMode0600(t *testing.T) {
	s := &Settings{NetworkID: "net-1"}
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := s.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("file mode = %v, want 0600", got)
	}
}

func TestOwnerKeyPair_InvalidBase64(t *testing.T) {
	s := &Settings{OwnerPrivateKey: "not-valid-base64!!"}
	if _, err := s.OwnerKeyPair(); err == nil {
		t.Error("OwnerKeyPair: want error for invalid base64, got nil")
	}
}

func TestOwnerKeyPair_WrongLengthIsRejected(t *testing.T) {
	s := &Settings{OwnerPrivateKey: base64.StdEncoding.EncodeToString([]byte("short"))}
	if _, err := s.OwnerKeyPair(); err == nil {
		t.Error("OwnerKeyPair: want error for wrong-length key, got nil")
	}
}

func TestMetricsPrivateKey_InvalidBase64(t *testing.T) {
	s := &Settings{MetricsEncryptionPrivateKey: "not-valid-base64!!"}
	if _, err := s.MetricsPrivateKey(); err == nil {
		t.Error("MetricsPrivateKey: want error for invalid base64, got nil")
	}
}

func TestMetricsPrivateKey_WrongLength(t *testing.T) {
	s := &Settings{MetricsEncryptionPrivateKey: base64.StdEncoding.EncodeToString([]byte("too-short"))}
	if _, err := s.MetricsPrivateKey(); err == nil {
		t.Error("MetricsPrivateKey: want error for wrong-length key, got nil")
	}
}

func TestMetricsPrivateKey_ValidRoundTrip(t *testing.T) {
	var want [32]byte
	if _, err := rand.Read(want[:]); err != nil {
		t.Fatal(err)
	}
	s := &Settings{MetricsEncryptionPrivateKey: base64.StdEncoding.EncodeToString(want[:])}
	got, err := s.MetricsPrivateKey()
	if err != nil {
		t.Fatalf("MetricsPrivateKey: %v", err)
	}
	if got != want {
		t.Errorf("MetricsPrivateKey = %x, want %x", got, want)
	}
}
