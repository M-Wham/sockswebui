package socks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// recHandler keeps every record at every level with raw (unescaped) attr
// values: a superset of what the supervisor's slot ring receives (it is a
// slog.Handler fed the same records, at INFO).
type recHandler struct {
	mu   *sync.Mutex
	recs *[]string
}

func (h recHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h recHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h recHandler) WithGroup(string) slog.Handler            { return h }
func (h recHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&b, " %s=%s", a.Key, a.Value.String())
		return true
	})
	h.mu.Lock()
	*h.recs = append(*h.recs, b.String())
	h.mu.Unlock()
	return nil
}

// logCapture tees a server's logs into a text handler and a raw record list.
type logCapture struct {
	mu   sync.Mutex
	text bytes.Buffer
	recs []string
}

func (lc *logCapture) logger() *slog.Logger {
	text := slog.NewTextHandler(&lockedWriter{w: &lc.text, mu: &lc.mu}, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(teeHandler{text, recHandler{mu: &lc.mu, recs: &lc.recs}})
}

func (lc *logCapture) all() string {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.text.String() + "\n" + strings.Join(lc.recs, "\n")
}

// waitFor polls until the raw records contain want.
func (lc *logCapture) waitFor(t *testing.T, want string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		out := lc.all()
		if strings.Contains(out, want) {
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("log never contained %q: %s", want, out)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type teeHandler [2]slog.Handler

func (t teeHandler) Enabled(context.Context, slog.Level) bool { return true }
func (t teeHandler) WithAttrs([]slog.Attr) slog.Handler       { return t }
func (t teeHandler) WithGroup(string) slog.Handler            { return t }
func (t teeHandler) Handle(ctx context.Context, r slog.Record) error {
	return errors.Join(t[0].Handle(ctx, r.Clone()), t[1].Handle(ctx, r.Clone()))
}

// canary returns a distinctive printable string of exactly n bytes.
func canary(tag string, n int) string {
	s := tag
	for i := 0; len(s) < n; i++ {
		s += fmt.Sprintf("%x", i%16)
	}
	return s[:n]
}

// assertNoCanary fails if out holds the canary or any prefix of it at least
// 8 bytes long (shorter prefixes are not distinctive).
func assertNoCanary(t *testing.T, out, name, c string) {
	t.Helper()
	if strings.Contains(out, c[:8]) {
		t.Fatalf("%s canary (or a prefix) logged: %s", name, out)
	}
}

const (
	authLogUser = "alice-configured"
	authLogPass = "s3cret-configured"
)

func TestAuthFailLogNoUser(t *testing.T) {
	userC := canary("UCANARYz", 255)
	passC := canary("PCANARYz", 255)

	t.Run("auth_failed", func(t *testing.T) {
		var lc logCapture
		_, addr := startServer(t, Options{Dialer: newFakeDialer(), Username: authLogUser, Password: authLogPass, Logger: lc.logger()})
		if st := authFrom(t, addr, "127.0.0.1", userC, passC); st != 0x01 {
			t.Fatalf("status = %#x, want 0x01", st)
		}
		out := lc.waitFor(t, "auth_failed")
		assertNoCanary(t, out, "username", userC)
		assertNoCanary(t, out, "password", passC)
	})

	t.Run("auth_throttled", func(t *testing.T) {
		var lc logCapture
		_, addr := startServer(t, Options{Dialer: newFakeDialer(), Username: authLogUser, Password: authLogPass, Logger: lc.logger()})
		for range authFailLimit {
			_ = authFrom(t, addr, "127.0.0.1", userC, passC)
		}
		if st := authFrom(t, addr, "127.0.0.1", userC, passC); st != 0x01 {
			t.Fatalf("throttled status = %#x, want 0x01", st)
		}
		out := lc.waitFor(t, "auth_throttled")
		assertNoCanary(t, out, "username", userC)
		assertNoCanary(t, out, "password", passC)
	})

	t.Run("request_rejected_logs_configured_user", func(t *testing.T) {
		var lc logCapture
		_, addr := startServer(t, Options{Dialer: newFakeDialer(), Username: authLogUser, Password: authLogPass, Logger: lc.logger()})
		c := dialServer(t, addr)
		greet(t, c, 0x02)
		if st := userPass(t, c, authLogUser, authLogPass); st != 0x00 {
			t.Fatalf("auth status = %#x", st)
		}
		mustWrite(t, c, requestBytes(0x02, 0x01, []byte{10, 0, 0, 1}, 80)) // BIND: unsupported
		_ = readReply(t, c)
		out := lc.waitFor(t, "socks request rejected")
		if !strings.Contains(out, "user="+authLogUser) {
			t.Fatalf("request rejected line lacks configured user: %s", out)
		}
		assertNoCanary(t, out, "password", authLogPass)
	})

	t.Run("connect_failed_logs_configured_user", func(t *testing.T) {
		var lc logCapture
		fd := newFakeDialer()
		fd.err = errors.New("dial refused")
		_, addr := startServer(t, Options{Dialer: fd, Username: authLogUser, Password: authLogPass, Logger: lc.logger()})
		c := dialServer(t, addr)
		greet(t, c, 0x02)
		if st := userPass(t, c, authLogUser, authLogPass); st != 0x00 {
			t.Fatalf("auth status = %#x", st)
		}
		mustWrite(t, c, requestBytes(0x01, 0x01, []byte{10, 0, 0, 1}, 80))
		_ = readReply(t, c)
		out := lc.waitFor(t, "socks connect failed")
		if !strings.Contains(out, "user="+authLogUser) {
			t.Fatalf("connect failed line lacks configured user: %s", out)
		}
		assertNoCanary(t, out, "password", authLogPass)
	})
}
