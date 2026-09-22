package chronicle

import (
	"bytes"
	"context"
	"time"

	"sporemachine/internal/cryptoutil"
	"sporemachine/internal/merkle"
	"sporemachine/internal/rpcclient"
	"sporemachine/internal/sporepb"
	"sporemachine/internal/storage"
)

const rpcTimeout = 5 * time.Second

type Syncer struct {
	store       *storage.Store
	nodeAddress string
}

func NewSyncer(store *storage.Store, nodeAddress string) *Syncer {
	return &Syncer{store: store, nodeAddress: nodeAddress}
}

// Pull compares Chronicle's own metrics tree against the known node's and
// fetches whatever is missing. Chronicle only ever pulls (it has nothing of
// its own to push into the network's metrics tree).
func (s *Syncer) Pull(ctx context.Context) (fetched int, err error) {
	client, conn, err := rpcclient.Dial(s.nodeAddress)
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	myTree, err := merkle.Build(s.store.LeafProviderFor(storage.TreeMetrics))
	if err != nil {
		return 0, err
	}
	myRoot := myTree.Root()

	rpcCtx, cancel := context.WithTimeout(ctx, rpcTimeout)
	resp, err := client.GossipRound(rpcCtx, &sporepb.GossipExchange{MetricsRootHash: myRoot[:]})
	cancel()
	if err != nil {
		return 0, err
	}
	if bytes.Equal(resp.GetMetricsRootHash(), myRoot[:]) {
		return 0, nil
	}

	frontier := []merkle.Path{merkle.Root()}
	for depth := uint32(0); depth < merkle.Depth && len(frontier) > 0; depth++ {
		reqPaths := make([]*sporepb.MerklePath, len(frontier))
		for i, p := range frontier {
			reqPaths[i] = &sporepb.MerklePath{Depth: p.Depth, PrefixBits: p.Prefix}
		}
		rpcCtx, cancel := context.WithTimeout(ctx, rpcTimeout)
		childResp, err := client.MerkleChildren(rpcCtx, &sporepb.MerkleChildrenRequest{
			Tree:  sporepb.TreeKind_METRICS,
			Paths: reqPaths,
		})
		cancel()
		if err != nil || len(childResp.GetNodes()) != len(frontier) {
			return 0, err
		}

		var next []merkle.Path
		for i, p := range frontier {
			leftPath, rightPath := p.Children()
			myLeft, myRight := myTree.ChildrenHashes(p)
			peer := childResp.GetNodes()[i]
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
		return 0, nil
	}

	groupPrefixes := make([]uint32, len(frontier))
	for i, p := range frontier {
		groupPrefixes[i] = p.Prefix
	}
	rpcCtx, cancel = context.WithTimeout(ctx, rpcTimeout)
	leafResp, err := client.MerkleLeaf(rpcCtx, &sporepb.MerkleLeafRequest{
		Tree:          sporepb.TreeKind_METRICS,
		GroupPrefixes: groupPrefixes,
	})
	cancel()
	if err != nil {
		return 0, err
	}

	var missing [][]byte
	for _, grp := range leafResp.GetGroups() {
		mine, err := s.store.LeafGroupIDs(storage.TreeMetrics, byte(grp.GetGroupPrefix()))
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
		return 0, nil
	}

	rpcCtx, cancel = context.WithTimeout(ctx, rpcTimeout)
	objResp, err := client.GetObjects(rpcCtx, &sporepb.GetObjectsRequest{Tree: sporepb.TreeKind_METRICS, ObjectIds: missing})
	cancel()
	if err != nil {
		return 0, err
	}

	for i, obj := range objResp.GetObjects() {
		if i >= len(missing) {
			break
		}
		// Metrics objects are encrypted, not owner-signed — only integrity
		// (hash match) is checked here, same as node's own ingest path.
		if !bytes.Equal(cryptoutil.ObjectID(obj.GetContent()), missing[i]) {
			continue
		}
		if _, err := s.store.PutObject(storage.TreeMetrics, missing[i], obj); err == nil {
			fetched++
		}
	}
	return fetched, nil
}
