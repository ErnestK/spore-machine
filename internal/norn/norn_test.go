package norn

import (
	"context"
	"testing"
	"time"

	"sporemachine/internal/spawnproto"
	"sporemachine/internal/sporepb"
)

func TestSpawnIntervalFallsBackToDefault(t *testing.T) {
	tests := []struct {
		name    string
		seconds int32
		want    time.Duration
	}{
		{"unset (0) falls back to 10 minutes", 0, 10 * time.Minute},
		{"negative falls back to 10 minutes", -5, 10 * time.Minute},
		{"positive value used as-is", 30, 30 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			genesis := &sporepb.Genesis{SpawnRules: &sporepb.SpawnRules{SpawnIntervalSeconds: tt.seconds}}
			if got := spawnInterval(genesis); got != tt.want {
				t.Errorf("spawnInterval() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSpawnRejectsUnsupportedMode(t *testing.T) {
	n := &Norn{genesis: &sporepb.Genesis{SpawnRules: &sporepb.SpawnRules{SpawnMode: sporepb.SpawnMode_DOCKER}}}

	_, err := n.spawn("candidate", 1, nil)
	if err == nil {
		t.Fatal("spawn() with SpawnMode_DOCKER returned nil error, want an error (not implemented in v0)")
	}
}

func TestSpawnFailsWhenFreePortFails(t *testing.T) {
	original := freePort
	defer func() { freePort = original }()

	wantErr := errFreePortForced{}
	freePort = func() (int, error) { return 0, wantErr }

	n := &Norn{genesis: &sporepb.Genesis{}} // SpawnMode defaults to PROCESS (0)

	_, err := n.spawn("candidate", 1, nil)
	if err == nil {
		t.Fatal("spawn() with a failing freePort returned nil error, want an error")
	}
}

type errFreePortForced struct{}

func (errFreePortForced) Error() string { return "forced free port failure for test" }

// TestProposeAgreeFalseSendsNothing verifies that when node-spawn-control
// declines, propose() never touches the completions channel and returns.
func TestProposeAgreeFalseSendsNothing(t *testing.T) {
	proposals := make(chan spawnproto.SpawnProposal)
	completions := make(chan spawnproto.SpawnCompleted)
	n := &Norn{genesis: &sporepb.Genesis{}, proposals: proposals, completions: completions}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		p := <-proposals
		p.Reply <- spawnproto.SpawnDecision{Agree: false}
	}()

	done := make(chan struct{})
	go func() { n.propose(ctx); close(done) }()

	select {
	case <-completions:
		t.Fatal("propose() sent a SpawnCompleted after Agree:false")
	case <-done:
		// propose returned without sending anything on completions — expected.
	case <-time.After(time.Second):
		t.Fatal("propose() did not return in time")
	}
}

// TestProposeSpawnErrorSendsNothing verifies that when spawn() fails (here,
// via an unsupported SpawnMode), propose() doesn't crash and sends nothing.
func TestProposeSpawnErrorSendsNothing(t *testing.T) {
	proposals := make(chan spawnproto.SpawnProposal)
	completions := make(chan spawnproto.SpawnCompleted)
	n := &Norn{
		genesis:     &sporepb.Genesis{SpawnRules: &sporepb.SpawnRules{SpawnMode: sporepb.SpawnMode_DOCKER}},
		proposals:   proposals,
		completions: completions,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		p := <-proposals
		p.Reply <- spawnproto.SpawnDecision{Agree: true}
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		n.propose(ctx) // must not panic even though spawn() will fail
	}()

	select {
	case <-completions:
		t.Fatal("propose() sent a SpawnCompleted despite spawn() failing")
	case <-done:
		// returned cleanly — expected.
	case <-time.After(time.Second):
		t.Fatal("propose() did not return in time")
	}
}

// TestProposeSendProposalRespectsCancel: nobody reads from proposals and ctx
// is already cancelled — propose() must return promptly, not block forever.
func TestProposeSendProposalRespectsCancel(t *testing.T) {
	proposals := make(chan spawnproto.SpawnProposal) // unbuffered, no reader
	completions := make(chan spawnproto.SpawnCompleted)
	n := &Norn{genesis: &sporepb.Genesis{}, proposals: proposals, completions: completions}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already done before propose() even starts

	done := make(chan struct{})
	go func() { n.propose(ctx); close(done) }()

	select {
	case <-done:
		// returned promptly — expected.
	case <-time.After(time.Second):
		t.Fatal("propose() blocked on sending SpawnProposal despite cancelled ctx")
	}
}

// TestProposeSendCompletionRespectsCancel: decision is Agree:true and spawn()
// succeeds, but nobody reads from completions; cancelling ctx after the
// decision is delivered must still let propose() return rather than block
// forever on the final send.
func TestProposeSendCompletionRespectsCancel(t *testing.T) {
	proposals := make(chan spawnproto.SpawnProposal)
	completions := make(chan spawnproto.SpawnCompleted) // unbuffered, no reader
	n := &Norn{genesis: &sporepb.Genesis{}, proposals: proposals, completions: completions, nodeBinary: "true"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		p := <-proposals
		p.Reply <- spawnproto.SpawnDecision{Agree: true}
	}()

	done := make(chan struct{})
	go func() { n.propose(ctx); close(done) }()

	// Give propose() time to receive the decision, call spawn() (fast: just
	// starts and lets "true" exit on its own) and reach the final blocking
	// send on completions, then cancel so that send has somewhere to return to.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// returned after cancellation — expected.
	case <-time.After(time.Second):
		t.Fatal("propose() blocked on sending SpawnCompleted despite cancelled ctx")
	}
}
