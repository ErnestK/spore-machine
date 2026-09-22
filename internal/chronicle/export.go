package chronicle

import (
	"encoding/csv"
	"os"

	"google.golang.org/protobuf/proto"

	"sporemachine/internal/cryptoutil"
	"sporemachine/internal/sporepb"
	"sporemachine/internal/storage"
)

// ReadSnapshots decrypts every metrics object currently in store.
func ReadSnapshots(store *storage.Store, pub, priv [32]byte) ([]*sporepb.MetricsSnapshot, error) {
	var out []*sporepb.MetricsSnapshot
	for group := range 256 {
		ids, err := store.LeafGroupIDs(storage.TreeMetrics, byte(group))
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			obj, err := store.GetObject(storage.TreeMetrics, id)
			if err != nil || obj == nil {
				continue
			}
			plaintext, err := cryptoutil.DecryptMetrics(pub, priv, obj.GetContent())
			if err != nil {
				continue
			}
			var snap sporepb.MetricsSnapshot
			if err := proto.Unmarshal(plaintext, &snap); err != nil {
				continue
			}
			out = append(out, &snap)
		}
	}
	return out, nil
}

// WriteGrowthCSV writes one row per distinct node_id (birth data is static
// across a node's snapshots). died_at_utc is always empty in v0 — there is
// no decided mechanism yet for recording a node's death time.
func WriteGrowthCSV(snapshots []*sporepb.MetricsSnapshot, outPath string) error {
	seen := make(map[string]bool)
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()
	if err := w.Write([]string{"created_at_utc", "node_id", "parent_id", "died_at_utc"}); err != nil {
		return err
	}
	for _, s := range snapshots {
		if seen[s.GetNodeId()] {
			continue
		}
		seen[s.GetNodeId()] = true
		if err := w.Write([]string{s.GetCreatedAtUtc(), s.GetNodeId(), s.GetParentId(), ""}); err != nil {
			return err
		}
	}
	return nil
}
