package node

import (
	"context"

	"sporemachine/internal/cryptoutil"
	"sporemachine/internal/sporepb"
)

// server implements sporepb.NodeServiceServer, delegating to the relevant
// part; kept separate from Node's own API (Run/Shutdown).
type server struct {
	*Node
	sporepb.UnimplementedNodeServiceServer
}

func (s *server) node() *Node { return s.Node }

func (s *server) SubmitData(_ context.Context, req *sporepb.SubmitDataRequest) (*sporepb.SubmitAck, error) {
	return s.node().gossip.HandleSubmitData(req), nil
}

func (s *server) SubmitTerminate(_ context.Context, req *sporepb.TerminateRequest) (*sporepb.TerminateAck, error) {
	n := s.node()
	msg := []byte(n.cfg.Genesis.GetNetworkId() + "TERMINATE")
	if !cryptoutil.VerifySignature(n.ownerPublicKey, msg, req.GetSignature()) {
		return &sporepb.TerminateAck{Received: false}, nil
	}

	n.terminateMu.Lock()
	already := n.terminateHandled
	n.terminateHandled = true
	n.terminateMu.Unlock()

	if !already {
		go n.initiateTermination(req)
	}
	return &sporepb.TerminateAck{Received: true}, nil
}

func (s *server) Ping(_ context.Context, req *sporepb.PingRequest) (*sporepb.PingResponse, error) {
	return s.node().mem.HandlePing(req), nil
}

func (s *server) PingIndirect(ctx context.Context, req *sporepb.IndirectPingRequest) (*sporepb.IndirectPingResponse, error) {
	acked := s.node().mem.HandleIndirectPing(ctx, req.GetTargetAddress())
	return &sporepb.IndirectPingResponse{Acked: acked}, nil
}

func (s *server) GossipRound(_ context.Context, req *sporepb.GossipExchange) (*sporepb.GossipExchange, error) {
	return s.node().gossip.HandleGossipRound(req), nil
}

func (s *server) MerkleChildren(_ context.Context, req *sporepb.MerkleChildrenRequest) (*sporepb.MerkleChildrenResponse, error) {
	return s.node().gossip.HandleMerkleChildren(req), nil
}

func (s *server) MerkleLeaf(_ context.Context, req *sporepb.MerkleLeafRequest) (*sporepb.MerkleLeafResponse, error) {
	return s.node().gossip.HandleMerkleLeaf(req), nil
}

func (s *server) GetObjects(_ context.Context, req *sporepb.GetObjectsRequest) (*sporepb.GetObjectsResponse, error) {
	return s.node().gossip.HandleGetObjects(req), nil
}

var _ sporepb.NodeServiceServer = (*server)(nil)
