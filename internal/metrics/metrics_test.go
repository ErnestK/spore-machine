package metrics

import (
	"context"
	"crypto/ed25519"
	"path/filepath"
	"testing"

	"sporemachine/internal/commandexec"
	"sporemachine/internal/gossipsync"
	"sporemachine/internal/membership"
	"sporemachine/internal/sporepb"
	"sporemachine/internal/storage"
)

func newTestCollector(t *testing.T, dbPath string) (*Collector, *storage.Store, *gossipsync.GossipSync) {
	t.Helper()
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	mem := membership.New("self:1", nil)
	exec := commandexec.New()
	gossip := gossipsync.New(context.Background(), store, mem, exec, ed25519.PublicKey(make([]byte, ed25519.PublicKeySize)), "self:1")
	var metricsPub [32]byte
	c := New("node-1", "2026-09-22T00:00:00Z", "", store, mem, exec, gossip, metricsPub)
	return c, store, gossip
}

func countStoredObjects(t *testing.T, store *storage.Store, tree storage.Tree) int {
	t.Helper()
	n := 0
	for g := 0; g < 256; g++ {
		ids, err := store.LeafGroupIDs(tree, byte(g))
		if err != nil {
			t.Fatalf("LeafGroupIDs(%d): %v", g, err)
		}
		n += len(ids)
	}
	return n
}

func TestCycle_NoPutOnStoreStatsError(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	c, store, _ := newTestCollector(t, dbPath)

	// Close the store out from under the collector: Stats() will now error,
	// and cycle() must return early without ever calling PutObject.
	if err := store.Close(); err != nil {
		t.Fatalf("store.Close: %v", err)
	}

	c.cycle() // must not panic

	reopened, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()
	if n := countStoredObjects(t, reopened, storage.TreeMetrics); n != 0 {
		t.Fatalf("expected no metrics object stored after a Stats() error, found %d", n)
	}
}

func TestCycle_StoredSnapshotHasNoSignature(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	c, store, _ := newTestCollector(t, dbPath)
	defer store.Close()

	c.cycle()

	var found *sporepb.StoredObject
	for g := 0; g < 256; g++ {
		ids, err := store.LeafGroupIDs(storage.TreeMetrics, byte(g))
		if err != nil {
			t.Fatalf("LeafGroupIDs(%d): %v", g, err)
		}
		for _, id := range ids {
			obj, err := store.GetObject(storage.TreeMetrics, id)
			if err != nil {
				t.Fatalf("GetObject: %v", err)
			}
			found = obj
		}
	}
	if found == nil {
		t.Fatal("expected cycle() to store exactly one metrics snapshot object")
	}
	if len(found.GetSignature()) != 0 {
		t.Fatalf("metrics snapshot must carry no owner signature, got %d bytes", len(found.GetSignature()))
	}
	if found.GetType() != sporepb.DataType_FILE {
		t.Fatalf("metrics snapshot Type = %v, want zero-value DataType_FILE (no COMMAND dispatch implied)", found.GetType())
	}
}

func TestCycle_ConsumesGossipSyncStatsWithoutLeftover(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	c, store, gossip := newTestCollector(t, dbPath)
	defer store.Close()

	// Baseline: a fresh GossipSync starts at zero.
	if st := gossip.ResetAndRead(); st.Rounds != 0 || st.ObjectsSynced != 0 || st.BytesSynced != 0 {
		t.Fatalf("expected fresh gossip stats to start at zero, got %+v", st)
	}

	c.cycle()

	// cycle() must have already read-and-reset gossip's sync stats into the
	// snapshot it stored; a second read must see zero, not a leftover count
	// that would double-count into the next cycle's snapshot.
	if st := gossip.ResetAndRead(); st.Rounds != 0 || st.ObjectsSynced != 0 || st.BytesSynced != 0 {
		t.Fatalf("expected gossip stats to be drained by cycle(), got leftover %+v", st)
	}

	// Sanity: cycle() actually stored a snapshot (it's encrypted, so we can't
	// decrypt and inspect its MerkleSync field here without the private key).
	if n := countStoredObjects(t, store, storage.TreeMetrics); n != 1 {
		t.Fatalf("expected exactly one stored metrics snapshot, found %d", n)
	}
}
