package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Upper bounds accepted when decoding a stored argon2id hash, so a tampered config cannot
// make login allocate unbounded memory or CPU.
const (
	Argon2MaxMemoryKiB = 256 * 1024
	Argon2MaxTime      = 10
	Argon2MaxKeyLen    = 64
)

// ErrBadHash reports a malformed or out-of-bounds argon2id PHC string.
var ErrBadHash = errors.New("malformed password hash")

// Argon2idHash is a decoded PHC string:
// $argon2id$v=19$m=<KiB>,t=<time>,p=<threads>$<salt b64>$<key b64> (raw std base64).
type Argon2idHash struct {
	Time      uint32
	MemoryKiB uint32
	Threads   uint8
	Salt      []byte
	Key       []byte
}

// ParseArgon2idHash strictly decodes an argon2id PHC string, enforcing the current
// argon2 version and the cost bounds above. Errors never include the input.
func ParseArgon2idHash(encoded string) (Argon2idHash, error) {
	var h Argon2idHash
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return h, ErrBadHash
	}
	var v int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != argon2.Version {
		return h, ErrBadHash
	}
	var threads uint32
	if n, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &h.MemoryKiB, &h.Time, &threads); err != nil || n != 3 {
		return h, ErrBadHash
	}
	if h.Time == 0 || h.Time > Argon2MaxTime || h.MemoryKiB == 0 || h.MemoryKiB > Argon2MaxMemoryKiB ||
		threads == 0 || threads > 255 {
		return h, ErrBadHash
	}
	h.Threads = uint8(threads)
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return h, ErrBadHash
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) == 0 || len(key) > Argon2MaxKeyLen {
		return h, ErrBadHash
	}
	h.Salt, h.Key = salt, key
	return h, nil
}
