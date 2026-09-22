package spawncontrol

import (
	"context"
	"testing"
	"time"

	"sporemachine/internal/membership"
	"sporemachine/internal/spawnproto"
	"sporemachine/internal/sporepb"
)

func newTestMembership(alive ...string) *membership.Membership {
	return membership.New("self", alive)
}

func TestDecideAgreesWithinDepthAndBatch(t *testing.T) {
	mem := newTestMembership("peer1", "peer2")
	c := &Controller{genesis: &sporepb.Genesis{}, ownGeneration: 0, mem: mem}

	got := c.decide()
	if !got.Agree {
		t.Fatalf("decide() = %+v, want Agree:true", got)
	}
	if len(got.SeedContacts) != len(mem.AllAlivePeers()) {
		t.Fatalf("SeedContacts = %v, want all alive peers %v", got.SeedContacts, mem.AllAlivePeers())
	}
}

func TestDecideRejectsBeyondMaxGenerationDepth(t *testing.T) {
	genesis := &sporepb.Genesis{SpawnRules: &sporepb.SpawnRules{MaxGenerationDepth: 2}}
	mem := newTestMembership()
	c := &Controller{genesis: genesis, ownGeneration: 2, mem: mem} // candidate generation = 3 > 2

	got := c.decide()
	if got.Agree {
		t.Fatalf("decide() = %+v, want Agree:false (candidate generation exceeds max depth)", got)
	}
}

func TestDecideMaxGenerationDepthZeroMeansUnlimited(t *testing.T) {
	genesis := &sporepb.Genesis{SpawnRules: &sporepb.SpawnRules{MaxGenerationDepth: 0}}
	mem := newTestMembership()
	c := &Controller{genesis: genesis, ownGeneration: 50, mem: mem}

	got := c.decide()
	if !got.Agree {
		t.Fatalf("decide() = %+v, want Agree:true (max_generation_depth==0 means no limit)", got)
	}
}

func TestDecideBatchFullPauseNotElapsed(t *testing.T) {
	mem := newTestMembership()
	c := &Controller{
		genesis:     &sporepb.Genesis{},
		mem:         mem,
		batch:       []string{"c1", "c2", "c3", "c4", "c5"},
		pausedSince: time.Now(), // pause just started, nowhere near 15m default
	}

	got := c.decide()
	if got.Agree {
		t.Fatalf("decide() = %+v, want Agree:false (pause not elapsed)", got)
	}
}

func TestDecideBatchFullPauseElapsedNoChildrenAlive(t *testing.T) {
	mem := newTestMembership() // none of the batch addresses are alive
	c := &Controller{
		genesis:     &sporepb.Genesis{},
		mem:         mem,
		batch:       []string{"c1", "c2", "c3", "c4", "c5"},
		pausedSince: time.Now().Add(-16 * time.Minute), // elapsed
	}

	got := c.decide()
	if !got.Agree {
		t.Fatalf("decide() = %+v, want Agree:true (pause elapsed, no batch member alive -> fresh evaluation)", got)
	}
	if c.batch != nil {
		t.Fatalf("batch = %v, want cleared after pause elapsed with nothing alive", c.batch)
	}
}

func TestDecideBatchFullPauseElapsedOneChildStillAlive(t *testing.T) {
	mem := newTestMembership("c3") // one batch member still alive
	pausedSince := time.Now().Add(-16 * time.Minute)
	c := &Controller{
		genesis:     &sporepb.Genesis{},
		mem:         mem,
		batch:       []string{"c1", "c2", "c3", "c4", "c5"},
		pausedSince: pausedSince,
	}

	got := c.decide()
	if got.Agree {
		t.Fatalf("decide() = %+v, want Agree:false (a batch member is still alive, pause extends)", got)
	}
	if !c.pausedSince.After(pausedSince) {
		t.Fatalf("pausedSince = %v, want reset to a later time when pause extends", c.pausedSince)
	}
	if len(c.batch) != 5 {
		t.Fatalf("batch = %v, want unchanged (still occupied)", c.batch)
	}
}

func TestBatchSizeDefaultsAndOverride(t *testing.T) {
	tests := []struct {
		name string
		rule int32
		want int
	}{
		{"unset falls back to default", 0, defaultBatchSize},
		{"genesis value used when set", 7, 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Controller{genesis: &sporepb.Genesis{SpawnRules: &sporepb.SpawnRules{MaxChildrenPerBatch: tt.rule}}}
			if got := c.batchSize(); got != tt.want {
				t.Errorf("batchSize() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestPauseForDefaultsAndOverride(t *testing.T) {
	tests := []struct {
		name string
		rule int32
		want time.Duration
	}{
		{"unset falls back to default", 0, defaultPauseFor},
		{"genesis value used when set", 60, 60 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Controller{genesis: &sporepb.Genesis{SpawnRules: &sporepb.SpawnRules{BatchPauseSeconds: tt.rule}}}
			if got := c.pauseFor(); got != tt.want {
				t.Errorf("pauseFor() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestRunStartsPauseExactlyAtBatchSize drives the real channel-based Run loop
// (not private state directly) to verify the pause clock starts the moment
// the batch first reaches batchSize(), not before.
func TestRunStartsPauseExactlyAtBatchSize(t *testing.T) {
	genesis := &sporepb.Genesis{SpawnRules: &sporepb.SpawnRules{MaxChildrenPerBatch: 2}}
	mem := newTestMembership()
	c := New(genesis, 0, mem)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proposals := make(chan spawnproto.SpawnProposal)
	completions := make(chan spawnproto.SpawnCompleted)
	go c.Run(ctx, proposals, completions)

	send := func(addr string) {
		select {
		case completions <- spawnproto.SpawnCompleted{Address: addr}:
		case <-time.After(time.Second):
			t.Fatal("timed out sending SpawnCompleted")
		}
	}
	ask := func() spawnproto.SpawnDecision {
		reply := make(chan spawnproto.SpawnDecision, 1)
		select {
		case proposals <- spawnproto.SpawnProposal{Reply: reply}:
		case <-time.After(time.Second):
			t.Fatal("timed out sending SpawnProposal")
		}
		select {
		case d := <-reply:
			return d
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for SpawnDecision")
			return spawnproto.SpawnDecision{}
		}
	}

	send("c1") // batch=1, below batchSize(2) — pause must not have started yet
	if got := ask(); !got.Agree {
		t.Fatalf("after 1/2 children, decide() = %+v, want Agree:true (pause not yet active)", got)
	}

	send("c2") // batch=2 == batchSize(2) — pause starts now
	if got := ask(); got.Agree {
		t.Fatalf("after 2/2 children (batch just filled), decide() = %+v, want Agree:false (pause active)", got)
	}
}
