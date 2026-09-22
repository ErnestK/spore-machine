package security_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"sporemachine/internal/cryptoutil"
	"sporemachine/internal/node"
	"sporemachine/internal/sporepb"
	"sporemachine/internal/storage"
)

// --- Scenario 1: invalid signature on SubmitData is dropped at intake ---
//
// The same ingest() gate handles SubmitData and objects pulled during a
// Merkle walk (see gossipsync.go doc comment), so exercising it via the
// direct SubmitData RPC also covers the "or relayed" half of the scenario
// without needing a slow real gossip round.
func TestSubmitData_InvalidSignature_Dropped(t *testing.T) {
	keys := genKeys(t)
	genesis := genesisWith("net-sec-sig", keys.ownerPub, keys.metricsPub)
	_, addr := startNode(t, genesis, "n1", nil)
	client, conn := dialReady(t, addr)
	defer conn.Close()

	for _, dt := range []sporepb.DataType{sporepb.DataType_COMMAND, sporepb.DataType_FILE} {
		content := []byte("payload-" + dt.String())
		badSig := make([]byte, ed25519.SignatureSize) // all-zero: not a valid signature over content

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		ack, err := client.SubmitData(ctx, &sporepb.SubmitDataRequest{Type: dt, Content: content, Signature: badSig})
		cancel()
		if err != nil {
			t.Fatalf("SubmitData: %v", err)
		}
		if ack.GetAccepted() {
			t.Fatalf("%s: expected Accepted=false for invalid signature, got true", dt)
		}

		id := cryptoutil.ObjectID(content)
		ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
		got, err := client.GetObjects(ctx2, &sporepb.GetObjectsRequest{Tree: sporepb.TreeKind_DATA, ObjectIds: [][]byte{id}})
		cancel2()
		if err != nil {
			t.Fatalf("GetObjects: %v", err)
		}
		if len(got.GetObjects()) != 0 {
			t.Fatalf("%s: object with invalid signature must not be stored, got %d objects", dt, len(got.GetObjects()))
		}
	}
}

// --- Scenario 2: content whose hash doesn't match the claimed object_id is
// dropped even during peer relay ---
//
// SubmitDataRequest carries no separate object_id claim (the node always
// recomputes hash(content) itself for that path), so this mismatch can only
// actually occur on the Merkle-sync/relay path, where a peer answers
// GetObjects for an id it claimed to have during the leaf-listing step. A
// fake malicious peer simulates exactly that lie.
func TestMerkleSync_ContentHashMismatch_Dropped(t *testing.T) {
	keys := genKeys(t)
	genesis := genesisWith("net-sec-hash", keys.ownerPub, keys.metricsPub)

	fakeContent := []byte("i am not what my id claims to be")
	fakeID := cryptoutil.ObjectID([]byte("something-else-entirely")) // deliberately NOT hash(fakeContent)
	peer := newLyingPeer(t, fakeID, fakeContent)
	defer peer.stop()

	n, addr := startNode(t, genesis, "victim", []string{peer.addr})
	client, conn := dialReady(t, addr)
	defer conn.Close()

	n.TriggerGossipRound() // forces the walk against the lying peer immediately, no 30s wait

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		got, err := client.GetObjects(ctx, &sporepb.GetObjectsRequest{Tree: sporepb.TreeKind_DATA, ObjectIds: [][]byte{fakeID}})
		cancel()
		if err == nil && len(got.GetObjects()) != 0 {
			t.Fatalf("object whose content doesn't hash to its claimed id must never be stored, got %d objects", len(got.GetObjects()))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// --- Scenario 3: a node's physically-stored data is fully plaintext-readable
// (SRS 002's threat model — captured node = fully open, not a bug) ---
func TestCapturedNode_StorageIsPlaintextReadable(t *testing.T) {
	keys := genKeys(t)
	genesis := genesisWith("net-sec-capture", keys.ownerPub, keys.metricsPub)
	dbPath := filepath.Join(t.TempDir(), "captured.db")

	n, err := node.New(node.Config{Genesis: genesis, Identity: "captured", Host: "127.0.0.1", Port: 0, DBPath: dbPath}, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatalf("node.New: %v", err)
	}
	go func() { _ = n.Run() }()
	addr := n.Address()
	client, conn := dialReady(t, addr)

	content := []byte("secret-looking bash command")
	sig := cryptoutil.Sign(keys.ownerPriv, content)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	ack, err := client.SubmitData(ctx, &sporepb.SubmitDataRequest{Type: sporepb.DataType_COMMAND, Content: content, Signature: sig})
	cancel()
	if err != nil || !ack.GetAccepted() {
		t.Fatalf("setup: SubmitData failed: err=%v accepted=%v", err, ack.GetAccepted())
	}
	conn.Close()
	n.Shutdown() // simulate the live process going away; only the disk remains

	// Attacker with physical disk access, no running process, no keys.
	reopened, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen captured store: %v", err)
	}
	defer reopened.Close()

	id := cryptoutil.ObjectID(content)
	obj, err := reopened.GetObject(storage.TreeData, id)
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	if obj == nil {
		t.Fatal("expected the submitted command to be present on disk")
	}
	if string(obj.GetContent()) != string(content) {
		t.Fatalf("content on disk is not plaintext: got %q, want %q", obj.GetContent(), content)
	}
	if string(obj.GetSignature()) != string(sig) {
		t.Fatalf("signature on disk does not match: got %x, want %x", obj.GetSignature(), sig)
	}
}

// --- Scenario 4: a captured node has no owner private key anywhere, so an
// attacker who only scraped it cannot get a forged command accepted by any
// other, uncaptured node ---
func TestCapturedNode_CannotForgeCommandAcceptedElsewhere(t *testing.T) {
	keys := genKeys(t)
	genesis := genesisWith("net-sec-forge", keys.ownerPub, keys.metricsPub)
	_, victimAddr := startNode(t, genesis, "victim-node", nil)
	client, conn := dialReady(t, victimAddr)
	defer conn.Close()

	// "Attacker" only has what's physically on a captured node: no
	// owner_private_key exists anywhere on disk (SRS 001/002) — the best they
	// can do is sign with a key of their own.
	_, attackerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate attacker key: %v", err)
	}
	content := []byte("rm -rf /forged-by-attacker")
	forgedSig := cryptoutil.Sign(attackerPriv, content)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	ack, err := client.SubmitData(ctx, &sporepb.SubmitDataRequest{Type: sporepb.DataType_COMMAND, Content: content, Signature: forgedSig})
	cancel()
	if err != nil {
		t.Fatalf("SubmitData: %v", err)
	}
	if ack.GetAccepted() {
		t.Fatal("a command signed by anyone other than the real owner must be rejected")
	}
}

