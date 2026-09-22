// Package norn runs inside the node process as a goroutine and spawns new
// node processes on approved proposals.
package norn

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"sporemachine/internal/spawnproto"
	"sporemachine/internal/sporepb"
)

type Norn struct {
	genesis       *sporepb.Genesis
	ownGeneration int32
	proposals     chan<- spawnproto.SpawnProposal
	completions   chan<- spawnproto.SpawnCompleted
	nodeBinary    string
}

func New(genesis *sporepb.Genesis, ownGeneration int32, proposals chan<- spawnproto.SpawnProposal, completions chan<- spawnproto.SpawnCompleted) *Norn {
	nodeBinary, err := os.Executable()
	if err != nil {
		nodeBinary = os.Args[0]
	}
	return &Norn{genesis: genesis, ownGeneration: ownGeneration, proposals: proposals, completions: completions, nodeBinary: nodeBinary}
}

// spawnInterval resolves how often to propose a candidate; falls back to a
// 10-minute default when Genesis leaves it unset (<=0).
func spawnInterval(genesis *sporepb.Genesis) time.Duration {
	interval := time.Duration(genesis.GetSpawnRules().GetSpawnIntervalSeconds()) * time.Second
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	return interval
}

func (n *Norn) Run(ctx context.Context) {
	ticker := time.NewTicker(spawnInterval(n.genesis))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n.propose(ctx)
		}
	}
}

func (n *Norn) propose(ctx context.Context) {
	candidateID := uuid.NewString()
	reply := make(chan spawnproto.SpawnDecision, 1)

	select {
	case n.proposals <- spawnproto.SpawnProposal{CandidateID: candidateID, Reply: reply}:
	case <-ctx.Done():
		return
	}

	var decision spawnproto.SpawnDecision
	select {
	case decision = <-reply:
	case <-ctx.Done():
		return
	}

	if !decision.Agree {
		return
	}

	generation := n.ownGeneration + 1
	address, err := n.spawn(candidateID, generation, decision.SeedContacts)
	if err != nil {
		log.Printf("norn: spawn failed for candidate %s: %v", candidateID, err)
		return
	}

	select {
	case n.completions <- spawnproto.SpawnCompleted{CandidateID: candidateID, Address: address}:
	case <-ctx.Done():
	}
}

// spawn launches a new OS process for the candidate and returns its address.
// DOCKER is reserved in Genesis but not implemented in v0.
func (n *Norn) spawn(candidateID string, generation int32, seedContacts []string) (string, error) {
	mode := n.genesis.GetSpawnRules().GetSpawnMode()
	if mode != sporepb.SpawnMode_PROCESS {
		return "", errUnsupportedSpawnMode(mode)
	}

	// Pick the child's port ourselves, before exec, so we know its address
	// up front instead of waiting for it to announce itself over SWIM.
	port, err := freePort()
	if err != nil {
		return "", fmt.Errorf("norn: find free port: %w", err)
	}
	address := fmt.Sprintf("127.0.0.1:%d", port)

	genesisBytes, err := proto.Marshal(n.genesis)
	if err != nil {
		return "", err
	}
	genesisB64 := base64.StdEncoding.EncodeToString(genesisBytes)

	args := []string{
		"--genesis-b64=" + genesisB64,
		"--identity=" + candidateID,
		"--generation=" + strconv.Itoa(int(generation)),
		"--seed-contacts=" + strings.Join(seedContacts, ","),
		"--host=127.0.0.1",
		"--port=" + strconv.Itoa(port),
	}
	cmd := exec.Command(n.nodeBinary, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return "", err
	}
	return address, nil
}

// freePort briefly binds :0 to let the OS assign a port, then releases it.
// Small race between release and the child's own bind; accepted for v0.
// A package var (not a plain func) so tests can force a failure.
var freePort = func() (int, error) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer lis.Close()
	return lis.Addr().(*net.TCPAddr).Port, nil
}

type errUnsupportedSpawnMode sporepb.SpawnMode

func (e errUnsupportedSpawnMode) Error() string {
	return "norn: spawn mode " + sporepb.SpawnMode(e).String() + " not implemented in v0"
}
