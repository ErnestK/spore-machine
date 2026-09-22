// Command primogenitor is the owner's one-shot CLI entry point into the
// network: launch the first node, submit a file or command, or terminate the
// network. No persistent loop — one action per invocation.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"time"

	"golang.org/x/crypto/curve25519"
	"google.golang.org/protobuf/proto"

	"sporemachine/internal/cryptoutil"
	"sporemachine/internal/ownersettings"
	"sporemachine/internal/rpcclient"
	"sporemachine/internal/sporepb"
)

const rpcTimeout = 5 * time.Second

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: primogenitor <start|submit-file|submit-command|terminate> [flags]")
	}
	cmd, args := os.Args[1], os.Args[2:]

	switch cmd {
	case "start":
		runStart(args)
	case "submit-file":
		runSubmitFile(args)
	case "submit-command":
		runSubmitCommand(args)
	case "terminate":
		runTerminate(args)
	default:
		log.Fatalf("primogenitor: unknown subcommand %q", cmd)
	}
}

func runStart(args []string) {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	settingsPath := fs.String("settings", "primogenitor-settings.yaml", "settings file path")
	nodeBinary := fs.String("node-binary", "", "path to the node binary (required)")
	host := fs.String("host", "127.0.0.1", "host the first node listens on")
	port := fs.Int("port", 17000, "port the first node listens on (fixed, so the address is known up front)")
	// Defaults below (depth 5, branching 3 -> 364 nodes max) are picked for
	// local debugging/testing, not production: they keep a full-depth swarm
	// comfortably within one machine's RAM/port/process limits. See
	// paper_point for the math on why 10/5 (the old defaults) was not.
	maxGenerationDepth := fs.Int("max-generation-depth", 5, "Genesis.SpawnRules.max_generation_depth")
	spawnIntervalSeconds := fs.Int("spawn-interval-seconds", 600, "Genesis.SpawnRules.spawn_interval_seconds")
	maxChildrenPerBatch := fs.Int("max-children-per-batch", 3, "Genesis.SpawnRules.max_children_per_batch")
	batchPauseSeconds := fs.Int("batch-pause-seconds", 900, "Genesis.SpawnRules.batch_pause_seconds")
	fs.Parse(args)

	if *nodeBinary == "" {
		log.Fatal("primogenitor: --node-binary is required")
	}
	s := loadSettingsFromPath(*settingsPath)

	ownerPriv, err := s.OwnerKeyPair()
	if err != nil {
		log.Fatalf("primogenitor: %v", err)
	}
	metricsPriv, err := s.MetricsPrivateKey()
	if err != nil {
		log.Fatalf("primogenitor: %v", err)
	}
	var metricsPub [32]byte
	curve25519.ScalarBaseMult(&metricsPub, &metricsPriv)

	genesis := &sporepb.Genesis{
		NetworkId:                  s.NetworkID,
		OwnerPublicKey:             ownerPriv.Public().(ed25519.PublicKey),
		MetricsEncryptionPublicKey: metricsPub[:],
		SpawnRules: &sporepb.SpawnRules{
			MaxGenerationDepth:   int32(*maxGenerationDepth),
			SpawnIntervalSeconds: int32(*spawnIntervalSeconds),
			SpawnMode:            sporepb.SpawnMode_PROCESS,
			MaxChildrenPerBatch:  int32(*maxChildrenPerBatch),
			BatchPauseSeconds:    int32(*batchPauseSeconds),
		},
	}
	genesisBytes, err := proto.Marshal(genesis)
	if err != nil {
		log.Fatalf("primogenitor: marshal genesis: %v", err)
	}
	genesisB64 := base64.StdEncoding.EncodeToString(genesisBytes)

	nodeCmd := exec.Command(*nodeBinary,
		"--genesis-b64="+genesisB64,
		"--identity=genesis",
		"--generation=0",
		"--host="+*host,
		"--port="+strconv.Itoa(*port),
	)
	nodeCmd.Stdout = os.Stdout
	nodeCmd.Stderr = os.Stderr
	if err := nodeCmd.Start(); err != nil {
		log.Fatalf("primogenitor: launch node: %v", err)
	}

	s.NodeAddress = fmt.Sprintf("%s:%d", *host, *port)
	if err := s.Save(*settingsPath); err != nil {
		log.Fatalf("primogenitor: save settings: %v", err)
	}
	fmt.Printf("first node started (pid=%d), node_address=%s written to %s\n", nodeCmd.Process.Pid, s.NodeAddress, *settingsPath)
}