// --- Scenario 5: an ordinary node cannot decrypt another node's metrics
// snapshot without the metrics private key ---
func TestOrdinaryNode_CannotDecryptOthersMetrics(t *testing.T) {
	keys := genKeys(t)
	genesis := genesisWith("net-sec-metrics", keys.ownerPub, keys.metricsPub)

	nodeA, addrA := startNode(t, genesis, "a", nil)
	nodeB, addrB := startNode(t, genesis, "b", []string{addrA})

	nodeA.TriggerMetricsCycle()
	ids := nodeA.TestAllMetricsObjectIDs()
	if len(ids) == 0 {
		t.Fatal("setup: node A produced no metrics snapshot")
	}
	// B is the one seeded with A's address, so B is the side whose membership
	// actually knows a peer to talk to; the initiator of a round pulls
	// whatever it's missing from the peer it compares against (see
	// gossipsync.exchangeWith), so triggering it on B is what pulls A's
	// snapshot into B, not the other way around.
	nodeB.TriggerGossipRound()

	clientB, connB := dialReady(t, addrB)
	defer connB.Close()
	resp := waitForObjects(t, clientB, sporepb.TreeKind_METRICS, ids)
	if len(resp.GetObjects()) == 0 {
		t.Fatal("setup: B never received A's metrics snapshot to begin with")
	}

	// B (an "ordinary" node here — it only has the public key, same as every
	// node) tries to decrypt with only the public key material: must fail.
	for _, obj := range resp.GetObjects() {
		var zeroPriv [32]byte
		if _, err := cryptoutil.DecryptMetrics(keys.metricsPub, zeroPriv, obj.GetContent()); err == nil {
			t.Fatal("decrypting a metrics snapshot without the real private key must fail")
		}
	}
}

// --- Scenario 6: a captured node exposes at most its current collection
// window, never a persisted plaintext history ---
func TestCapturedNode_MetricsNeverPersistedAsPlaintext(t *testing.T) {
	keys := genKeys(t)
	genesis := genesisWith("net-sec-plaintext", keys.ownerPub, keys.metricsPub)
	n, _ := startNode(t, genesis, "metrics-node", nil)

	n.TriggerMetricsCycle()
	ids := n.TestAllMetricsObjectIDs()
	if len(ids) == 0 {
		t.Fatal("setup: no metrics snapshot produced")
	}

	// "Physically read the disk" — reopen the node's own store is not
	// possible while it's running (bbolt takes an exclusive file lock), which
	// itself demonstrates there's exactly one on-disk copy, not a separate
	// plaintext log; inspect what's actually stored via the node's own read
	// path instead, which returns raw StoredObject bytes as persisted.
	client, conn := dialReady(t, n.Address())
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	resp, err := client.GetObjects(ctx, &sporepb.GetObjectsRequest{Tree: sporepb.TreeKind_METRICS, ObjectIds: ids})
	cancel()
	if err != nil || len(resp.GetObjects()) == 0 {
		t.Fatalf("GetObjects: err=%v objects=%d", err, len(resp.GetObjects()))
	}

	for _, obj := range resp.GetObjects() {
		// The stored content must not be a plaintext MetricsSnapshot: if it
		// were, unmarshalling would succeed and produce a snapshot with a
		// non-empty node_id (proto3 unmarshal of unrelated ciphertext bytes
		// either errors outright or silently yields all-zero/empty fields —
		// either way NodeId would not match).
		var maybePlain sporepb.MetricsSnapshot
		if err := proto.Unmarshal(obj.GetContent(), &maybePlain); err == nil && maybePlain.GetNodeId() == "metrics-node" {
			t.Fatal("metrics content is stored as plaintext, not ciphertext")
		}
	}
}

