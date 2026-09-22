package cryptoutil

import (
	"crypto/rand"
	"fmt"

	"golang.org/x/crypto/nacl/box"
)

// EncryptMetrics is anonymous encryption to a public key: anyone can encrypt,
// only the private key holder can decrypt.
func EncryptMetrics(publicKey [32]byte, plaintext []byte) ([]byte, error) {
	return box.SealAnonymous(nil, plaintext, &publicKey, rand.Reader)
}

func DecryptMetrics(publicKey, privateKey [32]byte, ciphertext []byte) ([]byte, error) {
	plaintext, ok := box.OpenAnonymous(nil, ciphertext, &publicKey, &privateKey)
	if !ok {
		return nil, fmt.Errorf("cryptoutil: failed to decrypt metrics snapshot")
	}
	return plaintext, nil
}