func runSubmitFile(args []string) {
	fs := flag.NewFlagSet("submit-file", flag.ExitOnError)
	settingsPath := fs.String("settings", "primogenitor-settings.yaml", "settings file path")
	fs.Parse(args)
	if fs.NArg() != 1 {
		log.Fatal("usage: primogenitor submit-file [--settings path] <file>")
	}

	s := loadSettingsFromPath(*settingsPath)
	ownerPriv, err := s.OwnerKeyPair()
	if err != nil {
		log.Fatalf("primogenitor: %v", err)
	}
	content, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		log.Fatalf("primogenitor: read file: %v", err)
	}

	ack := submitData(s.NodeAddress, sporepb.DataType_FILE, content, ownerPriv)
	fmt.Printf("accepted=%v object_id=%x\n", ack.GetAccepted(), ack.GetObjectId())
}

func runSubmitCommand(args []string) {
	fs := flag.NewFlagSet("submit-command", flag.ExitOnError)
	settingsPath := fs.String("settings", "primogenitor-settings.yaml", "settings file path")
	fs.Parse(args)
	if fs.NArg() != 1 {
		log.Fatal(`usage: primogenitor submit-command [--settings path] "<bash command>"`)
	}

	s := loadSettingsFromPath(*settingsPath)
	ownerPriv, err := s.OwnerKeyPair()
	if err != nil {
		log.Fatalf("primogenitor: %v", err)
	}
	cmdBytes, err := proto.Marshal(&sporepb.Command{BashCommand: fs.Arg(0)})
	if err != nil {
		log.Fatalf("primogenitor: marshal command: %v", err)
	}

	ack := submitData(s.NodeAddress, sporepb.DataType_COMMAND, cmdBytes, ownerPriv)
	fmt.Printf("accepted=%v object_id=%x\n", ack.GetAccepted(), ack.GetObjectId())
}

func submitData(nodeAddress string, dataType sporepb.DataType, content []byte, ownerPriv ed25519.PrivateKey) *sporepb.SubmitAck {
	sig := cryptoutil.Sign(ownerPriv, content)
	client, conn, err := rpcclient.Dial(nodeAddress)
	if err != nil {
		log.Fatalf("primogenitor: dial %s: %v", nodeAddress, err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()
	ack, err := client.SubmitData(ctx, &sporepb.SubmitDataRequest{Type: dataType, Content: content, Signature: sig})
	if err != nil {
		log.Fatalf("primogenitor: submit: %v", err)
	}
	return ack
}

func runTerminate(args []string) {
	fs := flag.NewFlagSet("terminate", flag.ExitOnError)
	settingsPath := fs.String("settings", "primogenitor-settings.yaml", "settings file path")
	fs.Parse(args)

	s := loadSettingsFromPath(*settingsPath)
	ownerPriv, err := s.OwnerKeyPair()
	if err != nil {
		log.Fatalf("primogenitor: %v", err)
	}

	msg := []byte(s.NetworkID + "TERMINATE")
	sig := cryptoutil.Sign(ownerPriv, msg)

	client, conn, err := rpcclient.Dial(s.NodeAddress)
	if err != nil {
		log.Fatalf("primogenitor: dial %s: %v", s.NodeAddress, err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()
	ack, err := client.SubmitTerminate(ctx, &sporepb.TerminateRequest{Signature: sig})
	if err != nil {
		log.Fatalf("primogenitor: terminate: %v", err)
	}
	fmt.Printf("received=%v\n", ack.GetReceived())
}

func loadSettingsFromPath(path string) *ownersettings.Settings {
	s, err := ownersettings.Load(path)
	if err != nil {
		log.Fatalf("primogenitor: %v", err)
	}
	return s
}
