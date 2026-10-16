package api

import (
	"context"
	"runtime/debug"

	"golang.org/x/crypto/argon2"

	"github.com/M-Wham/sockswebui/internal/config"
)

// argonIDKey is the argon2id primitive; tests replace it to block or count.
var argonIDKey = argon2.IDKey

// deriveKey is the one place argon2id runs. It waits for a hashSlots slot (password.go), or
// for ctx to end so abandoned requests do not queue forever, derives the key, releases the
// slot, then calls freeMem (debug.FreeOSMemory in production). The slot is released first so the full GC in
// freeMem never delays requests waiting for a slot.
func deriveKey(ctx context.Context, secret, salt []byte, t, mKiB uint32, p uint8, keyLen uint32, freeMem func()) ([]byte, error) {
	select {
	case hashSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	key := argonIDKey(secret, salt, t, mKiB, p, keyLen)
	<-hashSlots
	freeMem()
	return key, nil
}

// exportKDF returns the config export/import key derivation: argon2id behind hashSlots,
// bound to ctx so a departed client stops waiting.
func (s *Server) exportKDF(ctx context.Context) config.KDFFunc {
	return func(passphrase, salt []byte, t, mKiB uint32, p uint8) ([]byte, error) {
		return deriveKey(ctx, passphrase, salt, t, mKiB, p, 32, s.freeMem)
	}
}

// freeMem calls the FreeOSMemory hook (debug.FreeOSMemory unless a test replaced it).
func (s *Server) freeMem() {
	if s.freeOSMemory != nil {
		s.freeOSMemory()
		return
	}
	debug.FreeOSMemory()
}
