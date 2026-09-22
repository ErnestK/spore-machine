package node

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"sporemachine/internal/rpcclient"
	"sporemachine/internal/sporepb"
)

func TestRunOnceRecoveringReportsPanic(t *testing.T) {
	panicked := runOnceRecovering(context.Background(), "test", func(context.Context) {
		panic("boom")
	})
	if !panicked {
		t.Fatal("runOnceRecovering() = false for a panicking fn, want true")
	}
}

func TestRunOnceRecoveringNormalReturn(t *testing.T) {
	panicked := runOnceRecovering(context.Background(), "test", func(context.Context) {
		// returns normally, e.g. because ctx was already done
	})
	if panicked {
		t.Fatal("runOnceRecovering() = true for a normally-returning fn, want false")
	}
}

func TestRunSupervisedRestartsUpToLimitThenStops(t *testing.T) {
	var mu sync.Mutex
	calls := 0

	fn := func(context.Context) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n <= maxGoroutineRestarts {
			panic("boom")
		}
		// (maxGoroutineRestarts+1)-th call returns normally.
	}

	gaveUp := false
	onGiveUp := func() { gaveUp = true }

	runSupervised(context.Background(), "test", fn, onGiveUp)

	mu.Lock()
	got := calls
	mu.Unlock()

	if want := maxGoroutineRestarts + 1; got != want {
		t.Errorf("fn invoked %d times, want %d (initial attempt + %d restarts)", got, want, maxGoroutineRestarts)
	}
	if gaveUp {
		t.Error("onGiveUp was called, want it not called since fn eventually returned normally")
	}
}

func TestRunSupervisedGivesUpAfterExhaustingRestarts(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	giveUpCalls := 0

	fn := func(context.Context) {
		mu.Lock()
		calls++
		mu.Unlock()
		panic("always boom")
	}
	onGiveUp := func() {
		mu.Lock()
		giveUpCalls++
		mu.Unlock()
	}

	runSupervised(context.Background(), "test", fn, onGiveUp)

	mu.Lock()
	defer mu.Unlock()
	if want := maxGoroutineRestarts + 1; calls != want {
		t.Errorf("fn invoked %d times, want %d", calls, want)
	}
	if giveUpCalls != 1 {
		t.Errorf("onGiveUp called %d times, want exactly 1", giveUpCalls)
	}
}

func TestRunSupervisedDoesNotRestartOrGiveUpAfterCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already done before fn ever runs

	calls := 0
	fn := func(context.Context) {
		calls++
		panic("boom, but ctx is already cancelled")
	}
	gaveUp := false
	onGiveUp := func() { gaveUp = true }

	runSupervised(ctx, "test", fn, onGiveUp)

	if calls != 1 {
		t.Errorf("fn invoked %d times, want exactly 1 (no restart once ctx is done)", calls)
	}
	if gaveUp {
		t.Error("onGiveUp was called, want it not called — node is shutting down anyway")
	}
}

// TestNodeStartsAndAnswersPing is a smoke test: New+Run actually listens and
// answers a real gRPC call, then Shutdown stops it cleanly.
func TestNodeStartsAndAnswersPing(t *testing.T) {
	cfg := Config{
		Genesis: &sporepb.Genesis{},
		Host:    "127.0.0.1",
		Port:    0,
		DBPath:  filepath.Join(t.TempDir(), "smoke.db"),
	}
	n, err := New(cfg, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	runErr := make(chan error, 1)
	go func() { runErr <- n.Run() }()
	defer n.Shutdown()

	client, conn, err := rpcclient.Dial(n.selfAddress)
	if err != nil {
		t.Fatalf("Dial(%s) error = %v", n.selfAddress, err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := client.Ping(ctx, &sporepb.PingRequest{FromAddress: "test-probe"})
	if err != nil {
		t.Fatalf("Ping() error = %v, want the freshly-started node to answer", err)
	}
	if resp == nil {
		t.Fatal("Ping() returned a nil response")
	}

	n.Shutdown()
	select {
	case err := <-runErr:
		// grpcServer.Serve returns a non-nil "server stopped" style error on
		// Stop(); that's expected here, not a test failure.
		_ = err
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return within 2s of Shutdown()")
	}
}
