// Package commandexec runs already-validated commands.
package commandexec

import (
	"context"
	"os/exec"
	"slices"
	"sync"
	"time"
)

type Executor struct {
	mu                        sync.Mutex
	stored, succeeded, failed int64
	execMillis                []int64
}

func New() *Executor {
	return &Executor{}
}

// Submit runs bashCommand in its own short-lived goroutine so a long command
// doesn't block new object intake. ctx is the node's root context: on
// cancellation the running command is killed rather than left to finish.
func (e *Executor) Submit(ctx context.Context, bashCommand string) {
	e.mu.Lock()
	e.stored++
	e.mu.Unlock()
	go e.run(ctx, bashCommand)
}

func (e *Executor) run(ctx context.Context, bashCommand string) {
	start := time.Now()
	cmd := exec.CommandContext(ctx, "bash", "-c", bashCommand)
	err := cmd.Run()
	elapsedMs := time.Since(start).Milliseconds()

	e.mu.Lock()
	defer e.mu.Unlock()
	if err != nil {
		e.failed++
	} else {
		e.succeeded++
	}
	e.execMillis = append(e.execMillis, elapsedMs)
}

type Stats struct {
	Stored, Succeeded, Failed int64
	ExecMsMax, ExecMsMin      int64
	ExecMsAvg                 float64
	ExecMsMedian              int64
}

func (e *Executor) Stats() Stats {
	e.mu.Lock()
	defer e.mu.Unlock()

	st := Stats{Stored: e.stored, Succeeded: e.succeeded, Failed: e.failed}
	if len(e.execMillis) == 0 {
		return st
	}

	sorted := make([]int64, len(e.execMillis))
	copy(sorted, e.execMillis)
	slices.Sort(sorted)

	var sum int64
	for _, v := range sorted {
		sum += v
	}
	st.ExecMsMax = sorted[len(sorted)-1]
	st.ExecMsMin = sorted[0]
	st.ExecMsAvg = float64(sum) / float64(len(sorted))
	st.ExecMsMedian = sorted[len(sorted)/2]
	return st
}
