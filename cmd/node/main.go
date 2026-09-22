// Command node runs a single node. Genesis/identity/generation/seed_contacts
// always arrive via flags, since they cross an OS process boundary (from
// Primogenitor or from a parent's norn).
package main

import (
	"encoding/base64"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"google.golang.org/protobuf/proto"

	"sporemachine/internal/node"
	"sporemachine/internal/sporepb"
)

func main() {
	genesisB64 := flag.String("genesis-b64", "", "base64-encoded serialized Genesis (required)")
	identity := flag.String("identity", "genesis", "node identity (CandidateID); \"genesis\" for the first node")
	parentID := flag.String("parent-id", "", "parent's identity; empty for a node started directly by Primogenitor")
	generation := flag.Int("generation", 0, "generation number from Primogenitor")
	seedContacts := flag.String("seed-contacts", "", "comma-separated seed addresses")
	host := flag.String("host", "127.0.0.1", "gRPC listen host")
	port := flag.Int("port", 0, "port; 0 picks a free one")
	dbPath := flag.String("db", "", "bbolt file path; defaults to sporemachine-<identity>.db")
	flag.Parse()

	if *genesisB64 == "" {
		log.Fatal("node: --genesis-b64 is required")
	}
	genesisBytes, err := base64.StdEncoding.DecodeString(*genesisB64)
	if err != nil {
		log.Fatalf("node: decode --genesis-b64: %v", err)
	}
	genesis := &sporepb.Genesis{}
	if err := proto.Unmarshal(genesisBytes, genesis); err != nil {
		log.Fatalf("node: unmarshal Genesis: %v", err)
	}

	var seeds []string
	if *seedContacts != "" {
		seeds = strings.Split(*seedContacts, ",")
	}

	path := *dbPath
	if path == "" {
		path = "sporemachine-" + *identity + ".db"
	}

	cfg := node.Config{
		Genesis:      genesis,
		Identity:     *identity,
		ParentID:     *parentID,
		Generation:   int32(*generation),
		SeedContacts: seeds,
		Host:         *host,
		Port:         *port,
		DBPath:       path,
	}

	n, err := node.New(cfg, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		log.Fatalf("node: %v", err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		n.Shutdown()
	}()

	if err := n.Run(); err != nil {
		log.Printf("node: stopped: %v", err)
	}
}
