package main

import "net/http"

// Security headers sent on every response (clickjacking, MIME sniffing and referrer leaks).
const (
	headerCSP            = "default-src 'self'; frame-ancestors 'none'; base-uri 'none'"
	headerNoSniff        = "nosniff"
	headerReferrerPolicy = "no-referrer"
)

// securityHeaders is the outermost layer. It sets the headers before
// calling next, so early flushes (SSE) and error responses carry them, and it
// never wraps the ResponseWriter, so http.ResponseController flushing still works.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", headerCSP)
		h.Set("X-Content-Type-Options", headerNoSniff)
		h.Set("Referrer-Policy", headerReferrerPolicy)
		next.ServeHTTP(w, r)
	})
}
