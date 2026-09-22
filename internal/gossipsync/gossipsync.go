// Package gossipsync is the single intake point for new objects and the only
// distribution mechanism: a periodic round exchanging root hashes and known
// peers, triggering a Merkle walk on divergence.
package gossipsync

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"sporemachine/internal/commandexec"
	"sporemachine/internal/cryptoutil"
	"sporemachine/internal/membership"
	"sporemachine/internal/merkle"
	"sporemachine/internal/rpcclient"
	"sporemachine/internal/sporepb"
	"sporemachine/internal/storage"
)

const (
	roundInterval = 30 * time.Second
	roundPeers    = 3
	rpcTimeout    = 5 * time.Second
)

type GossipSync struct {
	store          *storage.Store
	mem            *membership.Membership
	exec           *commandexec.Executor
	ownerPublicKey ed25519.PublicKey
	selfAddress    string

	// rootCtx is the node's lifetime context, used for anything that must
	// outlive a single RPC call (e.g. dispatching a command for execution).
	// A gRPC handler's own ctx is cancelled as soon as the handler returns,
	// which is wrong for fire-and-forget work started from inside it.
	rootCtx context.Context

	// syncMu/inFlight guard against overlapping syncTree calls for the same
	// peer+tree: exchangeWith fires syncTree in its own goroutine without
	// waiting for it, so a slow sync still running when the next round ticks
	// could otherwise pile up multiple concurrent syncs against the same
	// target.
	syncMu   sync.Mutex
	inFlight map[string]struct{}

	statsMu                            sync.Mutex
	rounds, objectsSynced, bytesSynced int64
}

func New(rootCtx context.Context, store *storage.Store, mem *membership.Membership, exec *commandexec.Executor, ownerPublicKey ed25519.PublicKey, selfAddress string) *GossipSync {
	return &GossipSync{
		rootCtx:        rootCtx,
		store:          store,
		mem:            mem,
		exec:           exec,
		ownerPublicKey: ownerPublicKey,
		selfAddress:    selfAddress,
		inFlight:       make(map[string]struct{}),
	}
}

func (g *GossipSync) Run(ctx context.Context) {
	ticker := time.NewTicker(roundInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			g.round(ctx)
		}
	}
}

// TriggerRound runs one round immediately, without waiting for the ticker.
// Exposed for tests — production code only reaches round() via Run's ticker.
func (g *GossipSync) TriggerRound(ctx context.Context) {
	g.round(ctx)
}

func (g *GossipSync) round(ctx context.Context) {
	g.statsMu.Lock()
	g.rounds++
	g.statsMu.Unlock()

	for _, addr := range g.mem.RandomAlivePeers(roundPeers) {
		g.exchangeWith(ctx, addr)
	}
}

func (g *GossipSync) exchangeWith(ctx context.Context, addr string) {
	client, conn, err := rpcclient.Dial(addr)
	if err != nil {
		return
	}
	defer conn.Close()

	dataTree, err := merkle.Build(g.store.LeafProviderFor(storage.TreeData))
	if err != nil {
		return
	}
	metricsTree, err := merkle.Build(g.store.LeafProviderFor(storage.TreeMetrics))
	if err != nil {
		return
	}
	dataRoot := dataTree.Root()
	metricsRoot := metricsTree.Root()

	rpcCtx, cancel := context.WithTimeout(ctx, rpcTimeout)
	resp, err := client.GossipRound(rpcCtx, &sporepb.GossipExchange{
		DataRootHash:    dataRoot[:],
		MetricsRootHash: metricsRoot[:],
		KnownPeers:      g.knownPeers(),
	})
	cancel()
	if err != nil {
		return
	}

	g.mem.Learn(resp.GetKnownPeers())

	if !bytes.Equal(resp.GetDataRootHash(), dataRoot[:]) {
		g.startSyncTree(ctx, addr, storage.TreeData, dataTree)
	}
	if !bytes.Equal(resp.GetMetricsRootHash(), metricsRoot[:]) {
		g.startSyncTree(ctx, addr, storage.TreeMetrics, metricsTree)
	}
}

