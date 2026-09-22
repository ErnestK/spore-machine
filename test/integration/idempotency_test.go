package integration_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// Scenario 9 (Integration #9, SRS 009 addendum): a byte-identical resubmission
// of a command already executed once is not re-executed — object_id dedup at
// the storage layer means the second SubmitData is treated as "not new" and
// never reaches command execution again.
func TestIdenticalCommandResubmissionDoesNotReexecute(t *testing.T) {
	owner := newTestOwner(t)
	genesis, _, _ := newGenesis(t, owner, "test-net")

	a := spinNode(t, genesis, "a", nil)

	logFile := filepath.Join(t.TempDir(), "ran-once.log")
	req := signedCommand(t, owner, "echo ran >> '"+logFile+"'")

	client, closeConn := dial(t, a.Address())
	defer closeConn()

	submit := func() (accepted bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		ack, err := client.SubmitData(ctx, req)
		if err != nil {
			t.Fatalf("SubmitData: %v", err)
		}
		return ack.GetAccepted()
	}

	if !submit() {
		t.Fatalf("first submission should be accepted")
	}
	eventually(t, 3*time.Second, func() bool { return lineCount(logFile) >= 1 }, "command should have executed once")

	// Identical content — same object_id, already in storage.
	if !submit() {
		t.Fatalf("resubmission should still be Accepted:true (it's a valid, already-known object, not rejected) per SubmitAck semantics")
	}

	// Give a wrongly-re-executed command time to show up, then assert it didn't.
	time.Sleep(500 * time.Millisecond)
	if got := lineCount(logFile); got != 1 {
		t.Fatalf("expected exactly 1 execution even after identical resubmission, got %d", got)
	}
}
