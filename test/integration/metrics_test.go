package integration_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"sporemachine/internal/cryptoutil"
	"sporemachine/internal/sporepb"
)

// Scenario 8 (Integration #8): a node encrypts its metrics snapshot with the
// Genesis metrics public key and stores it in the metrics tree; whoever
// holds the matching private key (Primogenitor/Chronicle) can decrypt it
// back to the original plaintext after fetching it through the real
// GetObjects RPC (the same path a peer/Chronicle would use).
func TestMetricsSnapshotRoundTripsThroughEncryption(t *testing.T) {
	owner := newTestOwner(t)
	genesis, _, metricsPriv := newGenesis(t, owner, "test-net")

	a := spinNode(t, genesis, "a", nil)
	a.TriggerMetricsCycle()

	var objID []byte
	eventually(t, 3*time.Second, func() bool {
		ids := a.TestAllMetricsObjectIDs()
		if len(ids) == 0 {
			return false
		}
		objID = ids[0]
		return true
	}, "node should have produced one metrics snapshot object")

	if !objectExistsOn(t, a.Address(), sporepb.TreeKind_METRICS, objID) {
		t.Fatalf("expected the snapshot to be fetchable via GetObjects, like a real peer/Chronicle would")
	}

	client, closeConn := dial(t, a.Address())
	defer closeConn()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := client.GetObjects(ctx, &sporepb.GetObjectsRequest{Tree: sporepb.TreeKind_METRICS, ObjectIds: [][]byte{objID}})
	if err != nil || len(resp.GetObjects()) != 1 {
		t.Fatalf("GetObjects: err=%v objects=%d", err, len(resp.GetObjects()))
	}
	ciphertext := resp.GetObjects()[0].GetContent()

	var metricsPub [32]byte
	copy(metricsPub[:], genesis.GetMetricsEncryptionPublicKey())
	plaintext, err := cryptoutil.DecryptMetrics(metricsPub, metricsPriv, ciphertext)
	if err != nil {
		t.Fatalf("DecryptMetrics: %v", err)
	}

	var snap sporepb.MetricsSnapshot
	if err := proto.Unmarshal(plaintext, &snap); err != nil {
		t.Fatalf("unmarshal decrypted snapshot: %v", err)
	}
	if snap.GetNodeId() != "a" {
		t.Fatalf("expected NodeId %q, got %q", "a", snap.GetNodeId())
	}
}