// startSyncTree launches syncTree unless one is already running for the same
// peer+tree — exchangeWith doesn't wait for syncTree to finish, so an
// overlapping round (slow network, large backlog) could otherwise pile up
// multiple concurrent syncs against the same target.
func (g *GossipSync) startSyncTree(ctx context.Context, addr string, tree storage.Tree, myTree *merkle.Tree) {
	key := addr + "|" + treeLabel(tree)
	g.syncMu.Lock()
	if _, busy := g.inFlight[key]; busy {
		g.syncMu.Unlock()
		return
	}
	g.inFlight[key] = struct{}{}
	g.syncMu.Unlock()

	go func() {
		defer func() {
			g.syncMu.Lock()
			delete(g.inFlight, key)
			g.syncMu.Unlock()
		}()
		g.syncTree(ctx, addr, tree, myTree)
	}()
}

func treeLabel(t storage.Tree) string {
	if t == storage.TreeMetrics {
		return "metrics"
	}
	return "data"
}

func (g *GossipSync) knownPeers() []string {
	return append(g.mem.AllAlivePeers(), g.selfAddress)
}

// syncTree walks one level at a time, batching all diverging children into
// one request per level; myTree is a fixed snapshot for the whole walk.
func (g *GossipSync) syncTree(ctx context.Context, addr string, tree storage.Tree, myTree *merkle.Tree) {
	client, conn, err := rpcclient.Dial(addr)
	if err != nil {
		return
	}
	defer conn.Close()

	protoTree := toProtoTreeKind(tree)
	frontier := []merkle.Path{merkle.Root()}

	for depth := uint32(0); depth < merkle.Depth && len(frontier) > 0; depth++ {
		reqPaths := make([]*sporepb.MerklePath, len(frontier))
		for i, p := range frontier {
			reqPaths[i] = &sporepb.MerklePath{Depth: p.Depth, PrefixBits: p.Prefix}
		}
		rpcCtx, cancel := context.WithTimeout(ctx, rpcTimeout)
		resp, err := client.MerkleChildren(rpcCtx, &sporepb.MerkleChildrenRequest{Tree: protoTree, Paths: reqPaths})
		cancel()
		if err != nil || len(resp.GetNodes()) != len(frontier) {
			return
		}

		var next []merkle.Path
		for i, p := range frontier {
			leftPath, rightPath := p.Children()
			myLeft, myRight := myTree.ChildrenHashes(p)
			peer := resp.GetNodes()[i]
			if !bytes.Equal(myLeft[:], peer.GetLeftHash()) {
				next = append(next, leftPath)
			}
			if !bytes.Equal(myRight[:], peer.GetRightHash()) {
				next = append(next, rightPath)
			}
		}
		frontier = next
	}
	if len(frontier) == 0 {
		return
	}

	groupPrefixes := make([]uint32, len(frontier))
	for i, p := range frontier {
		groupPrefixes[i] = p.Prefix
	}
	rpcCtx, cancel := context.WithTimeout(ctx, rpcTimeout)
	leafResp, err := client.MerkleLeaf(rpcCtx, &sporepb.MerkleLeafRequest{Tree: protoTree, GroupPrefixes: groupPrefixes})
	cancel()
	if err != nil {
		return
	}

	var missing [][]byte
	for _, grp := range leafResp.GetGroups() {
		mine, err := g.store.LeafGroupIDs(tree, byte(grp.GetGroupPrefix()))
		if err != nil {
			continue
		}
		mineSet := make(map[string]struct{}, len(mine))
		for _, id := range mine {
			mineSet[string(id)] = struct{}{}
		}
		for _, id := range grp.GetObjectIds() {
			if _, ok := mineSet[string(id)]; !ok {
				missing = append(missing, id)
			}
		}
	}
	if len(missing) == 0 {
		return
	}

	rpcCtx, cancel = context.WithTimeout(ctx, rpcTimeout)
	objResp, err := client.GetObjects(rpcCtx, &sporepb.GetObjectsRequest{Tree: protoTree, ObjectIds: missing})
	cancel()
	if err != nil {
		return
	}

	syncedCount, syncedBytes := g.ingestBatch(tree, missing, objResp.GetObjects())

	g.statsMu.Lock()
	g.objectsSynced += syncedCount
	g.bytesSynced += syncedBytes
	g.statsMu.Unlock()
}

