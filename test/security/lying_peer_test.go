package security_test

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"

	"sporemachine/internal/sporepb"
)

// lyingPeer is a fake NodeService that always claims its data tree diverges,
// then hands back one object whose content doesn't actually hash to the id
// it claimed during leaf listing — simulating a malicious/buggy peer during
// Merkle sync (see TestMerkleSync_ContentHashMismatch_Dropped).
type lyingPeer struct {
	sporepb.UnimplementedNodeServiceServer
	fakeID      []byte
	fakeContent []byte
	addr        string
	server      *grpc.Server
}

func newLyingPeer(t *testing.T, fakeID, fakeContent []byte) *lyingPeer {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("lyingPeer listen: %v", err)
	}
	p := &lyingPeer{fakeID: fakeID, fakeContent: fakeContent, addr: lis.Addr().String(), server: grpc.NewServer()}
	sporepb.RegisterNodeServiceServer(p.server, p)
	go func() { _ = p.server.Serve(lis) }()
	return p
}

func (p *lyingPeer) stop() { p.server.Stop() }

// GossipRound always claims a data root that won't match an honest peer's,
// forcing the caller into a Merkle walk against us.
func (p *lyingPeer) GossipRound(_ context.Context, _ *sporepb.GossipExchange) (*sporepb.GossipExchange, error) {
	lie := make([]byte, 32)
	for i := range lie {
		lie[i] = 0xFF
	}
	return &sporepb.GossipExchange{DataRootHash: lie, MetricsRootHash: lie}, nil
}

// MerkleChildren claims every path's children differ too, so the walk
// descends all the way to the leaves instead of stopping early.
func (p *lyingPeer) MerkleChildren(_ context.Context, req *sporepb.MerkleChildrenRequest) (*sporepb.MerkleChildrenResponse, error) {
	nodes := make([]*sporepb.MerkleNodeHashes, len(req.GetPaths()))
	for i := range nodes {
		nodes[i] = &sporepb.MerkleNodeHashes{LeftHash: []byte("lie-left"), RightHash: []byte("lie-right")}
	}
	return &sporepb.MerkleChildrenResponse{Nodes: nodes}, nil
}

// MerkleLeaf always reports our one fake id, regardless of which group
// prefixes were actually asked for.
func (p *lyingPeer) MerkleLeaf(_ context.Context, _ *sporepb.MerkleLeafRequest) (*sporepb.MerkleLeafResponse, error) {
	return &sporepb.MerkleLeafResponse{Groups: []*sporepb.MerkleLeafGroup{
		{GroupPrefix: uint32(p.fakeID[0]), ObjectIds: [][]byte{p.fakeID}},
	}}, nil
}

// GetObjects hands back content that does not hash to fakeID — the lie the
// victim's ingest() hash check is supposed to catch.
func (p *lyingPeer) GetObjects(_ context.Context, _ *sporepb.GetObjectsRequest) (*sporepb.GetObjectsResponse, error) {
	return &sporepb.GetObjectsResponse{Objects: []*sporepb.StoredObject{
		{Type: sporepb.DataType_FILE, Content: p.fakeContent},
	}}, nil
}
