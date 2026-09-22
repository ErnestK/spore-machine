package integration_test

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	"sporemachine/internal/cryptoutil"
	"sporemachine/internal/sporepb"
)

// fakePeer is a minimal stand-in NodeService that only implements
// SubmitTerminate, counting how many times it's called — used to observe
// fan-out behavior without needing to read a real Node's private state.
// ackDelay optionally delays the response, to widen the window during which
// the real node under test hasn't shut down yet (its Shutdown only runs
// after every neighbor's ack has arrived).
type fakePeer struct {
	sporepb.UnimplementedNodeServiceServer
	ackDelay time.Duration
	mu       sync.Mutex
	calls    int
}

func (p *fakePeer) SubmitTerminate(_ context.Context, _ *sporepb.TerminateRequest) (*sporepb.TerminateAck, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	if p.ackDelay > 0 {
		time.Sleep(p.ackDelay)
	}
	return &sporepb.TerminateAck{Received: true}, nil
}

func (p *fakePeer) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func startFakePeer(t *testing.T, ackDelay time.Duration) (addr string, peer *fakePeer) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	peer = &fakePeer{ackDelay: ackDelay}
	srv := grpc.NewServer()
	sporepb.RegisterNodeServiceServer(srv, peer)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String(), peer
}

func signedTerminate(t *testing.T, owner testOwner, networkID string) *sporepb.TerminateRequest {
	t.Helper()
	msg := []byte(networkID + "TERMINATE")
	return &sporepb.TerminateRequest{Signature: cryptoutil.Sign(owner.priv, msg)}
}

// Scenario 6 (Integration #6): a valid TerminateRequest sent to node X fans
// out to every known neighbor via direct SubmitTerminate calls (not a random
// gossip subset), waiting for a TerminateAck from each before X itself
// shuts down.
func TestTerminateFansOutToEveryNeighbor(t *testing.T) {
	owner := newTestOwner(t)
	genesis, _, _ := newGenesis(t, owner, "test-net")

	peer1Addr, peer1 := startFakePeer(t, 0)
	peer2Addr, peer2 := startFakePeer(t, 0)

	x := spinNode(t, genesis, "x", []string{peer1Addr, peer2Addr})

	client, closeConn := dial(t, x.Address())
	defer closeConn()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ack, err := client.SubmitTerminate(ctx, signedTerminate(t, owner, genesis.GetNetworkId()))
	if err != nil {
		t.Fatalf("SubmitTerminate: %v", err)
	}
	if !ack.GetReceived() {
		t.Fatalf("expected Received:true for a validly signed request")
	}

	eventually(t, 3*time.Second, func() bool { return peer1.Calls() == 1 && peer2.Calls() == 1 },
		"both neighbors should each receive exactly one SubmitTerminate call (got %d, %d)", peer1.Calls(), peer2.Calls())

	// X should have shut down after fanning out — a subsequent RPC must fail.
	eventually(t, 3*time.Second, func() bool {
		pingCtx, pingCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer pingCancel()
		_, err := client.Ping(pingCtx, &sporepb.PingRequest{FromAddress: "probe"})
		return err != nil
	}, "X should have shut down after the fan-out completed")
}

// Scenario 7 (Integration #7): a node that already processed one
// TerminateRequest replies TerminateAck{received:true} again on a repeat
// delivery, without re-broadcasting to its neighbors a second time.
//
// X's Shutdown only runs after every neighbor has acked (initiateTermination
// waits on the whole fan-out), so the redelivery must land inside that
// window to actually exercise the idempotency flag rather than just hitting
// a server that's already gone — the fake neighbor here delays its own ack
// to make that window deterministic instead of racy.
func TestRepeatTerminateDoesNotRebroadcast(t *testing.T) {
	owner := newTestOwner(t)
	genesis, _, _ := newGenesis(t, owner, "test-net")

	peerAddr, peer := startFakePeer(t, 800*time.Millisecond)
	x := spinNode(t, genesis, "x", []string{peerAddr})

	client, closeConn := dial(t, x.Address())
	defer closeConn()
	req := signedTerminate(t, owner, genesis.GetNetworkId())

	send := func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		ack, err := client.SubmitTerminate(ctx, req)
		return err == nil && ack.GetReceived()
	}

	if !send() {
		t.Fatalf("first SubmitTerminate should be Received:true")
	}
	eventually(t, 500*time.Millisecond, func() bool { return peer.Calls() >= 1 },
		"fan-out to the neighbor should have started")

	// Redeliver while X is still inside initiateTermination (waiting on the
	// neighbor's delayed ack) — terminateHandled should already be true.
	if !send() {
		t.Fatalf("redelivered SubmitTerminate should still be Received:true")
	}

	// Let the delayed ack land and X finish shutting down, then check the
	// neighbor was only ever fanned out to once, not twice.
	time.Sleep(1200 * time.Millisecond)
	if got := peer.Calls(); got != 1 {
		t.Fatalf("neighbor should have exactly 1 fan-out call, got %d — indicates re-broadcast on redelivery", got)
	}
}
