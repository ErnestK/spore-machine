// Package node wires all node parts together into one process and serves
// NodeService, delegating each call to the relevant part.
package node

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc"

	"sporemachine/internal/commandexec"
	"sporemachine/internal/gossipsync"
	"sporemachine/internal/membership"
	"sporemachine/internal/metrics"
	"sporemachine/internal/norn"
	"sporemachine/internal/rpcclient"
	"sporemachine/internal/spawncontrol"
	"sporemachine/internal/spawnproto"
	"sporemachine/internal/sporepb"
	"sporemachine/internal/storage"
)

// Config is what a node receives at startup, either from Primogenitor (first
// node) or from its parent's norn (later generations).
type Config struct {
	Genesis      *sporepb.Genesis
	Identity     string // CandidateID; "genesis" for the first node
	ParentID     string // empty for a node started directly by Primogenitor
	Generation   int32
	SeedContacts []string
	Host         string
	Port         int // 0 = pick a free port
	DBPath       string
}

type Node struct {
	cfg Config

	store  *storage.Store
	mem    *membership.Membership
	exec   *commandexec.Executor
	gossip *gossipsync.GossipSync

	spawnCtl    *spawncontrol.Controller
	norn        *norn.Norn
	proposals   chan spawnproto.SpawnProposal
	completions chan spawnproto.SpawnCompleted

	metricsCollector *metrics.Collector

	ownerPublicKey ed25519.PublicKey

	selfAddress string
	listener    net.Listener
	grpcServer  *grpc.Server

	ctx    context.Context
	cancel context.CancelFunc

	terminateMu      sync.Mutex
	terminateHandled bool
}

func New(cfg Config, createdAtUTC string) (*Node, error) {
	store, err := storage.Open(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("node: open storage: %w", err)
	}

	ownerPublicKey := ed25519.PublicKey(cfg.Genesis.GetOwnerPublicKey())

	lis, err := net.Listen("tcp", fmt.Sprintf("%s:%d", cfg.Host, cfg.Port))
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("node: listen: %w", err)
	}
	selfAddress := lis.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())

	mem := membership.New(selfAddress, cfg.SeedContacts)
	exec := commandexec.New()
	gossip := gossipsync.New(ctx, store, mem, exec, ownerPublicKey, selfAddress)

	proposals := make(chan spawnproto.SpawnProposal)
	completions := make(chan spawnproto.SpawnCompleted)
	spawnCtl := spawncontrol.New(cfg.Genesis, cfg.Generation, mem)
	n := norn.New(cfg.Genesis, cfg.Generation, proposals, completions)

	var metricsPublicKey [32]byte
	copy(metricsPublicKey[:], cfg.Genesis.GetMetricsEncryptionPublicKey())
	metricsCollector := metrics.New(cfg.Identity, createdAtUTC, cfg.ParentID, store, mem, exec, gossip, metricsPublicKey)

	nd := &Node{
		cfg:              cfg,
		store:            store,
		mem:              mem,
		exec:             exec,
		gossip:           gossip,
		spawnCtl:         spawnCtl,
		norn:             n,
		proposals:        proposals,
		completions:      completions,
		metricsCollector: metricsCollector,
		ownerPublicKey:   ownerPublicKey,
		selfAddress:      selfAddress,
		listener:         lis,
		ctx:              ctx,
		cancel:           cancel,
	}
	nd.grpcServer = grpc.NewServer()
	sporepb.RegisterNodeServiceServer(nd.grpcServer, &server{Node: nd})
	return nd, nil
}

// maxGoroutineRestarts is how many times a crashed persistent goroutine is
// restarted before the whole node gives up and shuts down (structure/d2/0.1_main_init.md §2.2).
const maxGoroutineRestarts = 3

