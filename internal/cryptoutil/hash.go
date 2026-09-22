package cryptoutil

import "crypto/sha256"

// ObjectID is the content-addressing hash of an object's bytes.
func ObjectID(content []byte) []byte {
	h := sha256.Sum256(content)
	return h[:]
}
