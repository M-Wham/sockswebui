package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"runtime/debug"
	"sync/atomic"

	"golang.org/x/crypto/argon2"

	"github.com/M-Wham/sockswebui/internal/config"
)

// argonParams are argon2id cost parameters. They are encoded into the PHC string, so
// verification always uses the parameters the hash was created with.
type argonParams struct {
	Time      uint32
	MemoryKiB uint32
	Threads   uint8
	KeyLen    uint32
	SaltLen   uint32
}

// defaultParams keep peak memory low for the < 64 MB RSS criterion: t=3, m=16 MiB, p=2.
// Hashes made with older (costlier) parameters still verify, as params are in the PHC string.
var defaultParams = argonParams{Time: 3, MemoryKiB: 16 * 1024, Threads: 2, KeyLen: 32, SaltLen: 16}

// hashSlots bounds concurrent argon2 computations (each may use MemoryKiB of RAM). Admin
// password hashing and config export/import key derivation (deriveKey) share this one
// semaphore.
var hashSlots = make(chan struct{}, 1)

// hashMaxWaiters bounds requests queued for argon2 work (hashing or waiting for a
// hashSlots slot). Past it, password-bearing routes answer 503 before reading the body
// . If field 503s appear, raise this rather than remove the bound.
const hashMaxWaiters = 8

// hashWaiters counts requests currently admitted by tryEnterHashQueue.
var hashWaiters atomic.Int32

// tryEnterHashQueue admits the caller to the argon2 queue, or writes 503 "busy, retry
// shortly" with Retry-After: 1 and returns false. Callers that get true must
// `defer leaveHashQueue()` immediately.
func tryEnterHashQueue(w http.ResponseWriter) bool {
	if hashWaiters.Add(1) > hashMaxWaiters {
		hashWaiters.Add(-1)
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, "busy, retry shortly")
		return false
	}
	return true
}

// leaveHashQueue releases a place taken by tryEnterHashQueue.
func leaveHashQueue() { hashWaiters.Add(-1) }

// hashQueueLen reports the current number of admitted requests.
func hashQueueLen() int32 { return hashWaiters.Load() }

var errBadHash = config.ErrBadHash

// hashPassword returns a PHC-encoded argon2id hash:
// $argon2id$v=19$m=<KiB>,t=<time>,p=<threads>$<salt b64>$<key b64> (raw std base64).
func hashPassword(ctx context.Context, pw string, p argonParams) (string, error) {
	return hashPasswordWith(ctx, pw, p, debug.FreeOSMemory)
}

// hashPasswordWith is hashPassword with the post-derivation FreeOSMemory hook supplied.
func hashPasswordWith(ctx context.Context, pw string, p argonParams, freeMem func()) (string, error) {
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key, err := argonKey(ctx, pw, salt, p, freeMem)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.MemoryKiB, p.Time, p.Threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// verifyPassword reports whether pw matches the PHC-encoded hash, comparing in constant time.
// It returns ctx's error if ctx ends while waiting for a hash slot.
func verifyPassword(ctx context.Context, pw, encoded string) (bool, error) {
	return verifyPasswordWith(ctx, pw, encoded, debug.FreeOSMemory)
}

// verifyPasswordWith is verifyPassword with the post-derivation FreeOSMemory hook supplied.
func verifyPasswordWith(ctx context.Context, pw, encoded string, freeMem func()) (bool, error) {
	p, salt, want, err := decodeHash(encoded)
	if err != nil {
		return false, err
	}
	got, err := argonKey(ctx, pw, salt, p, freeMem)
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// argonKey derives pw's key with p through the shared hashSlots semaphore (deriveKey).
func argonKey(ctx context.Context, pw string, salt []byte, p argonParams, freeMem func()) ([]byte, error) {
	return deriveKey(ctx, []byte(pw), salt, p.Time, p.MemoryKiB, p.Threads, p.KeyLen, freeMem)
}

func decodeHash(encoded string) (argonParams, []byte, []byte, error) {
	h, err := config.ParseArgon2idHash(encoded)
	if err != nil {
		return argonParams{}, nil, nil, errBadHash
	}
	p := argonParams{Time: h.Time, MemoryKiB: h.MemoryKiB, Threads: h.Threads, KeyLen: uint32(len(h.Key))}
	return p, h.Salt, h.Key, nil
}
