package membership

import (
	"testing"
	"time"

	"sporemachine/internal/sporepb"
)

func TestMarkAliveOnNewAddress(t *testing.T) {
	m := New("self", nil)
	m.markAlive("a")

	c := m.Counts()
	if c.Alive != 1 || c.Suspect != 0 || c.Dead != 0 {
		t.Fatalf("Counts after markAlive on new address: %+v", c)
	}
}

func TestMarkSuspectNoopOnDead(t *testing.T) {
	m := New("self", nil)
	m.members["a"] = &entry{status: sporepb.MemberStatus_DEAD}

	m.markSuspect("a")

	c := m.Counts()
	if c.Dead != 1 || c.Suspect != 0 {
		t.Fatalf("markSuspect on a DEAD entry changed its status: %+v", c)
	}
}

func TestExpireSuspects(t *testing.T) {
	t.Run("not yet timed out stays SUSPECT", func(t *testing.T) {
		m := New("self", nil)
		m.members["a"] = &entry{status: sporepb.MemberStatus_SUSPECT, suspectAt: time.Now().Add(-1 * time.Second)}

		m.expireSuspects()

		c := m.Counts()
		if c.Suspect != 1 || c.Dead != 0 {
			t.Fatalf("expireSuspects fired before suspectTimeout elapsed: %+v", c)
		}
	})

	t.Run("timed out becomes DEAD", func(t *testing.T) {
		m := New("self", nil)
		m.members["a"] = &entry{status: sporepb.MemberStatus_SUSPECT, suspectAt: time.Now().Add(-31 * time.Second)}

		m.expireSuspects()

		c := m.Counts()
		if c.Dead != 1 || c.Suspect != 0 {
			t.Fatalf("expireSuspects did not expire a SUSPECT entry past suspectTimeout: %+v", c)
		}
	})
}

func TestApplyPiggybackSelfRefutation(t *testing.T) {
	m := New("self", nil)

	// A claim about self with incarnation >= our current one bumps ours past it.
	m.applyPiggyback([]*sporepb.MemberState{
		{Address: "self", Status: sporepb.MemberStatus_SUSPECT, Incarnation: 5},
	})
	if got := m.snapshotPiggyback()[0].GetIncarnation(); got != 6 {
		t.Fatalf("selfIncarnation after refutation = %d, want 6", got)
	}

	// A stale claim (lower incarnation than ours) does not bump it further.
	m.applyPiggyback([]*sporepb.MemberState{
		{Address: "self", Status: sporepb.MemberStatus_SUSPECT, Incarnation: 3},
	})
	if got := m.snapshotPiggyback()[0].GetIncarnation(); got != 6 {
		t.Fatalf("selfIncarnation after stale claim = %d, want unchanged 6", got)
	}
}

func TestApplyPiggybackPeer(t *testing.T) {
	cases := []struct {
		name            string
		incoming        *sporepb.MemberState
		wantStatus      sporepb.MemberStatus
		wantIncarnation int64
	}{
		{
			name:            "higher incarnation wins regardless of status",
			incoming:        &sporepb.MemberState{Address: "p", Status: sporepb.MemberStatus_SUSPECT, Incarnation: 3},
			wantStatus:      sporepb.MemberStatus_SUSPECT,
			wantIncarnation: 3,
		},
		{
			name:            "equal incarnation, more severe status wins",
			incoming:        &sporepb.MemberState{Address: "p", Status: sporepb.MemberStatus_SUSPECT, Incarnation: 2},
			wantStatus:      sporepb.MemberStatus_SUSPECT,
			wantIncarnation: 2,
		},
		{
			name:            "equal incarnation, less severe status loses",
			incoming:        &sporepb.MemberState{Address: "p", Status: sporepb.MemberStatus_ALIVE, Incarnation: 2},
			wantStatus:      sporepb.MemberStatus_SUSPECT, // existing, unchanged
			wantIncarnation: 2,
		},
		{
			name:            "lower incarnation loses even with a more severe status",
			incoming:        &sporepb.MemberState{Address: "p", Status: sporepb.MemberStatus_DEAD, Incarnation: 1},
			wantStatus:      sporepb.MemberStatus_SUSPECT, // existing, unchanged
			wantIncarnation: 2,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := New("self", nil)
			m.members["p"] = &entry{status: sporepb.MemberStatus_SUSPECT, incarnation: 2}

			m.applyPiggyback([]*sporepb.MemberState{c.incoming})

			e := m.members["p"]
			if e.status != c.wantStatus || e.incarnation != c.wantIncarnation {
				t.Fatalf("got status=%v incarnation=%d, want status=%v incarnation=%d",
					e.status, e.incarnation, c.wantStatus, c.wantIncarnation)
			}
		})
	}
}

func TestLearn(t *testing.T) {
	m := New("self", nil)
	m.members["known"] = &entry{status: sporepb.MemberStatus_SUSPECT}

	m.Learn([]string{"self", "known", "new"})

	if _, ok := m.members["self"]; ok {
		t.Fatal("Learn added selfAddress as a member")
	}
	if e := m.members["known"]; e.status != sporepb.MemberStatus_SUSPECT {
		t.Fatalf("Learn overwrote an already-known member's status: %v", e.status)
	}
	if e, ok := m.members["new"]; !ok || e.status != sporepb.MemberStatus_ALIVE {
		t.Fatal("Learn did not add the previously unknown address as ALIVE")
	}
}

func TestRandomAlivePeers(t *testing.T) {
	m := New("self", nil)
	m.members["a"] = &entry{status: sporepb.MemberStatus_ALIVE}
	m.members["b"] = &entry{status: sporepb.MemberStatus_ALIVE}
	m.members["c"] = &entry{status: sporepb.MemberStatus_SUSPECT}
	m.members["d"] = &entry{status: sporepb.MemberStatus_DEAD}

	for i := 0; i < 20; i++ {
		got := m.RandomAlivePeers(1)
		if len(got) > 1 {
			t.Fatalf("RandomAlivePeers(1) returned %d addresses", len(got))
		}
		for _, addr := range got {
			if e := m.members[addr]; e.status != sporepb.MemberStatus_ALIVE {
				t.Fatalf("RandomAlivePeers returned non-ALIVE address %q (%v)", addr, e.status)
			}
		}
	}

	if got := m.RandomAlivePeers(10); len(got) != 2 {
		t.Fatalf("RandomAlivePeers(10) with only 2 ALIVE members returned %d", len(got))
	}
}

func TestCounts(t *testing.T) {
	m := New("self", nil)
	m.members["a"] = &entry{status: sporepb.MemberStatus_ALIVE}
	m.members["b"] = &entry{status: sporepb.MemberStatus_ALIVE}
	m.members["c"] = &entry{status: sporepb.MemberStatus_SUSPECT}
	m.members["d"] = &entry{status: sporepb.MemberStatus_DEAD}

	c := m.Counts()
	if c.Alive != 2 || c.Suspect != 1 || c.Dead != 1 {
		t.Fatalf("Counts = %+v, want {Alive:2 Suspect:1 Dead:1}", c)
	}
}
