package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

// seqReader yields scripted bytes, then io.EOF.
func seqReader(b ...byte) io.Reader { return bytes.NewReader(b) }

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// cycleReader repeats a byte pattern forever.
type cycleReader struct {
	p []byte
	i int
}

func (c *cycleReader) Read(b []byte) (int, error) {
	for k := range b {
		b[k] = c.p[c.i%len(c.p)]
		c.i++
	}
	return len(b), nil
}

func TestNewSessionIDAlnum(t *testing.T) {
	id, err := NewSessionID(&cycleReader{p: []byte{0, 10, 36, 61}}, SessionAlnum, 8)
	if err != nil {
		t.Fatal(err)
	}
	if id != "AKk9AKk9" {
		t.Fatalf("id %q", id)
	}
	if !validSessionID(id, SessionAlnum, 8) {
		t.Fatalf("invalid id %q", id)
	}
}

func TestNewSessionIDDigitsFirstNonZero(t *testing.T) {
	id, err := NewSessionID(&cycleReader{p: []byte{0}}, SessionDigits, 4)
	if err != nil {
		t.Fatal(err)
	}
	if id != "1000" {
		t.Fatalf("got %q want 1000", id)
	}
	id, err = NewSessionID(&cycleReader{p: []byte{8, 9}}, SessionDigits, 3)
	if err != nil || id != "998" {
		t.Fatalf("got %q %v", id, err)
	}
}

func TestNewSessionIDDiffersFromPrevious(t *testing.T) {
	// first draw "AAAA" collides with prev, second "BBBB" differs.
	id, err := NewSessionID(seqReader(10, 10, 10, 10, 11, 11, 11, 11), SessionAlnum, 4, "KKKK")
	if err != nil {
		t.Fatal(err)
	}
	if id != "LLLL" {
		t.Fatalf("got %q", id)
	}
}

func TestNewSessionIDReaderFailure(t *testing.T) {
	id, err := NewSessionID(errReader{}, SessionAlnum, 10)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v", err)
	}
	if id != "" {
		t.Fatalf("id = %q", id)
	}
}

func TestNewSessionIDRejectionSampling(t *testing.T) {
	// 255, 248 are out of range for alnum (limit 248); 250 and 252 for digits.
	id, err := NewSessionID(seqReader(255, 248, 251, 0), SessionAlnum, 2)
	if err == nil && id != "" {
		// 251 rejected too (>=248); expect only "0" accepted then EOF -> error.
		t.Fatalf("unexpected id %q", id)
	}
	id, err = NewSessionID(seqReader(255, 249, 1, 247), SessionAlnum, 2)
	if want := string([]byte{sessionAlnumChars[1], sessionAlnumChars[247%62]}); err != nil || id != want {
		t.Fatalf("got %q %v want %q", id, err, want)
	}
	// digits: first char limit 252 (9*28), later chars limit 250.
	id, err = NewSessionID(seqReader(252, 255, 0, 250, 255, 5), SessionDigits, 2)
	if err != nil || id != "15" {
		t.Fatalf("got %q %v", id, err)
	}
}

func TestNewSessionIDBadArgs(t *testing.T) {
	if _, err := NewSessionID(seqReader(1), "weird", 4); err == nil {
		t.Fatal("unknown charset accepted")
	}
	if _, err := NewSessionID(seqReader(1), SessionAlnum, 0); err == nil {
		t.Fatal("length 0 accepted")
	}
}

func sessEP(user, pass string) Endpoint {
	e := proxyEndpoint("p", KindSOCKS5)
	e.Username, e.Password = user, pass
	e.Session = Session{Charset: SessionAlnum, Length: 10}
	return e
}

