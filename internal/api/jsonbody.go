package api

import (
	"mime"
	"net/http"
)

// requireJSON refuses a request whose Content-Type media type is not application/json
// (parameters such as charset are allowed). It writes 415 and returns false on refusal.
// A cross-site page can send a no-cors POST only with a simple Content-Type (text/plain,
// form-urlencoded, multipart), so this closes the no-preflight path.
func requireJSON(w http.ResponseWriter, r *http.Request) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err == nil && mt == "application/json" { // ParseMediaType lower-cases mt
		return true
	}
	writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
	return false
}