// validate checks hash and, for data, signature — shared by ingest and
// ingestBatch, which differ only in how they write the result.
func (g *GossipSync) validate(tree storage.Tree, id []byte, obj *sporepb.StoredObject) bool {
	if !bytes.Equal(cryptoutil.ObjectID(obj.GetContent()), id) {
		return false
	}
	if tree == storage.TreeData {
		return cryptoutil.VerifySignature(g.ownerPublicKey, obj.GetContent(), obj.GetSignature())
	}
	return true
}

// ingest is the single intake point for one object at a time, used by
// SubmitData: hash and signature are always rechecked here, even for objects
// received from a peer rather than directly from the owner.
func (g *GossipSync) ingest(tree storage.Tree, id []byte, obj *sporepb.StoredObject) bool {
	if !g.validate(tree, id, obj) {
		return false
	}
	isNew, err := g.store.PutObject(tree, id, obj)
	if err != nil {
		return false
	}
	// Dispatch for execution only when this object is genuinely new to this
	// node's storage — a byte-identical resubmission (from the owner, or
	// redelivered via gossip/Merkle-sync) must not re-execute the command
	// (SRS 009, resubmission addendum).
	if isNew && tree == storage.TreeData && obj.GetType() == sporepb.DataType_COMMAND {
		var cmd sporepb.Command
		if err := proto.Unmarshal(obj.GetContent(), &cmd); err == nil {
			// g.rootCtx, not a caller-supplied ctx: this dispatch must
			// outlive the RPC handler that triggered it.
			g.exec.Submit(g.rootCtx, cmd.GetBashCommand())
		}
	}
	return true
}

// ingestBatch validates each object the same way ingest does, but writes all
// valid ones in a single storage transaction instead of one per object —
// meant for a Merkle catch-up batch, where many objects can arrive together
// (see structure/d3/storage-putobject-per-object-transaction.md).
func (g *GossipSync) ingestBatch(tree storage.Tree, ids [][]byte, objs []*sporepb.StoredObject) (count int64, totalBytes int64) {
	n := min(len(ids), len(objs))
	var toStore []storage.ObjectToPut
	for i := range n {
		if g.validate(tree, ids[i], objs[i]) {
			toStore = append(toStore, storage.ObjectToPut{ID: ids[i], Obj: objs[i]})
		}
	}
	if len(toStore) == 0 {
		return 0, 0
	}
	newFlags, err := g.store.PutObjects(tree, toStore)
	if err != nil {
		return 0, 0
	}
	for i, it := range toStore {
		count++
		totalBytes += int64(len(it.Obj.GetContent()))
		// Same idempotency rule as ingest: dispatch only genuinely new commands.
		if newFlags[i] && tree == storage.TreeData && it.Obj.GetType() == sporepb.DataType_COMMAND {
			var cmd sporepb.Command
			if err := proto.Unmarshal(it.Obj.GetContent(), &cmd); err == nil {
				g.exec.Submit(g.rootCtx, cmd.GetBashCommand())
			}
		}
	}
	return count, totalBytes
}

func (g *GossipSync) HandleSubmitData(req *sporepb.SubmitDataRequest) *sporepb.SubmitAck {
	id := cryptoutil.ObjectID(req.GetContent())
	obj := &sporepb.StoredObject{Type: req.GetType(), Content: req.GetContent(), Signature: req.GetSignature()}
	accepted := g.ingest(storage.TreeData, id, obj)
	return &sporepb.SubmitAck{Accepted: accepted, ObjectId: id}
}

