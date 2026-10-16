package supervisor

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/egress"
)

// fakeKey is a freshly generated, never-real 32-byte base64 key.
func fakeKey(t *testing.T) string {
	t.Helper()
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b[:])
}

func TestLogRing(t *testing.T) {
	t.Run("keeps last 500 in order", func(t *testing.T) {
		s, _, _ := newTestSlot(t, rigSlot(1, "wgA"), nil)
		for i := 0; i < 600; i++ {
			s.log.Info("line", "i", i)
		}
		lines := s.Logs()
		if len(lines) != LogRingSize || LogRingSize != 500 {
			t.Fatalf("len = %d (size %d), want 500", len(lines), LogRingSize)
		}
		for j, l := range lines {
			if want := fmt.Sprintf("i=%d", 100+j); !strings.Contains(l.Text, want) {
				t.Fatalf("line %d = %q, want %s", j, l.Text, want)
			}
		}
		// Lines is a copy.
		lines[0].Text = "mutated"
		if s.Logs()[0].Text == "mutated" {
			t.Fatal("Logs returned the ring's backing array")
		}
	})

	t.Run("redacts secrets", func(t *testing.T) {
		priv, psk, proxyPW, socksPW := fakeKey(t), fakeKey(t), "proxy-"+fakeKey(t), "socks-"+fakeKey(t)
		eps := []config.Endpoint{
			{ID: "wgA", Kind: config.KindWireGuard, PrivateKey: priv, PresharedKey: psk},
			{ID: "up", Kind: config.KindSOCKS5, Username: "u", Password: proxyPW},
		}
		cfg := rigSlot(1, "wgA", "up")
		cfg.SocksAuth = &config.SocksAuth{Username: "client", Password: socksPW}
		s, _, procLog := newTestSlot(t, cfg, eps)
		secrets := []string{priv, psk, proxyPW, socksPW}
		for _, v := range secrets {
			s.log.Info("msg has "+v, "attr", "x"+v+"y", "err", errors.New("boom "+v))
			s.log.With("pre", v).WithGroup("g").Info("grouped", "inner", v)
		}
		lines := s.Logs()
		if len(lines) != 2*len(secrets) {
			t.Fatalf("got %d lines, want %d", len(lines), 2*len(secrets))
		}
		for _, l := range lines {
			if !strings.Contains(l.Text, redactedMark) {
				t.Fatalf("line not redacted: %q", l.Text)
			}
		}
		all := procLog.String()
		for _, l := range lines {
			all += "\n" + l.Text + l.Msg
		}
		for i, v := range secrets {
			if strings.Contains(all, v) {
				t.Fatalf("secret %d leaked into ring or process log", i)
			}
		}
		if !strings.Contains(procLog.String(), redactedMark) {
			t.Fatal("process log mirror missing redacted lines")
		}
	})

	t.Run("redacts hex key forms", func(t *testing.T) {
		priv, psk := fakeKey(t), fakeKey(t)
		eps := []config.Endpoint{{ID: "wgA", Kind: config.KindWireGuard, PrivateKey: priv, PresharedKey: psk}}
		s, _, procLog := newTestSlot(t, rigSlot(1, "wgA"), eps)
		var forms []string
		for _, k := range []string{priv, psk} {
			raw, _ := base64.StdEncoding.DecodeString(k)
			lo := hex.EncodeToString(raw)
			forms = append(forms, lo, strings.ToUpper(lo))
		}
		for _, v := range forms {
			s.log.Info("uapi private_key="+v, "err", errors.New("set preshared_key="+v))
		}
		all := procLog.String()
		for _, l := range s.Logs() {
			all += "\n" + l.Text
		}
		for i, v := range forms {
			if strings.Contains(all, v) {
				t.Fatalf("hex key form %d leaked: %s", i, all)
			}
		}
	})

	t.Run("every transition logged", func(t *testing.T) {
		s, ev, _ := newTestSlot(t, rigSlot(1, "wgA"), nil,
			withBuilder(func([]string, []config.Endpoint, bool) (egress.Chain, error) {
				return nil, errors.New("stub build failure")
			}))
		if err := s.Start(); err != nil {
			t.Fatal(err)
		}
		waitState(t, ev, StateError, 5*time.Second)
		_ = s.Stop()
		var got []string
		for _, l := range s.Logs() {
			if l.Msg == "slot state" {
				got = append(got, l.Text)
			}
		}
		want := []string{"to=STARTING", "to=ERROR", "to=STOPPED"}
		if len(got) != len(want) {
			t.Fatalf("transition lines = %q, want %v", got, want)
		}
		for i, w := range want {
			if !strings.Contains(got[i], w) {
				t.Fatalf("transition %d = %q, want %s", i, got[i], w)
			}
		}
		if s.Logs()[0].Level != slog.LevelInfo.String() {
			t.Fatalf("level = %q", s.Logs()[0].Level)
		}
	})
}
