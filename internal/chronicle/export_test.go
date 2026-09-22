package chronicle

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"testing"

	"sporemachine/internal/sporepb"
)

func TestWriteGrowthCSV_DedupesByNodeID_KeepsFirstSeen(t *testing.T) {
	snapshots := []*sporepb.MetricsSnapshot{
		{NodeId: "node-a", CreatedAtUtc: "2026-01-01T00:00:00Z", ParentId: ""},
		{NodeId: "node-b", CreatedAtUtc: "2026-01-02T00:00:00Z", ParentId: "node-a"},
		{NodeId: "node-a", CreatedAtUtc: "2026-01-01T00:03:00Z", ParentId: ""}, // later snapshot, same node — must be skipped
	}

	out := filepath.Join(t.TempDir(), "growth.csv")
	if err := WriteGrowthCSV(snapshots, out); err != nil {
		t.Fatalf("WriteGrowthCSV: %v", err)
	}

	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	records, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}

	// header + 2 rows (node-a kept once, node-b once)
	if len(records) != 3 {
		t.Fatalf("got %d records (incl. header), want 3: %v", len(records), records)
	}

	rowsByNode := map[string][]string{}
	for _, rec := range records[1:] {
		rowsByNode[rec[1]] = rec
	}
	if len(rowsByNode) != 2 {
		t.Fatalf("expected exactly 2 distinct node_id rows, got %d", len(rowsByNode))
	}
	if got := rowsByNode["node-a"][0]; got != "2026-01-01T00:00:00Z" {
		t.Errorf("node-a created_at_utc = %q, want first-seen value %q", got, "2026-01-01T00:00:00Z")
	}
}

func TestWriteGrowthCSV_EmptyInput(t *testing.T) {
	out := filepath.Join(t.TempDir(), "growth.csv")
	if err := WriteGrowthCSV(nil, out); err != nil {
		t.Fatalf("WriteGrowthCSV(nil): %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Error("expected at least a header row, got empty file")
	}
}
