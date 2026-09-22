package main

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// TestMissingGenesisFlagFails builds the real node binary and runs it with
// no flags at all, verifying it fails fast (non-zero exit) rather than
// hanging or panicking, per main()'s "--genesis-b64 is required" check.
// Built as a real binary (not re-exec'd via the test binary) to avoid `go
// test`'s own flags colliding with main's flag.Parse().
func TestMissingGenesisFlagFails(t *testing.T) {
	binPath := filepath.Join(t.TempDir(), "node-under-test")
	build := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/node: %v\n%s", err, out)
	}

	run := exec.Command(binPath)
	out, err := run.CombinedOutput()
	if err == nil {
		t.Fatalf("node binary exited 0 with no flags, want a non-zero exit (missing --genesis-b64); output:\n%s", out)
	}
}