// HandleGossipRound only answers with our own state; the comparison and any
// resulting Merkle walk are always driven by the caller (see package doc).
func (g *GossipSync) HandleGossipRound(req *sporepb.GossipExchange) *sporepb.GossipExchange {
	g.mem.Learn(req.GetKnownPeers())

	dataTree, err := merkle.Build(g.store.LeafProviderFor(storage.TreeData))
	if err != nil {
		return &sporepb.GossipExchange{}
	}
	metricsTree, err := merkle.Build(g.store.LeafProviderFor(storage.TreeMetrics))
	if err != nil {
		return &sporepb.GossipExchange{}
	}
	dataRoot := dataTree.Root()
	metricsRoot := metricsTree.Root()
	return &sporepb.GossipExchange{
		DataRootHash:    dataRoot[:],
		MetricsRootHash: metricsRoot[:],
		KnownPeers:      g.knownPeers(),
	}
}

func (g *GossipSync) HandleMerkleChildren(req *sporepb.MerkleChildrenRequest) *sporepb.MerkleChildrenResponse {
	tree := fromProtoTreeKind(req.GetTree())
	t, err := merkle.Build(g.store.LeafProviderFor(tree))
	if err != nil {
		return &sporepb.MerkleChildrenResponse{}
	}
	nodes := make([]*sporepb.MerkleNodeHashes, len(req.GetPaths()))
	for i, p := range req.GetPaths() {
		left, right := t.ChildrenHashes(merkle.Path{Depth: p.GetDepth(), Prefix: p.GetPrefixBits()})
		nodes[i] = &sporepb.MerkleNodeHashes{LeftHash: left[:], RightHash: right[:]}
	}
	return &sporepb.MerkleChildrenResponse{Nodes: nodes}
}

func (g *GossipSync) HandleMerkleLeaf(req *sporepb.MerkleLeafRequest) *sporepb.MerkleLeafResponse {
	tree := fromProtoTreeKind(req.GetTree())
	groups := make([]*sporepb.MerkleLeafGroup, 0, len(req.GetGroupPrefixes()))
	for _, gp := range req.GetGroupPrefixes() {
		ids, err := g.store.LeafGroupIDs(tree, byte(gp))
		if err != nil {
			continue
		}
		groups = append(groups, &sporepb.MerkleLeafGroup{GroupPrefix: gp, ObjectIds: ids})
	}
	return &sporepb.MerkleLeafResponse{Groups: groups}
}

func (g *GossipSync) HandleGetObjects(req *sporepb.GetObjectsRequest) *sporepb.GetObjectsResponse {
	tree := fromProtoTreeKind(req.GetTree())
	objs := make([]*sporepb.StoredObject, 0, len(req.GetObjectIds()))
	for _, id := range req.GetObjectIds() {
		obj, err := g.store.GetObject(tree, id)
		if err != nil || obj == nil {
			continue
		}
		objs = append(objs, obj)
	}
	return &sporepb.GetObjectsResponse{Objects: objs}
}

// SyncStats is reset on each read; node-metrics reads and resets it once per
// snapshot cycle.
type SyncStats struct {
	Rounds, ObjectsSynced, BytesSynced int64
}

func (g *GossipSync) ResetAndRead() SyncStats {
	g.statsMu.Lock()
	defer g.statsMu.Unlock()
	st := SyncStats{Rounds: g.rounds, ObjectsSynced: g.objectsSynced, BytesSynced: g.bytesSynced}
	g.rounds, g.objectsSynced, g.bytesSynced = 0, 0, 0
	return st
}

func toProtoTreeKind(t storage.Tree) sporepb.TreeKind {
	if t == storage.TreeMetrics {
		return sporepb.TreeKind_METRICS
	}
	return sporepb.TreeKind_DATA
}

func fromProtoTreeKind(t sporepb.TreeKind) storage.Tree {
	if t == sporepb.TreeKind_METRICS {
		return storage.TreeMetrics
	}
	return storage.TreeData
}
