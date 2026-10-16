package importer

import (
	"net"
	"net/netip"
	"path"
	"strings"

	"github.com/M-Wham/sockswebui/internal/config"
)

// Per-file import statuses.
const (
	StatusImported         = "imported"
	StatusSkippedDuplicate = "skipped-duplicate"
	StatusError            = "error"
)

// Result is the outcome for one uploaded file or zip entry. Zip entries are named
// "<zip>/<entry>". Endpoint points into the added slice returned by Import (nil unless
// Status is imported). Reason and Warnings never contain key material.
type Result struct {
	File     string
	Status   string
	Reason   string
	Warnings []string
	Endpoint *config.Endpoint
}

// Import parses a batch of uploads in order. `.conf` files are parsed with
// ParseFile; `.zip` files are expanded with ExpandZip and each entry parsed; anything else is a
// per-file error. A WireGuard config whose peer public key + endpoint matches an existing
// endpoint, or one imported earlier in the same batch, is skipped-duplicate. A bad file never
// stops the rest of the batch. IDs are not assigned. Pure: no I/O; existing is not modified.
func Import(existing []config.Endpoint, uploads []File) ([]Result, []config.Endpoint) {
	seen := make(map[string]bool, len(existing))
	for _, e := range existing {
		if e.Kind == config.KindWireGuard {
			seen[dedupeKey(e)] = true
		}
	}
	var results []Result
	var added []config.Endpoint
	idx := map[int]int{} // result index -> added index
	parse := func(name string, f File) {
		r := Result{File: name}
		if f.Err != "" {
			r.Status, r.Reason = StatusError, f.Err
			results = append(results, r)
			return
		}
		e, warns, err := ParseFile(name, f.Data)
		switch {
		case err != nil:
			r.Status, r.Reason = StatusError, err.Error()
		case seen[dedupeKey(e)]:
			r.Status, r.Reason = StatusSkippedDuplicate, "an endpoint with the same peer public key and endpoint already exists"
		default:
			seen[dedupeKey(e)] = true
			r.Status, r.Warnings = StatusImported, warns
			idx[len(results)] = len(added)
			added = append(added, e)
		}
		results = append(results, r)
	}
	for _, u := range uploads {
		switch strings.ToLower(path.Ext(baseName(u.Name))) {
		case ".conf":
			parse(u.Name, u)
		case ".zip":
			files, err := ExpandZip(u.Data)
			if err != nil {
				results = append(results, Result{File: u.Name, Status: StatusError, Reason: err.Error()})
				continue
			}
			for _, f := range files {
				parse(u.Name+"/"+f.Name, f)
			}
		default:
			results = append(results, Result{File: u.Name, Status: StatusError,
				Reason: "unsupported file type (expected .conf or .zip)"})
		}
	}
	for ri, ai := range idx {
		results[ri].Endpoint = &added[ai]
	}
	return results, added
}

// dedupeKey is the peer public key (exact) plus the endpoint with a lower-cased hostname or
// canonical IP literal, so equivalent spellings of the same host:port collide.
func dedupeKey(e config.Endpoint) string {
	return e.PeerPublicKey + "|" + normalizeHostPort(e.Endpoint)
}

func normalizeHostPort(hp string) string {
	host, port, err := net.SplitHostPort(hp)
	if err != nil {
		return strings.ToLower(hp)
	}
	if a, err := netip.ParseAddr(host); err == nil {
		host = a.Unmap().String()
	} else {
		host = strings.TrimSuffix(strings.ToLower(host), ".")
	}
	return net.JoinHostPort(host, port)
}
