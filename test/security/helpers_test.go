// Package security_test holds black-box security/adversarial tests that
// exercise real nodes over real gRPC — see structure/test_plan.md's
// "Security / adversarial" section for the approved scenario list.
package security_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/nacl/box"
	"google.golang.org/grpc"

	"sporemachine/internal/node"
	"sporemachine/internal/rpcclient"
	"sporemachine/internal/sporepb"
)

type keySet struct {
	ownerPub    ed25519.PublicKey
	ownerPriv   ed25519.PrivateKey
	metricsPub  [32]byte
	metricsPriv [32]byte
}

func genKeys(t *testing.T) keySet {
	t.Helper()
	ownerPub, ownerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate owner key: %v", err)
	}
	metricsPub, metricsPriv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate metrics key: %v", err)
	}
	return keySet{ownerPub: ownerPub, ownerPriv: ownerPriv, metricsPub: *metricsPub, metricsPriv: *metricsPriv}
}

func genesisWith(networkID string, ownerPub ed25519.PublicKey, metricsPub [32]byte) *sporepb.Genesis {
	return &sporepb.Genesis{
		NetworkId:                  networkID,
		OwnerPublicKey:             ownerPub,
		MetricsEncryptionPublicKey: metricsPub[:],
	}
}

// startNode boots a real node on 127.0.0.1:0 and registers its shutdown;
// returns the node and the address to dial it at (n.Address(), read back
// after New() since port 0 picks one dynamically).
func startNode(t *testing.T, genesis *sporepb.Genesis, identity string, seeds []string) (*node.Node, string) {
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
		t.Fatalf("node.New: %v", err)
	}
	go func() { _ = n.Run() }()
	t.Cleanup(n.Shutdown)
	return n, n.Address()
}

// dialReady dials addr and blocks (with a bounded deadline) until a trivial
// RPC actually succeeds — Run() starts gRPC serving in a goroutine, so a
// freshly-started node isn't guaranteed ready the instant startNode returns.
func dialReady(t *testing.T, addr string) (sporepb.NodeServiceClient, *grpc.ClientConn) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		client, conn, err := rpcclient.Dial(addr)
		if err != nil {
			lastErr = err
			time.Sleep(20 * time.Millisecond)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		_, err = client.Ping(ctx, &sporepb.PingRequest{FromAddress: "probe"})
		cancel()
		if err == nil {
			return client, conn
		}
		lastErr = err
		_ = conn.Close()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("dial %s: never became ready: %v", addr, lastErr)
	return nil, nil
}
