// Package spawnproto holds the in-process channel types between norn and
// node-spawn-control.
package spawnproto

type SpawnProposal struct {
	CandidateID string
	Reply       chan SpawnDecision
}

type SpawnDecision struct {
	Agree        bool
	SeedContacts []string
}

// SpawnCompleted is a fire-and-forget notification: norn already knows the
// child's address (it picked the port before exec) and doesn't wait for a
// reply.
type SpawnCompleted struct {
	CandidateID string
	Address     string
}