// --- Scenario 7: a forged TerminateRequest is dropped immediately — no
// fan-out, no shutdown ---
func TestSubmitTerminate_InvalidSignature_Dropped(t *testing.T) {
	keys := genKeys(t)
	genesis := genesisWith("net-sec-terminate", keys.ownerPub, keys.metricsPub)

	nodeA, addrA := startNode(t, genesis, "a", nil)
	nodeB, addrB := startNode(t, genesis, "b", []string{addrA})
	// B initiates (it's the one seeded with A's address); B's known_peers in
	// that request teaches A about B too, via HandleGossipRound's Learn() —
	// so A now has a real peer it could (wrongly) fan out to.
	nodeB.TriggerGossipRound()
	waitUntilKnowsAlive(t, nodeA, addrB)

	clientA, connA := dialReady(t, addrA)
	defer connA.Close()

	badSig := make([]byte, ed25519.SignatureSize)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	ack, err := clientA.SubmitTerminate(ctx, &sporepb.TerminateRequest{Signature: badSig})
	cancel()
	if err != nil {
		t.Fatalf("SubmitTerminate: %v", err)
	}
	if ack.GetReceived() {
		t.Fatal("forged TerminateRequest must not be accepted")
	}

	// Give a hypothetical (wrong) fan-out a moment to happen, then confirm
	// neither node terminated.
	time.Sleep(200 * time.Millisecond)
	if _, conn := dialReady(t, addrA); conn != nil {
		conn.Close()
	}
	if _, conn := dialReady(t, addrB); conn != nil {
		conn.Close()
	}
	_ = nodeB
}

// --- Scenario 8: a non-Chronicle client using the exact RPC shape Chronicle
// uses for metrics, but asking for the data tree instead, is served anyway —
// "Chronicle only asks for metrics" is Chronicle's own choice, not enforced
// by the node (SRS 004, limitations section; confirmed against
// chronicle/sync.go, which drives GossipRound/MerkleChildren/MerkleLeaf/
// GetObjects with Tree=METRICS — nothing on the node side special-cases the
// caller). ---
func TestNode_DoesNotEnforceMetricsOnlyForNonChronicleCaller(t *testing.T) {
	keys := genKeys(t)
	genesis := genesisWith("net-sec-access", keys.ownerPub, keys.metricsPub)
	n, addr := startNode(t, genesis, "n1", nil)

	content := []byte("some file bytes, not a metrics snapshot")
	sig := cryptoutil.Sign(keys.ownerPriv, content)
	client, conn := dialReady(t, addr)
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	ack, err := client.SubmitData(ctx, &sporepb.SubmitDataRequest{Type: sporepb.DataType_FILE, Content: content, Signature: sig})
	cancel()
	if err != nil || !ack.GetAccepted() {
		t.Fatalf("setup: SubmitData failed: err=%v accepted=%v", err, ack.GetAccepted())
	}

	// An arbitrary client — nothing marks it as "Chronicle" to the node —
	// calls the same GetObjects RPC Chronicle would, but for TreeKind_DATA.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	got, err := client.GetObjects(ctx2, &sporepb.GetObjectsRequest{Tree: sporepb.TreeKind_DATA, ObjectIds: [][]byte{ack.GetObjectId()}})
	cancel2()
	if err != nil {
		t.Fatalf("GetObjects: %v", err)
	}
	if len(got.GetObjects()) != 1 {
		t.Fatalf("expected the node to serve the data tree to an arbitrary caller (no enforcement exists), got %d objects", len(got.GetObjects()))
	}
	_ = n
}

// waitUntilKnowsAlive polls until n considers addr ALIVE in its membership
// (peer-learning via a gossip round is asynchronous relative to the
// TriggerGossipRound call that kicks it off).
func waitUntilKnowsAlive(t *testing.T, n *node.Node, addr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if n.KnowsAlive(addr) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s never learned %s is alive", n.Address(), addr)
}

// waitForObjects polls GetObjects until every id in want is present (syncTree
// runs asynchronously after TriggerGossipRound, so the fetch may not have
// landed yet the instant the round call returns).
func waitForObjects(t *testing.T, client sporepb.NodeServiceClient, tree sporepb.TreeKind, want [][]byte) *sporepb.GetObjectsResponse {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last *sporepb.GetObjectsResponse
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		resp, err := client.GetObjects(ctx, &sporepb.GetObjectsRequest{Tree: tree, ObjectIds: want})
		cancel()
		if err == nil {
			last = resp
			if len(resp.GetObjects()) >= len(want) {
				return resp
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return last
}
