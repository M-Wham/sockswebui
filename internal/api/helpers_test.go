package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/M-Wham/sockswebui/internal/config"
)

// testParams keeps argon2id cheap under -race; production uses defaultParams.
var testParams = argonParams{Time: 1, MemoryKiB: 1024, Threads: 1, KeyLen: 32, SaltLen: 16}

type testEnv struct {
	t     *testing.T
	dir   string
	store *config.Store
	srv   *Server
	logs  *bytes.Buffer
	ctrl  *fakeCtrl
	h     http.Handler
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	st, err := config.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	logs := &bytes.Buffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	srv := newServer(st, log, testParams)
	ctrl := newFakeCtrl()
	srv.ctrl = ctrl
	// Test-only protected routes so the middleware can be exercised before real routes exist.
	srv.mux.HandleFunc("GET /api/test", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	srv.mux.HandleFunc("POST /api/test", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	srv.mux.HandleFunc("PUT /api/test", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	srv.mux.HandleFunc("DELETE /api/test", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return &testEnv{t: t, dir: dir, store: st, srv: srv, logs: logs, ctrl: ctrl, h: srv}
}

type reqOpt func(*http.Request)

func withCookie(c *http.Cookie) reqOpt { return func(r *http.Request) { r.AddCookie(c) } }
func withCSRF(tok string) reqOpt       { return func(r *http.Request) { r.Header.Set(csrfHeader, tok) } }

func (e *testEnv) do(method, path, body string, opts ...reqOpt) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

// setupRemote is the client address e.setup posts from.
const setupRemote = "203.0.113.250:1"

func (e *testEnv) setup(pw string) {
	e.t.Helper()
	// Setup shares the auth budget; a dedicated client keeps it out of the
	// budget of the default remote that tests log in from.
	rec := e.do("POST", "/api/setup", `{"password":"`+pw+`"}`, withRemote(setupRemote))
	if rec.Code != http.StatusNoContent {
		e.t.Fatalf("setup: status %d body %s", rec.Code, rec.Body)
	}
}

// login performs setup-independent login and returns the session cookie and CSRF token.
func (e *testEnv) login(pw string) (*http.Cookie, string) {
	e.t.Helper()
	rec := e.do("POST", "/api/login", `{"password":"`+pw+`"}`)
	if rec.Code != http.StatusOK {
		e.t.Fatalf("login: status %d body %s", rec.Code, rec.Body)
	}
	var body struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.CSRFToken == "" {
		e.t.Fatalf("login body %q: %v", rec.Body, err)
	}
	return sessionCookie(e.t, rec), body.CSRFToken
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			return c
		}
	}
	t.Fatalf("no %s cookie in response", sessionCookieName)
	return nil
}

func decodeFieldErrors(t *testing.T, rec *httptest.ResponseRecorder) []config.FieldError {
	t.Helper()
	var body struct {
		Errors []config.FieldError `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode errors %q: %v", rec.Body, err)
	}
	return body.Errors
}
