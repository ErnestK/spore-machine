package main

import (
	"os/exec"
	"path/filepath"
	"testing"

	"sporemachine/internal/ownersettings"
)

// Smoke test: run the keygen binary as a subprocess (package main has no
// testable seam separate from flag parsing / log.Fatal) and check the file
// it produces is actually loadable and its keys decode.
func TestKeygen_ProducesLoadableSettings(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "primogenitor-settings.yaml")

	cmd := exec.Command("go", "run", ".", "--network-id=test-net", "--out="+outPath)
	cmd.Dir = "."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("keygen run failed: %v\noutput:\n%s", err, out)
	}

	settings, err := ownersettings.Load(outPath)
	if err != nil {
		t.Fatalf("ownersettings.Load(%s): %v", outPath, err)
	}
	if settings.NetworkID != "test-net" {
		t.Errorf("network_id = %q, want %q", settings.NetworkID, "test-net")
	}

	if _, err := settings.OwnerKeyPair(); err != nil {
		t.Errorf("OwnerKeyPair: %v", err)
	}
	if _, err := settings.MetricsPrivateKey(); err != nil {
		t.Errorf("MetricsPrivateKey: %v", err)
	}
}

func TestKeygen_MissingNetworkID_Fails(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "settings.yaml")
	cmd := exec.Command("go", "run", ".", "--out="+outPath)
	cmd.Dir = "."
	if err := cmd.Run(); err == nil {
		t.Error("keygen: want non-zero exit when --network-id is missing, got success")
	}
}
