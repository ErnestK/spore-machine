package cryptoutil

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"golang.org/x/crypto/nacl/box"
)

func TestSignVerifyRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	content := []byte("bash_command: echo hello")
	sig := Sign(priv, content)
	if !VerifySignature(pub, content, sig) {
		t.Fatal("VerifySignature: valid signature rejected")
	}
}

func TestVerifySignatureRejectsTamperedContent(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	sig := Sign(priv, []byte("original"))
	if VerifySignature(pub, []byte("tampered"), sig) {
		t.Fatal("VerifySignature: accepted signature over different content")
	}
}

func TestVerifySignatureRejectsWrongKeyLength(t *testing.T) {
	cases := []struct {
		name string
		key  ed25519.PublicKey
	}{
		{"empty", ed25519.PublicKey{}},
		{"too short", ed25519.PublicKey(make([]byte, 16))},
		{"too long", ed25519.PublicKey(make([]byte, 64))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if VerifySignature(c.key, []byte("x"), []byte("y")) {
				t.Fatalf("VerifySignature: accepted with key length %d", len(c.key))
			}
		})
	}
}

func TestEncryptDecryptMetricsRoundTrip(t *testing.T) {
	pub, priv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("box.GenerateKey: %v", err)
	}
	plaintext := []byte(`{"node_id":"n1"}`)

	ciphertext, err := EncryptMetrics(*pub, plaintext)
	if err != nil {
		t.Fatalf("EncryptMetrics: %v", err)
	}
	got, err := DecryptMetrics(*pub, *priv, ciphertext)
	if err != nil {
		t.Fatalf("DecryptMetrics: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("DecryptMetrics: got %q, want %q", got, plaintext)
	}
}

func TestDecryptMetricsFailsWithWrongPrivateKey(t *testing.T) {
	pub, _, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("box.GenerateKey: %v", err)
	}
	_, wrongPriv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("box.GenerateKey: %v", err)
	}

	ciphertext, err := EncryptMetrics(*pub, []byte("secret"))
	if err != nil {
		t.Fatalf("EncryptMetrics: %v", err)
	}
	if _, err := DecryptMetrics(*pub, *wrongPriv, ciphertext); err == nil {
		t.Fatal("DecryptMetrics: succeeded with the wrong private key")
	}
}

func TestObjectIDDeterministicAndContentDependent(t *testing.T) {
	a := []byte("echo hello")
	b := []byte("echo helloo") // one extra byte

	id1 := ObjectID(a)
	id2 := ObjectID(a)
	if !bytes.Equal(id1, id2) {
		t.Fatal("ObjectID: same content produced different IDs")
	}

	idB := ObjectID(b)
	if bytes.Equal(id1, idB) {
		t.Fatal("ObjectID: different content produced the same ID")
	}
}
