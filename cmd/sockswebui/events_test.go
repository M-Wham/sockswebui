package main

import (
	"bufio"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The API is wired to a live supervisor: /api/events streams a snapshot, and
// an open SSE stream does not stall graceful shutdown (exit code 0 at cleanup).
func TestRunEventsWiredAndShutdown(t *testing.T) {
	var res *http.Response
	// Registered before startRun's cleanup so the stream stays open during shutdown.
	t.Cleanup(func() {
		if res != nil {
			res.Body.Close()
		}
	})
	base := startRun(t, t.TempDir(), testFS())
	c := setupAndLogin(t, base)
	req, err := http.NewRequest("GET", base+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(c)
	res, err = (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status %d content-type %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	line, err := bufio.NewReader(res.Body).ReadString('\n')
	if err != nil || line != "event: snapshot\n" {
		t.Fatalf("first line %q: %v", line, err)
	}
	if res, body := doReq(t, "GET", base+"/api/slots/1/logs", "", c); res.StatusCode != http.StatusOK ||
		!strings.Contains(body, `"lines":[]`) {
		t.Fatalf("logs: %d %s", res.StatusCode, body)
	}
}
