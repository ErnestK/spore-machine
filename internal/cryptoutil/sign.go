// Package cryptoutil holds the crypto primitives: Ed25519 for owner
// signatures, X25519 (via nacl/box anonymous seal) for metrics encryption,
// SHA-256 for content addressing.
package cryptoutil

import (
	"crypto/ed25519"
)

func VerifySignature(ownerPublicKey ed25519.PublicKey, content, signature []byte) bool {
	if len(ownerPublicKey) != ed25519.PublicKeySize {
		return false
	}
	return ed25519.Verify(ownerPublicKey, content, signature)
}

func Sign(ownerPrivateKey ed25519.PrivateKey, content []byte) []byte {
	return ed25519.Sign(ownerPrivateKey, content)
}
