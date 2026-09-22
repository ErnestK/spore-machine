package gossipsync

import (
	"context"
	"crypto/ed25519"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/proto"

	"sporemachine/internal/commandexec"
	"sporemachine/internal/cryptoutil"
	"sporemachine/internal/membership"
	"sporemachine/internal/merkle"
	"sporemachine/internal/sporepb"
	"sporemachine/internal/storage"
)

func newTestSync(t *testing.T, ownerPub ed25519.PublicKey) *GossipSync {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	mem := membership.New("self:1", nil)
	exec := commandexec.New()
	return New(context.Background(), store, mem, exec, ownerPub, "self:1")
}

func TestIngestTreeData_RejectsHashMismatch(t *testing.T) {
	ownerPub, ownerPriv, _ := ed25519.GenerateKey(nil)
	g := newTestSync(t, ownerPub)

	content := []byte("echo hello")
	sig := cryptoutil.Sign(ownerPriv, content)
	wrongID := cryptoutil.ObjectID([]byte("something else"))

	obj := &sporepb.StoredObject{Type: sporepb.DataType_COMMAND, Content: content, Signature: sig}
	if g.ingest(storage.TreeData, wrongID, obj) {
		t.Fatal("expected ingest to reject a hash/content mismatch")
	}
	if stored, _ := g.store.GetObject(storage.TreeData, wrongID); stored != nil {
		t.Fatal("rejected object must not be stored")
	}
}

func TestIngestTreeData_RejectsInvalidSignature(t *testing.T) {
	ownerPub, _, _ := ed25519.GenerateKey(nil)
	_, attackerPriv, _ := ed25519.GenerateKey(nil)
	g := newTestSync(t, ownerPub)

	content := []byte("echo hello")
	id := cryptoutil.ObjectID(content)
	badSig := cryptoutil.Sign(attackerPriv, content) // a real signature, but not the owner's

	obj := &sporepb.StoredObject{Type: sporepb.DataType_COMMAND, Content: content, Signature: badSig}
	if g.ingest(storage.TreeData, id, obj) {
		t.Fatal("expected ingest to reject a signature not made by the owner")
	}
	if stored, _ := g.store.GetObject(storage.TreeData, id); stored != nil {
		t.Fatal("rejected object must not be stored")
	}
}

func TestIngestTreeData_AcceptsValidObject(t *testing.T) {
	tests := []struct {
		name         string
		dataType     sporepb.DataType
		content      []byte
		wantDispatch bool
	}{
		{"file is stored but not dispatched for execution", sporepb.DataType_FILE, []byte("plain file bytes"), false},
		{"command is stored and dispatched for execution", sporepb.DataType_COMMAND, mustMarshalCommand(t, "true"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ownerPub, ownerPriv, _ := ed25519.GenerateKey(nil)
			g := newTestSync(t, ownerPub)

			id := cryptoutil.ObjectID(tt.content)
			sig := cryptoutil.Sign(ownerPriv, tt.content)
			obj := &sporepb.StoredObject{Type: tt.dataType, Content: tt.content, Signature: sig}

			if !g.ingest(storage.TreeData, id, obj) {
				t.Fatal("expected a validly signed, hash-matching object to be accepted")
			}
			stored, err := g.store.GetObject(storage.TreeData, id)
			if err != nil || stored == nil {
				t.Fatalf("expected object to be stored, err=%v stored=%v", err, stored)
			}

			// Submit increments Stored synchronously before the exec goroutine runs.
			gotDispatch := g.exec.Stats().Stored > 0
			if gotDispatch != tt.wantDispatch {
				t.Fatalf("dispatch to executor = %v, want %v", gotDispatch, tt.wantDispatch)
			}
		})
	}
}

func TestIngestTreeMetrics_NoSignatureCheck(t *testing.T) {
	ownerPub, _, _ := ed25519.GenerateKey(nil)
	g := newTestSync(t, ownerPub)

	content := []byte("this is an already-encrypted metrics snapshot")
	id := cryptoutil.ObjectID(content)
	obj := &sporepb.StoredObject{Content: content, Signature: []byte("not a real signature at all")}

	if !g.ingest(storage.TreeMetrics, id, obj) {
		t.Fatal("expected metrics object to be accepted on hash match alone, per structure/d2/3.5_node_gossip_sync.md §1 (metrics have no owner signature to check)")
	}
	if stored, _ := g.store.GetObject(storage.TreeMetrics, id); stored == nil {
		t.Fatal("expected accepted metrics object to be stored")
	}
}

