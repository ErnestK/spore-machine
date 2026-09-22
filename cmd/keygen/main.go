// Command keygen generates the owner's two keypairs and writes a ready-to-use
// primogenitor-settings.yaml. Key generation is deliberately kept out of
// Primogenitor itself; this is a separate one-off helper.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"log"

	"golang.org/x/crypto/nacl/box"

	"sporemachine/internal/ownersettings"
)

func main() {
	networkID := flag.String("network-id", "", "network ID for this network (required)")
	out := flag.String("out", "primogenitor-settings.yaml", "output settings file path")
	flag.Parse()

	if *networkID == "" {
		log.Fatal("keygen: --network-id is required")
	}

	ownerPub, ownerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatalf("keygen: generate owner key: %v", err)
	}
	metricsPub, metricsPriv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatalf("keygen: generate metrics key: %v", err)
	}

	s := &ownersettings.Settings{
		NetworkID:                   *networkID,
		OwnerPrivateKey:             base64.StdEncoding.EncodeToString(ownerPriv),
		MetricsEncryptionPrivateKey: base64.StdEncoding.EncodeToString(metricsPriv[:]),
	}
	if err := s.Save(*out); err != nil {
		log.Fatalf("keygen: %v", err)
	}

	fmt.Printf("wrote %s\n\n", *out)
	fmt.Printf("owner_public_key (base64):              %s\n", base64.StdEncoding.EncodeToString(ownerPub))
	fmt.Printf("metrics_encryption_public_key (base64): %s\n", base64.StdEncoding.EncodeToString(metricsPub[:]))
	fmt.Printf("\nmetrics_encryption_private_key (base64), for chronicle-settings.yaml:\n%s\n",
		base64.StdEncoding.EncodeToString(metricsPriv[:]))
}
