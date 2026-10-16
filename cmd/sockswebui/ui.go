package main

import (
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"

	"github.com/M-Wham/sockswebui/internal/api"
	"github.com/M-Wham/sockswebui/internal/config"
)

// newHandler mounts the API at /api/ and the embedded UI at /. store and ctrl must be non-nil.
// apiOpts carries build-specific API extras (none in release builds).
// names is the accepted UI name list built once at boot (bootNames).
// Layer order is fixed: securityHeaders, then the Host guard, then the mux;
// the API checks Origin against the same names before requireAuth.
func newHandler(store *config.Store, log *slog.Logger, ctrl api.SlotController, uiFS fs.FS,
	names api.NameList, apiOpts ...api.Option) http.Handler {
	base := []api.Option{api.WithAppVersion(version), api.WithNameList(names)}
	apiH := api.New(store, log, ctrl, append(base, apiOpts...)...)
	mux := http.NewServeMux()
	mux.Handle("/api", apiH)
	mux.Handle("/api/", apiH)
	mux.Handle("/", newUIHandler(uiFS))
	return closingHandler{Handler: securityHeaders(api.HostGuard(mux, names)), closer: apiH.(io.Closer)}
}

// closingHandler lets serveUI stop the API's log-aggregator ticker at shutdown.
type closingHandler struct {
	http.Handler
	closer io.Closer
}

func (c closingHandler) Close() error { return c.closer.Close() }

// uiContentTypes pins types that Go's builtin mime table lacks, so a host
// without /etc/mime.types does not serve them as octet-stream under nosniff.
var uiContentTypes = map[string]string{".woff2": "font/woff2"}

// newUIHandler serves the embedded UI filesystem at /. Directories are never listed:
// a directory with an index.html serves it, any other directory is 404.
func newUIHandler(uiFS fs.FS) http.Handler {
	files := http.FileServer(http.FS(uiFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "."
		}
		if fi, err := fs.Stat(uiFS, name); err == nil && fi.IsDir() {
			if _, err := fs.Stat(uiFS, path.Join(name, "index.html")); err != nil {
				http.NotFound(w, r)
				return
			}
		}
		if ct, ok := uiContentTypes[path.Ext(name)]; ok {
			w.Header().Set("Content-Type", ct)
		}
		files.ServeHTTP(w, r)
	})
}
