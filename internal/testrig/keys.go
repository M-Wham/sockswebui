package testrig

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"golang.org/x/crypto/curve25519"
)

// keyPair is a freshly generated WireGuard key pair. Keys live only in memory.
type keyPair struct {
	priv, pub [32]byte
}

// newKeyPair generates a WireGuard (curve25519) key pair from crypto/rand.
func newKeyPair() (keyPair, error) {
	var kp keyPair
	if _, err := rand.Read(kp.priv[:]); err != nil {
		return kp, err
	}
	kp.priv[0] &= 248
	kp.priv[31] = (kp.priv[31] & 127) | 64
	pub, err := curve25519.X25519(kp.priv[:], curve25519.Basepoint)
	if err != nil {
		return kp, err
	}
	copy(kp.pub[:], pub)
	return kp, nil
}

func b64(k [32]byte) string { return base64.StdEncoding.EncodeToString(k[:]) }

// KeyHex converts a base64 WireGuard key (config form) to the hex form used by
// the wireguard-go UAPI.
func KeyHex(b64Key string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(b64Key)
	if err != nil {
		return "", fmt.Errorf("decode key: %w", err)
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("key length %d, want 32", len(raw))
	}
	return hex.EncodeToString(raw), nil
}
