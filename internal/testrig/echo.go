package testrig

import (
	"encoding/json"
	"net"
	"net/http"
	"time"
)

// EchoHandler answers every request with {"ip": <caller host>, "hop": hop}.
func EchoHandler(hop string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"ip": host, "hop": hop})
	})
}

// ServeEcho serves EchoHandler(hop) on ln until ln is closed. It returns a
// channel that is closed once the server goroutine has exited.
func ServeEcho(ln net.Listener, hop string) <-chan struct{} {
	return serve(ln, EchoHandler(hop))
}

func serve(ln net.Listener, h http.Handler) <-chan struct{} {
	done := make(chan struct{})
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	srv.SetKeepAlivesEnabled(false)
	go func() {
		defer close(done)
		_ = srv.Serve(ln) // returns once ln is closed
	}()
	return done
}

// EchoCountryHandler is EchoHandler plus a "country" field, mimicking
// Mullvad's am.i.mullvad.net/json response shape.
func EchoCountryHandler(hop, country string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"ip": host, "hop": hop, "country": country})
	})
}

// ServeEchoCountry serves EchoCountryHandler(hop, country) on ln until ln is
// closed; the returned channel closes once the server goroutine has exited.
func ServeEchoCountry(ln net.Listener, hop, country string) <-chan struct{} {
	return serve(ln, EchoCountryHandler(hop, country))
}
