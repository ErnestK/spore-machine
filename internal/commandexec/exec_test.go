package commandexec

import (
	"context"
	"testing"
	"time"
)

// waitFor polls cond until it returns true or the deadline elapses.
func waitFor(t *testing.T, deadline time.Duration, cond func() bool) {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("condition not met within %s", deadline)
	}
}

func TestSubmitIncrementsStoredSynchronously(t *testing.T) {
	e := New()
	e.Submit(context.Background(), "true")
	if got := e.Stats().Stored; got != 1 {
		t.Fatalf("Stats().Stored right after Submit = %d, want 1 (must be synchronous)", got)
	}
}

func TestSubmitSucceededVsFailed(t *testing.T) {
	e := New()
	e.Submit(context.Background(), "true")
	e.Submit(context.Background(), "false")

	waitFor(t, 2*time.Second, func() bool {
		st := e.Stats()
		return st.Succeeded+st.Failed == 2
	})

	st := e.Stats()
	if st.Succeeded != 1 {
		t.Errorf("Succeeded = %d, want 1", st.Succeeded)
	}
	if st.Failed != 1 {
		t.Errorf("Failed = %d, want 1", st.Failed)
	}
}

func TestSubmitContextCancellationKillsRunningCommand(t *testing.T) {
	e := New()
	ctx, cancel := context.WithCancel(context.Background())
	e.Submit(ctx, "sleep 5")

	start := time.Now()
	time.Sleep(100 * time.Millisecond)
	cancel()

	waitFor(t, 2*time.Second, func() bool {
		return e.Stats().Failed+e.Stats().Succeeded == 1
	})
	elapsed := time.Since(start)
	if elapsed >= 4*time.Second {
		t.Errorf("command took %s to finish after cancel, expected it to be killed well before the 5s sleep completed", elapsed)
	}
}

func TestStatsOnZeroExecutionsIsZeroed(t *testing.T) {
	e := New()
	st := e.Stats()
	if st.ExecMsMax != 0 || st.ExecMsMin != 0 || st.ExecMsAvg != 0 || st.ExecMsMedian != 0 {
		t.Errorf("Stats() on zero executions = %+v, want all-zero exec fields", st)
	}
}

func TestStatsTimingRoughOrdering(t *testing.T) {
	e := New()
	e.Submit(context.Background(), "sleep 0.01")
	e.Submit(context.Background(), "sleep 0.05")
	e.Submit(context.Background(), "sleep 0.1")

	waitFor(t, 3*time.Second, func() bool {
		st := e.Stats()
		return st.Succeeded == 3
	})

	st := e.Stats()
	if !(float64(st.ExecMsMin) <= st.ExecMsAvg && st.ExecMsAvg <= float64(st.ExecMsMax)) {
		t.Errorf("timing stats not sanely ordered: min=%d avg=%.1f max=%d", st.ExecMsMin, st.ExecMsAvg, st.ExecMsMax)
	}
	if st.ExecMsMedian < st.ExecMsMin || st.ExecMsMedian > st.ExecMsMax {
		t.Errorf("median %d outside [min=%d, max=%d]", st.ExecMsMedian, st.ExecMsMin, st.ExecMsMax)
	}
}
