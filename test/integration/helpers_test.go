// Package integration_test exercises real internal/node.Node instances wired
// together on localhost — no mocks. It imports sporemachine/internal/... only
// (allowed: this directory is inside module sporemachine), never redefines
// production behavior.
package integration_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/nacl/box"
	"google.golang.org/protobuf/proto"

	"sporemachine/internal/cryptoutil"
	"sporemachine/internal/node"
	"sporemachine/internal/rpcclient"
	"sporemachine/internal/sporepb"
)

type testOwner struct {
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
}

func newTestOwner(t *testing.T) testOwner {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate owner key: %v", err)
	}
	return testOwner{pub: pub, priv: priv}
}

// newGenesis returns a Genesis for owner plus the metrics keypair (public
// half already embedded in the Genesis, private half returned separately —
// only Primogenitor/Chronicle would hold it in the real system).
func newGenesis(t *testing.T, owner testOwner, networkID string) (*sporepb.Genesis, [32]byte, [32]byte) {
	t.Helper()
	metricsPub, metricsPriv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate metrics key: %v", err)
	}
	g := &sporepb.Genesis{
		NetworkId:                  networkID,
		OwnerPublicKey:             owner.pub,
		MetricsEncryptionPublicKey: metricsPub[:],
	}
	return g, *metricsPub, *metricsPriv
}

// spinNode starts a real node.Node in the background and registers its
// shutdown as test cleanup. seeds are addresses this node treats as ALIVE
// from the start (matches membership.New's seed-contact behavior).
func spinNode(t *testing.T, genesis *sporepb.Genesis, identity string, seeds []string) *node.Node {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), identity+".db")
	n, err := node.New(node.Config{
		Genesis:      genesis,
		Identity:     identity,
		Generation:   0,
		SeedContacts: seeds,
		Host:         "127.0.0.1",
		Port:         0,
		DBPath:       dbPath,
	}, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatalf("node.New(%s): %v", identity, err)
	}
	go func() {
		_ = n.Run() // returns once Shutdown() stops the gRPC server; error is expected then
	}()
	t.Cleanup(n.Shutdown)
	return n
}

func dial(t *testing.T, addr string) (sporepb.NodeServiceClient, func()) {
	t.Helper()
	client, conn, err := rpcclient.Dial(addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	return client, func() { conn.Close() }
}

// signedCommand builds a SubmitDataRequest the way Primogenitor would: sign
// over the marshaled Command, not over the bare bash string.
func signedCommand(t *testing.T, owner testOwner, bashCommand string) *sporepb.SubmitDataRequest {
	t.Helper()
	content, err := proto.Marshal(&sporepb.Command{BashCommand: bashCommand})
	if err != nil {
		t.Fatalf("marshal command: %v", err)
	}
	sig := cryptoutil.Sign(owner.priv, content)
	return &sporepb.SubmitDataRequest{Type: sporepb.DataType_COMMAND, Content: content, Signature: sig}
}

func signedFile(t *testing.T, owner testOwner, payload []byte) *sporepb.SubmitDataRequest {
	t.Helper()
	sig := cryptoutil.Sign(owner.priv, payload)
	return &sporepb.SubmitDataRequest{Type: sporepb.DataType_FILE, Content: payload, Signature: sig}
}

// marshalCommandContent returns the same bytes signedCommand would sign over,
// without signing — used to build a FILE object whose content happens to
// look like a Command, to prove the FILE path never dispatches to exec.
func marshalCommandContent(t *testing.T, bashCommand string) []byte {
	t.Helper()
	content, err := proto.Marshal(&sporepb.Command{BashCommand: bashCommand})
	if err != nil {
		t.Fatalf("marshal command content: %v", err)
	}
	return content
}

// objectExistsOn asks addr directly (via the real GetObjects RPC, the same
// one peers use during Merkle sync) whether it has id in tree.
func objectExistsOn(t *testing.T, addr string, tree sporepb.TreeKind, id []byte) bool {
	t.Helper()
	client, closeConn := dial(t, addr)
	defer closeConn()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := client.GetObjects(ctx, &sporepb.GetObjectsRequest{Tree: tree, ObjectIds: [][]byte{id}})
	if err != nil {
		return false
	}
	return len(resp.GetObjects()) == 1
}

// eventually polls cond until it returns true or timeout passes, failing the
// test with msg otherwise. Used instead of fixed sleeps throughout.
func eventually(t *testing.T, timeout time.Duration, cond func() bool, msg string, args ...any) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("condition not met within %s: %s", timeout, fmt.Sprintf(msg, args...))
	}
}
