// Package spawncontrol decides whether to agree to norn's spawn proposal.
package spawncontrol

import (
	"context"
	"slices"
	"time"

	"sporemachine/internal/membership"
	"sporemachine/internal/spawnproto"
	"sporemachine/internal/sporepb"
)

const (
	defaultBatchSize = 5
	defaultPauseFor  = 15 * time.Minute
)

type Controller struct {
	genesis       *sporepb.Genesis
	ownGeneration int32
	mem           *membership.Membership

	batch       []string // addresses of this batch's spawned children
	pausedSince time.Time
}

func New(genesis *sporepb.Genesis, ownGeneration int32, mem *membership.Membership) *Controller {
	return &Controller{genesis: genesis, ownGeneration: ownGeneration, mem: mem}
}

func (c *Controller) batchSize() int {
	if n := c.genesis.GetSpawnRules().GetMaxChildrenPerBatch(); n > 0 {
		return int(n)
	}
	return defaultBatchSize
}

func (c *Controller) pauseFor() time.Duration {
	if s := c.genesis.GetSpawnRules().GetBatchPauseSeconds(); s > 0 {
		return time.Duration(s) * time.Second
	}
	return defaultPauseFor
}

// Run listens on both channels from norn in one select: no dedicated timer
// for the pause, it's checked lazily against wall-clock time whenever the
// next SpawnProposal arrives.
func (c *Controller) Run(ctx context.Context, proposals <-chan spawnproto.SpawnProposal, completions <-chan spawnproto.SpawnCompleted) {
	for {
		select {
		case <-ctx.Done():
			return
		case p := <-proposals:
			decision := c.decide()
			select {
			case p.Reply <- decision:
			case <-ctx.Done():
			}
		case done := <-completions:
			c.batch = append(c.batch, done.Address)
			if len(c.batch) >= c.batchSize() {
				c.pausedSince = time.Now()
			}
		}
	}
}

func (c *Controller) decide() spawnproto.SpawnDecision {
	if len(c.batch) >= c.batchSize() {
		if time.Since(c.pausedSince) < c.pauseFor() {
			return spawnproto.SpawnDecision{Agree: false}
		}
		if c.anyBatchMemberAlive() {
			c.pausedSince = time.Now() // still occupied, recheck again after another full pause
			return spawnproto.SpawnDecision{Agree: false}
		}
		c.batch = nil // all of the previous batch is gone, room for a new one
	}

	candidateGeneration := c.ownGeneration + 1
	// max_generation_depth == 0 means "unset", treated as no limit.
	if maxDepth := c.genesis.GetSpawnRules().GetMaxGenerationDepth(); maxDepth > 0 && candidateGeneration > maxDepth {
		return spawnproto.SpawnDecision{Agree: false}
	}
	return spawnproto.SpawnDecision{Agree: true, SeedContacts: c.mem.AllAlivePeers()}
}

func (c *Controller) anyBatchMemberAlive() bool {
	return slices.ContainsFunc(c.batch, c.mem.IsAlive)
}
