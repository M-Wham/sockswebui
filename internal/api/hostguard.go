package api

import "net/http"

// hostRefused is the 403 body for a Host outside the accepted name list. It names the
// accepted forms but never the device hostname or configured names.
type hostRefused struct {
	Error string `json:"error"`
	Hint  string `json:"hint"`
}

const hostRefusedHint = "open the UI by IP address, localhost, the device hostname or <hostname>.local, " +
	"or start sockswebui with --ui-host <name> for another name"

// HostGuard refuses (403) any request whose Host is empty or not in list, before
// next runs, so DNS-rebinding pages reach no handler, limiter or stream.
// It does not wrap the ResponseWriter.
func HostGuard(next http.Handler, list NameList) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !list.Allows(r.Host) {
			writeJSON(w, http.StatusForbidden, hostRefused{Error: "host not allowed", Hint: hostRefusedHint})
			return
		}
		next.ServeHTTP(w, r)
	})
}
