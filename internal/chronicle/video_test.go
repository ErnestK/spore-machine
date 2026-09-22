package chronicle

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"testing"
)

func writeCSVRows(t *testing.T, path string, rows [][]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := csv.NewWriter(f)
	if err := w.Write([]string{"created_at_utc", "node_id", "parent_id", "died_at_utc"}); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if err := w.Write(r); err != nil {
			t.Fatal(err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		t.Fatal(err)
	}
}

// readGrowthCSV skips a row with a bad timestamp (same column count, bad
// cell content) rather than failing the whole file. A structurally short row
// (fewer columns than the header) is a different case: Go's encoding/csv
// Reader enforces a consistent column count across ReadAll and errors out
// before readGrowthCSV's own len(rec)<3 defensive check ever runs — so that
// check is effectively unreachable via this entry point today. Tested here
// only for the reachable case; flagged in the report, not changed.
func TestReadGrowthCSV_SkipsMalformedRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "growth.csv")
	writeCSVRows(t, path, [][]string{
		{"2026-01-01T00:00:00Z", "node-a", "", ""},  // good
		{"not-a-timestamp", "node-b", "node-a", ""}, // bad timestamp — skipped
	})

	rows, err := readGrowthCSV(path)
	if err != nil {
		t.Fatalf("readGrowthCSV: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1 (only the well-formed row): %+v", len(rows), rows)
	}
	if rows[0].nodeID != "node-a" {
		t.Errorf("kept row nodeID = %q, want %q", rows[0].nodeID, "node-a")
	}
}

func TestReadGrowthCSV_StructurallyShortRow_SkippedNotFailed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "growth.csv")
	writeCSVRows(t, path, [][]string{
		{"2026-01-01T00:00:00Z", "node-a", "", ""},
	})
	// Append a short row by hand — writeCSVRows' writer would enforce column
	// count itself, so bypass it here to prove the real Reader behavior.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("2026-01-02T00:00:00Z,node-b\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	rows, err := readGrowthCSV(path)
	if err != nil {
		t.Fatalf("readGrowthCSV: want the good row to still come back despite the short row, got error: %v", err)
	}
	if len(rows) != 1 || rows[0].nodeID != "node-a" {
		t.Errorf("readGrowthCSV: want exactly [node-a] (short row skipped, logged, not fatal), got %+v", rows)
	}
}

func TestRenderGrowthVideo_SingleRootRow_NoPanic(t *testing.T) {
	csvPath := filepath.Join(t.TempDir(), "growth.csv")
	writeCSVRows(t, csvPath, [][]string{
		{"2026-01-01T00:00:00Z", "node-a", "", ""}, // root: no parent
	})
	outPath := filepath.Join(t.TempDir(), "growth.gif")

	if err := RenderGrowthVideo(csvPath, outPath); err != nil {
		t.Fatalf("RenderGrowthVideo: %v", err)
	}

	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("output GIF not created: %v", err)
	}
	if info.Size() == 0 {
		t.Error("output GIF is empty")
	}
}