func (n *Node) Run() error {
	giveUp := n.Shutdown
	go runSupervised(n.ctx, "membership", n.mem.Run, giveUp)
	go runSupervised(n.ctx, "gossipsync", n.gossip.Run, giveUp)
	go runSupervised(n.ctx, "spawncontrol", func(ctx context.Context) {
		n.spawnCtl.Run(ctx, n.proposals, n.completions)
	}, giveUp)
	go runSupervised(n.ctx, "norn", n.norn.Run, giveUp)
	go runSupervised(n.ctx, "metrics", n.metricsCollector.Run, giveUp)

	log.Printf("node: listening on %s (identity=%s generation=%d)", n.selfAddress, n.cfg.Identity, n.cfg.Generation)
	return n.grpcServer.Serve(n.listener)
}

// runSupervised runs fn(ctx) and restarts it on panic, up to maxGoroutineRestarts
// times. A normal return (ctx cancelled, node shutting down) is not a crash and
// is not restarted. If fn keeps panicking after all restarts are used up, onGiveUp
// is called once so the whole node shuts down instead of running with a dead part.
func runSupervised(ctx context.Context, name string, fn func(context.Context), onGiveUp func()) {
	for attempt := 1; ; attempt++ {
		if !runOnceRecovering(ctx, name, fn) {
			return // fn returned normally — ctx was cancelled, nothing to restart
		}
		if ctx.Err() != nil {
			return // shutting down anyway, don't restart into a cancelled context
		}
		if attempt > maxGoroutineRestarts {
			log.Printf("node: %s panicked %d times, giving up — shutting down node", name, attempt)
			onGiveUp()
			return
		}
		log.Printf("node: %s panicked (restart %d/%d)", name, attempt, maxGoroutineRestarts)
	}
}

// runOnceRecovering runs fn(ctx) once, recovering a panic. It reports whether
// fn panicked (true) as opposed to returning normally (false).
func runOnceRecovering(ctx context.Context, name string, fn func(context.Context)) (panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("node: %s panic: %v", name, r)
			panicked = true
		}
	}()
	fn(ctx)
	return false
}

// Shutdown is abrupt, not graceful: cancel, stop, close, in that order,
// without waiting for anything in flight.
func (n *Node) Shutdown() {
	n.cancel()
	n.grpcServer.Stop()
	_ = n.store.Close()
}

// The methods below exist only so integration tests can observe/drive a real
// Node without waiting on production tickers (30s gossip round, 3min metrics
// cycle) or reaching into unexported fields. Not called by production code.

// Address returns the address this node actually listens on (Port:0 in
// Config picks one dynamically, so callers must read it back).
func (n *Node) Address() string { return n.selfAddress }

// KnowsAlive reports whether addr is currently ALIVE in this node's own
// membership view.
func (n *Node) KnowsAlive(addr string) bool { return n.mem.IsAlive(addr) }

// TriggerGossipRound runs one gossip/Merkle round immediately.
func (n *Node) TriggerGossipRound() { n.gossip.TriggerRound(n.ctx) }

// TriggerMetricsCycle runs one metrics collection cycle immediately.
func (n *Node) TriggerMetricsCycle() { n.metricsCollector.Cycle() }

// TestAllMetricsObjectIDs returns every object ID currently in this node's
// metrics tree, by scanning all 256 leaf groups — there is no production RPC
// to list tree contents (a real Chronicle/peer only ever asks for specific
// IDs it already learned about via Merkle sync).
func (n *Node) TestAllMetricsObjectIDs() [][]byte {
	var all [][]byte
	for g := 0; g < 256; g++ {
		ids, err := n.store.LeafGroupIDs(storage.TreeMetrics, byte(g))
		if err != nil {
			continue
		}
		all = append(all, ids...)
	}
	return all
}

// initiateTermination fans the terminate request out to every known peer,
// waits for acks (bounded so an unresponsive peer can't hang this forever),
// then shuts down.
func (n *Node) initiateTermination(req *sporepb.TerminateRequest) {
	var wg sync.WaitGroup
	for _, addr := range n.mem.AllAlivePeers() {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			client, conn, err := rpcclient.Dial(addr)
			if err != nil {
				return
			}
			defer conn.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _ = client.SubmitTerminate(ctx, req)
		}(addr)
	}
	wg.Wait()
	n.Shutdown()
}