func TestHandleSubmitData_RejectionStillReportsObjectID(t *testing.T) {
	ownerPub, _, _ := ed25519.GenerateKey(nil)
	g := newTestSync(t, ownerPub)

	content := []byte("echo hello")
	wantID := cryptoutil.ObjectID(content)

	req := &sporepb.SubmitDataRequest{Type: sporepb.DataType_COMMAND, Content: content, Signature: []byte("garbage")}
	ack := g.HandleSubmitData(req)
	if ack.GetAccepted() {
		t.Fatal("expected rejection for an unsigned/garbage-signed command")
	}
	if string(ack.GetObjectId()) != string(wantID) {
		t.Fatalf("ack.ObjectId = %x, want %x (computed regardless of acceptance)", ack.GetObjectId(), wantID)
	}
}

func TestHandleGossipRound_ReflectsOwnStateAndLearnsPeers(t *testing.T) {
	ownerPub, _, _ := ed25519.GenerateKey(nil)
	g := newTestSync(t, ownerPub)

	req := &sporepb.GossipExchange{
		DataRootHash:    []byte("bogus, not a real root hash"),
		MetricsRootHash: []byte("also bogus"),
		KnownPeers:      []string{"peer:9"},
	}
	resp := g.HandleGossipRound(req)

	if string(resp.GetDataRootHash()) == string(req.GetDataRootHash()) {
		t.Fatal("response must reflect this node's own computed root, not echo the requester's bogus hash")
	}
	if !g.mem.IsAlive("peer:9") {
		t.Fatal("expected known_peers from the request to be learned into membership")
	}
}

func TestRound_IncrementsRoundsAndResetAndReadClearsIt(t *testing.T) {
	ownerPub, _, _ := ed25519.GenerateKey(nil)
	g := newTestSync(t, ownerPub)

	g.round(context.Background())
	g.round(context.Background())

	st := g.ResetAndRead()
	if st.Rounds != 2 {
		t.Fatalf("Rounds = %d, want 2", st.Rounds)
	}

	st2 := g.ResetAndRead()
	if st2.Rounds != 0 || st2.ObjectsSynced != 0 || st2.BytesSynced != 0 {
		t.Fatalf("second ResetAndRead should see zeroed counters, got %+v", st2)
	}
}

func TestStartSyncTree_SkipsIfAlreadyInFlightForSamePeerAndTree(t *testing.T) {
	ownerPub, _, _ := ed25519.GenerateKey(nil)
	g := newTestSync(t, ownerPub)
	myTree, err := merkle.Build(g.store.LeafProviderFor(storage.TreeData))
	if err != nil {
		t.Fatalf("merkle.Build: %v", err)
	}

	// Simulate a sync already in progress for peer:1's data tree — deterministic,
	// no real goroutine/network involved, so there's nothing to race against.
	key := "peer:1|" + treeLabel(storage.TreeData)
	g.syncMu.Lock()
	g.inFlight[key] = struct{}{}
	g.syncMu.Unlock()

	g.startSyncTree(context.Background(), "peer:1", storage.TreeData, myTree)

	g.syncMu.Lock()
	_, stillPresent := g.inFlight[key]
	count := len(g.inFlight)
	g.syncMu.Unlock()
	if !stillPresent || count != 1 {
		t.Fatalf("inFlight = %v (len %d), want exactly {%q} unchanged — startSyncTree must skip when already in flight for the same peer+tree", g.inFlight, count, key)
	}
}

func TestTreeLabel_DistinguishesDataAndMetrics(t *testing.T) {
	if treeLabel(storage.TreeData) == treeLabel(storage.TreeMetrics) {
		t.Fatal("treeLabel must differ between TreeData and TreeMetrics, so a data sync and a metrics sync for the same peer don't share an in-flight key")
	}
}

func mustMarshalCommand(t *testing.T, bashCommand string) []byte {
	t.Helper()
	raw, err := proto.Marshal(&sporepb.Command{BashCommand: bashCommand})
	if err != nil {
		t.Fatalf("marshal command: %v", err)
	}
	return raw
}
