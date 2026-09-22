package integration_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"sporemachine/internal/sporepb"
)

// Scenario 3 (Integration #3): node B starts empty, A holds N objects; B's
// gossip round detects the data_root_hash mismatch and pulls the missing
// objects via the batched Merkle walk (GossipExchange never carries a full
// ID list — the proto only has root hashes + known_peers, structurally).
//
// Note vs. the plan's "converges over several rounds" wording: reading
// gossipsync.go, one round's syncTree call already walks every level
// synchronously inside that one call — a single TriggerGossipRound is
// sufficient to fully converge, not "several 30s ticks". Tested as such.
func TestMerkleSyncConvergesFromEmpty(t *testing.T) {
	owner := newTestOwner(t)
	genesis, _, _ := newGenesis(t, owner, "test-net")

	a := spinNode(t, genesis, "a", nil)
	b := spinNode(t, genesis, "b", []string{a.Address()})

	const n = 12
	ids := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		ids = append(ids, submitFile(t, a.Address(), owner, fmt.Sprintf("payload-%d", i)))
	}

	b.TriggerGossipRound()

	eventually(t, 5*time.Second, func() bool {
		for _, id := range ids {
			if !objectExistsOn(t, b.Address(), sporepb.TreeKind_DATA, id) {
				return false
			}
		}
		return true
	}, "B should have pulled all %d objects from A via Merkle sync", n)
}

// Scenario 4 (Integration #4): A initiates a round with B while A also lacks
// objects B has; within that one exchange only the initiator (A) downloads —
// the responder (B) does not push its extra objects back. Confirmed by
// reading gossipsync.go: HandleGossipRound only ever answers with the
// responder's own state, it never itself dials out.
func TestOnlyInitiatorPullsInAnExchange(t *testing.T) {
	owner := newTestOwner(t)
	genesis, _, _ := newGenesis(t, owner, "test-net")

	b := spinNode(t, genesis, "b", nil)
	a := spinNode(t, genesis, "a", []string{b.Address()}) // A knows B from construction

	// Give A one exclusive object, B a different exclusive object.
	aOnly := submitFile(t, a.Address(), owner, "a-only-payload")
	bOnly := submitFile(t, b.Address(), owner, "b-only-payload")

	a.TriggerGossipRound() // A initiates against B

	eventually(t, 3*time.Second, func() bool {
		return objectExistsOn(t, a.Address(), sporepb.TreeKind_DATA, bOnly)
	}, "A (initiator) should have pulled B's object")

	// B (responder in that same exchange) must NOT have pulled A's object.
	if objectExistsOn(t, b.Address(), sporepb.TreeKind_DATA, aOnly) {
		t.Fatalf("B (responder) pulled A's object in the same exchange — should only happen if B later initiates its own round")
	}
}

func submitFile(t *testing.T, addr string, owner testOwner, payload string) []byte {
	t.Helper()
	client, closeConn := dial(t, addr)
	defer closeConn()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ack, err := client.SubmitData(ctx, signedFile(t, owner, []byte(payload)))
	if err != nil || !ack.GetAccepted() {
		t.Fatalf("submitFile(%s): err=%v accepted=%v", addr, err, ack.GetAccepted())
	}
	return ack.GetObjectId()
}
