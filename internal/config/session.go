package config

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// SessionToken is the literal placeholder in an endpoint's username or password that the
// egress layer replaces with Session.CurrentID.
const SessionToken = "{session}"

// Session charsets.
const (
	SessionAlnum  = "alnum"
	SessionDigits = "digits"
)

// Session length bounds and default.
const (
	MinSessionLength     = 1
	MaxSessionLength     = 32
	DefaultSessionLength = 10
)

const (
	sessionAlnumChars  = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	sessionDigitsChars = "0123456789"
)

// Session describes a rotating sticky-session ID for a socks5/http endpoint.
//
// It is a plain value, not a pointer, so reflect.DeepEqual sees CurrentID changes. The ZERO VALUE
// means "no session": it is omitted from JSON (omitzero) and Validate skips session checks. Any
// non-zero value is "present" and must be fully valid. CurrentID is server-owned.
type Session struct {
	Charset   string `json:"charset,omitempty"`
	Length    int    `json:"length,omitempty"`
	CurrentID string `json:"current_id,omitempty"`
}

// sessionAlphabet returns the allowed characters for charset, or "" if unknown.
func sessionAlphabet(charset string) string {
	switch charset {
	case SessionAlnum:
		return sessionAlnumChars
	case SessionDigits:
		return sessionDigitsChars
	}
	return ""
}

// validSessionID reports whether id fits charset and length (digits: first character 1-9).
func validSessionID(id, charset string, length int) bool {
	alpha := sessionAlphabet(charset)
	if alpha == "" || len(id) != length || length < 1 {
		return false
	}
	for i := 0; i < len(id); i++ {
		if !strings.ContainsRune(alpha, rune(id[i])) {
			return false
		}
	}
	return !(charset == SessionDigits && id[0] == '0')
}

// SessionIDFits reports whether id still matches charset and length. The API uses it to decide
// whether a stored current_id must be regenerated.
func SessionIDFits(id, charset string, length int) bool { return validSessionID(id, charset, length) }

// NewSessionID draws a uniformly random ID from r (production: crypto/rand.Reader) using rejection
// sampling, so there is no modulo bias. The result never equals prev (redraw on collision). A
// reader failure returns the error and an empty ID.
func NewSessionID(r io.Reader, charset string, length int, prev ...string) (string, error) {
	alpha := sessionAlphabet(charset)
	if alpha == "" {
		return "", errors.New("unknown session charset")
	}
	if length < MinSessionLength || length > MaxSessionLength {
		return "", fmt.Errorf("session length must be between %d and %d", MinSessionLength, MaxSessionLength)
	}
	var last string
	if len(prev) > 0 {
		last = prev[0]
	}
	for {
		id, err := drawSessionID(r, charset, alpha, length)
		if err != nil {
			return "", err
		}
		if id != last {
			return id, nil
		}
	}
}

func drawSessionID(r io.Reader, charset, alpha string, length int) (string, error) {
	out := make([]byte, length)
	for i := range out {
		chars := alpha
		if i == 0 && charset == SessionDigits {
			chars = alpha[1:] // first digit 1-9
		}
		n := len(chars)
		limit := 256 - 256%n // accept bytes < limit
		for {
			var b [1]byte
			if _, err := io.ReadFull(r, b[:]); err != nil {
				return "", err
			}
			if int(b[0]) < limit {
				out[i] = chars[int(b[0])%n]
				break
			}
		}
	}
	return string(out), nil
}
