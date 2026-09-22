package chronicle

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"sporemachine/internal/sporepb"
)

func TestObserve_NilNestedStats_DoesNotPanicAndLeavesGaugesUnset(t *testing.T) {
	e := NewExporter()
	snap := &sporepb.MetricsSnapshot{
		NodeId:     "node-a",
		CpuPercent: 12.5,
		// Commands, Storage, MerkleSync left nil on purpose.
	}

	e.Observe(snap) // must not panic

	if got := testutil.ToFloat64(e.gauges["cpu_percent"].WithLabelValues("node-a")); got != 12.5 {
		t.Errorf("cpu_percent = %v, want 12.5 (always set, independent of nested stats)", got)
	}

	for _, name := range []string{"commands_stored", "storage_object_count", "merkle_sync_rounds"} {
		if n := testutil.CollectAndCount(e.gauges[name]); n != 0 {
			t.Errorf("%s: got %d series, want 0 (never set when nested stats are nil)", name, n)
		}
	}
}

func TestObserve_FullSnapshot_SetsNestedGauges(t *testing.T) {
	e := NewExporter()
	snap := &sporepb.MetricsSnapshot{
		NodeId:   "node-a",
		Commands: &sporepb.CommandStats{Stored: 3, Succeeded: 2, Failed: 1},
		Storage:  &sporepb.StorageStats{ObjectCount: 10, TotalBytes: 1024},
	}

	e.Observe(snap)

	if got := testutil.ToFloat64(e.gauges["commands_stored"].WithLabelValues("node-a")); got != 3 {
		t.Errorf("commands_stored = %v, want 3", got)
	}
	if got := testutil.ToFloat64(e.gauges["storage_total_bytes"].WithLabelValues("node-a")); got != 1024 {
		t.Errorf("storage_total_bytes = %v, want 1024", got)
	}
	if n := testutil.CollectAndCount(e.gauges["merkle_sync_rounds"]); n != 0 {
		t.Errorf("merkle_sync_rounds: got %d series, want 0 (MerkleSync left nil)", n)
	}
}
