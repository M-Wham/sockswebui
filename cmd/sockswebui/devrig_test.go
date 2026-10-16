package main

import (
	"encoding/json"
	"net/http"
	"os/exec"
	"strings"
	"testing"
)

// The release (untagged) build must not link the testrig package or the testing
// package (release binary carries no testrig code).
func TestNoTestrigInRelease(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	for _, pkg := range strings.Fields(string(out)) {
		if pkg == "testing" || strings.HasSuffix(pkg, "/internal/testrig") {
			t.Errorf("release build links %s", pkg)
		}
	}
}

// loginCSRF logs in again on an already set-up server and returns the session
// cookie plus the CSRF token from the login body.
func loginCSRF(t *testing.T, base string) (*http.Cookie, string) {
	t.Helper()
	res, body := doReq(t, "POST", base+"/api/login", `{"password":"correct horse battery"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login: %d %s", res.StatusCode, body)
	}
	var b struct {
		CSRF string `json:"csrf_token"`
	}
	if json.Unmarshal([]byte(body), &b) != nil || b.CSRF == "" {
		t.Fatalf("login body %q", body)
	}
	for _, c := range res.Cookies() {
		if c.Value != "" {
			return c, b.CSRF
		}
	}
	t.Fatal("login set no cookie")
	return nil, ""
}

// postCSRF sends an authenticated state-changing request.
func postCSRF(t *testing.T, url string, c *http.Cookie, tok string) int {
	t.Helper()
	req, _ := http.NewRequest("POST", url, nil)
	if c != nil {
		req.AddCookie(c)
	}
	if tok != "" {
		req.Header.Set("X-CSRF-Token", tok)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	res.Body.Close()
	return res.StatusCode
}
