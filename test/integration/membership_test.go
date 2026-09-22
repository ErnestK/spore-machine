package integration_test

import (
	"testing"
	"time"
)

// Scenario 5 (Integration #5): a freshly spawned node C, given seed_contacts
// = [A], joins by pinging A with plain SWIM (no dedicated join RPC exists —
// membership.tick's normal ping is all it takes). C's address then reaches a
// third node D — which never had C as a seed — only later, through the
// known_peers field of a gossip round between D and A.
func TestNewNodeJoinsViaSwimThenSpreadsViaGossip(t *testing.T) {
	owner := newTestOwner(t)
	genesis, _, _ := newGenesis(t, owner, "test-net")

	a := spinNode(t, genesis, "a", nil)
	d := spinNode(t, genesis, "d", []string{a.Address()}) // D knows only A, never C
	c := spinNode(t, genesis, "c", []string{a.Address()}) // C's only seed is A

	// Step 1: SWIM. C pings A on its own 5s ticker; no gossip round or extra
	// seam needed — real production interval, just bounded by a generous
	// timeout since we can't shorten membership's ticker without a seam.
	eventually(t, 12*time.Second, func() bool { return a.KnowsAlive(c.Address()) },
		"A should learn C is alive via C's own SWIM ping, within a couple of 5s ticks")

	// D must not know C yet — it was never given C as a seed and no gossip
	// round has happened.
	if d.KnowsAlive(c.Address()) {
		t.Fatalf("D should not know C yet — no gossip round has carried known_peers")
	}

	// Step 2: peer exchange. D initiates a round against A; A's known_peers
	// now includes C, so D learns C's address from that exchange alone.
	d.TriggerGossipRound()

	eventually(t, 3*time.Second, func() bool { return d.KnowsAlive(c.Address()) },
		"D should learn C's address via A's known_peers in a gossip round")
}