func TestValidateSessionRules(t *testing.T) {
	const u, p = "SECRETUSER", "SECRETPASS"
	long := strings.Repeat("x", 250)
	cases := []struct {
		name  string
		mut   func(*Endpoint)
		field string // expected failing field; "" = valid
	}{
		{"valid username token", func(e *Endpoint) { e.Username = u + "-{session}" }, ""},
		{"valid password token", func(e *Endpoint) { e.Username = u; e.Password = p + "{session}" }, ""},
		{"empty current_id ok", func(e *Endpoint) { e.Username = u + "{session}"; e.Session.CurrentID = "" }, ""},
		{"zero tokens", func(e *Endpoint) { e.Username = u; e.Password = p }, "endpoints[0].session"},
		{"two tokens", func(e *Endpoint) { e.Username = u + "{session}"; e.Password = p + "{session}" }, "endpoints[0].session"},
		{"two in one field", func(e *Endpoint) { e.Username = u + "{session}{session}" }, "endpoints[0].session"},
		{"username only token", func(e *Endpoint) { e.Username = "{session}"; e.Password = p }, "endpoints[0].username"},
		{"password only token", func(e *Endpoint) { e.Username = u; e.Password = "{session}" }, "endpoints[0].password"},
		{"id wrong length", func(e *Endpoint) { e.Username = u + "{session}"; e.Session.CurrentID = "abc" }, "endpoints[0].session.current_id"},
		{"id bad charset", func(e *Endpoint) { e.Username = u + "{session}"; e.Session.CurrentID = "abcdef-hij" }, "endpoints[0].session.current_id"},
		{"digits leading zero", func(e *Endpoint) {
			e.Username = u + "{session}"
			e.Session = Session{Charset: SessionDigits, Length: 4, CurrentID: "0123"}
		}, "endpoints[0].session.current_id"},
		{"digits ok", func(e *Endpoint) {
			e.Username = u + "{session}"
			e.Session = Session{Charset: SessionDigits, Length: 4, CurrentID: "1230"}
		}, ""},
		{"rendered too long", func(e *Endpoint) { e.Username = long + "{session}" }, "endpoints[0].username"},
		{"rendered exactly 255", func(e *Endpoint) { e.Username = strings.Repeat("x", 245) + "{session}" }, ""},
		{"length 0", func(e *Endpoint) { e.Username = u + "{session}"; e.Session.Length = 0 }, "endpoints[0].session.length"},
		{"length 33", func(e *Endpoint) { e.Username = u + "{session}"; e.Session.Length = 33 }, "endpoints[0].session.length"},
		{"unknown charset", func(e *Endpoint) { e.Username = u + "{session}"; e.Session.Charset = "hex" }, "endpoints[0].session.charset"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := sessEP(u, p)
			c.mut(&e)
			errs := ValidateEndpoint("endpoints[0]", e)
			if c.field == "" {
				if len(errs) != 0 {
					t.Fatalf("unexpected: %v", errs)
				}
				return
			}
			if !hasField(errs, c.field) {
				t.Fatalf("want error on %s, got %v", c.field, errs)
			}
			msg := errs.Error()
			for _, s := range []string{e.Username, e.Password} {
				if s != "" && s != SessionToken && strings.Contains(msg, s) {
					t.Fatalf("error leaks %q: %s", s, msg)
				}
			}
			if strings.Contains(msg, "SECRET") {
				t.Fatalf("error leaks secret: %s", msg)
			}
		})
	}
}

func TestValidateSessionHTTPSkips255(t *testing.T) {
	e := sessEP(strings.Repeat("x", 300)+"{session}", "")
	e.Kind = KindHTTP
	if errs := ValidateEndpoint("endpoints[0]", e); len(errs) != 0 {
		t.Fatalf("http endpoint rejected: %v", errs)
	}
}

func TestValidateSessionThroughValidate(t *testing.T) {
	c := Default()
	c.Endpoints = []Endpoint{sessEP("u-{session}", "")}
	if err := Validate(c); err != nil {
		t.Fatal(err)
	}
}

func TestSessionZeroValueRoundTrip(t *testing.T) {
	e := proxyEndpoint("p", KindSOCKS5)
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "session") {
		t.Fatalf("unset session serialised: %s", b)
	}
	e.Session = Session{Charset: SessionAlnum, Length: 10, CurrentID: "abc"}
	b, _ = json.Marshal(e)
	var back Endpoint
	if err := json.Unmarshal(b, &back); err != nil || back.Session != e.Session {
		t.Fatalf("round trip: %v %+v", err, back.Session)
	}
}
