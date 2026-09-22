package integration_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sporemachine/internal/sporepb"
)

// Scenario 1 (structure/test_plan.md Integration #1): a signed Command
// submitted to node A propagates to B and C over the gossip/Merkle round,
// and each node independently re-verifies the signature and executes the
// command in its own goroutine — not just A. Observed via a shared file: the
// bash command appends one line per node that actually ran it (each run is a
// real subprocess, so N nodes running it gives N lines).
func TestCommandPropagatesAndExecutesOnEveryNode(t *testing.T) {
	owner := newTestOwner(t)
	genesis, _, _ := newGenesis(t, owner, "test-net")

	a := spinNode(t, genesis, "a", nil)
	b := spinNode(t, genesis, "b", []string{a.Address()})
	c := spinNode(t, genesis, "c", []string{a.Address()})

	logFile := filepath.Join(t.TempDir(), "ran.log")
	cmd := fmt.Sprintf("echo ran >> '%s'", logFile)

	client, closeConn := dial(t, a.Address())
	defer closeConn()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ack, err := client.SubmitData(ctx, signedCommand(t, owner, cmd))
	if err != nil {
		t.Fatalf("SubmitData: %v", err)
	}
	if !ack.GetAccepted() {
		t.Fatalf("expected command accepted by A")
	}

	// A executes directly from the SubmitData ingest path.
	eventually(t, 3*time.Second, func() bool { return lineCount(logFile) >= 1 }, "A should have executed the command")

	// B and C only have A as a seed; trigger their rounds so they pull it.
	b.TriggerGossipRound()
	c.TriggerGossipRound()

	eventually(t, 5*time.Second, func() bool { return lineCount(logFile) >= 3 },
		"expected 3 executions (one per node), got %d", lineCount(logFile))

	if got := lineCount(logFile); got != 3 {
		t.Fatalf("expected exactly 3 executions (A, B, C each once), got %d", got)
	}
}

// Scenario 2 (Integration #2): a FILE-type object is stored and gossiped
// like data, but never handed to command execution — even if its content
// happens to be a validly-marshaled Command. Verified negatively: the
// content's bash command (which would leave an observable side effect if
// ever executed) never runs, even after the object has fully propagated.
func TestFileObjectIsNeverExecuted(t *testing.T) {
	owner := newTestOwner(t)
	genesis, _, _ := newGenesis(t, owner, "test-net")

	a := spinNode(t, genesis, "a", nil)
	b := spinNode(t, genesis, "b", []string{a.Address()})

	logFile := filepath.Join(t.TempDir(), "should-not-exist.log")
	dangerousContent := marshalCommandContent(t, fmt.Sprintf("echo executed >> '%s'", logFile))

	client, closeConn := dial(t, a.Address())
	defer closeConn()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req := signedFile(t, owner, dangerousContent)
	ack, err := client.SubmitData(ctx, req)
	if err != nil {
		t.Fatalf("SubmitData: %v", err)
	}
	if !ack.GetAccepted() {
		t.Fatalf("expected FILE accepted by A")
	}

	// Confirm it really propagates (rules out "never executed because never
	// even arrived" as a false-positive pass).
	b.TriggerGossipRound()
	objID := ack.GetObjectId()
	eventually(t, 5*time.Second, func() bool {
		return objectExistsOn(t, b.Address(), sporepb.TreeKind_DATA, objID)
	}, "FILE object should have propagated to B")

	// Give any (incorrect) execution path a moment it would need, then
	// assert the side effect never happened on either node.
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(logFile); err == nil {
		t.Fatalf("FILE content was executed as a command — should never happen")
	}
}

func lineCount(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n := 0
	for _, c := range b {
		if c == '\n' {
			n++
		}
	}
	return n
}
