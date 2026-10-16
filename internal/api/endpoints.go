package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/M-Wham/sockswebui/internal/config"
)

// maxEndpointBody caps JSON request bodies (endpoints, slots, settings).
const maxEndpointBody = 1 << 20

// errEndpointNotFound aborts a store.Update when the target id does not exist.
var errEndpointNotFound = errors.New("endpoint not found")

// registerEndpoints wires the endpoint CRUD and import routes.
func (s *Server) registerEndpoints() {
	s.mux.HandleFunc("GET /api/endpoints", s.handleListEndpoints)
	s.mux.HandleFunc("POST /api/endpoints", s.handleCreateEndpoint)
	s.mux.HandleFunc("POST /api/endpoints/import", s.handleImportEndpoints)
	s.mux.HandleFunc("GET /api/endpoints/{id}", s.handleGetEndpoint)
	s.mux.HandleFunc("PUT /api/endpoints/{id}", s.handleUpdateEndpoint)
	s.mux.HandleFunc("DELETE /api/endpoints/{id}", s.handleDeleteEndpoint)
	s.mux.HandleFunc("POST /api/endpoints/{id}/rotate", s.handleRotateEndpoint)
}

func (s *Server) handleListEndpoints(w http.ResponseWriter, _ *http.Request) {
	eps := s.store.Get().Endpoints
	out := endpointList{Endpoints: make([]endpointResponse, len(eps))}
	for i, e := range eps {
		out.Endpoints[i] = toEndpointResponse(e)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetEndpoint(w http.ResponseWriter, r *http.Request) {
	cfg := s.store.Get()
	i := endpointIndex(cfg.Endpoints, r.PathValue("id"))
	if i < 0 {
		writeError(w, http.StatusNotFound, "endpoint not found")
		return
	}
	writeJSON(w, http.StatusOK, toEndpointResponse(cfg.Endpoints[i]))
}

func (s *Server) handleCreateEndpoint(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeEndpointRequest(w, r)
	if !ok {
		return
	}
	var created config.Endpoint
	var prefix string
	err := s.store.Update(func(c *config.Config) error {
		id, err := newEndpointID(c.Endpoints)
		if err != nil {
			return err
		}
		created = req.toEndpoint(id, config.Endpoint{})
		if err := ensureSessionID(&created, s.randReader()); err != nil {
			return err
		}
		prefix = endpointPath(len(c.Endpoints))
		c.Endpoints = append(c.Endpoints, created)
		return nil
	})
	if err != nil {
		s.writeStoreError(w, "create endpoint", err, prefix)
		return
	}
	s.log.Info("endpoint created", "id", created.ID, "kind", created.Kind)
	w.Header().Set("Location", "/api/endpoints/"+created.ID)
	writeJSON(w, http.StatusCreated, toEndpointResponse(created))
}

func (s *Server) handleUpdateEndpoint(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if endpointIndex(s.store.Get().Endpoints, id) < 0 {
		writeError(w, http.StatusNotFound, "endpoint not found")
		return
	}
	req, ok := decodeEndpointRequest(w, r)
	if !ok {
		return
	}
	// startMu (then the store lock) spans persist..apply, so a start of a slot routed
	// through this endpoint cannot interleave (fail closed).
	s.startMu.Lock()
	defer s.startMu.Unlock()
	var updated config.Endpoint
	var prefix string
	err := s.store.Update(func(c *config.Config) error {
		i := endpointIndex(c.Endpoints, id)
		if i < 0 {
			return errEndpointNotFound
		}
		updated = req.toEndpoint(id, c.Endpoints[i])
		if err := ensureSessionID(&updated, s.randReader()); err != nil {
			return err
		}
		prefix = endpointPath(i)
		c.Endpoints[i] = updated
		return nil
	})
	switch {
	case errors.Is(err, errEndpointNotFound):
		writeError(w, http.StatusNotFound, "endpoint not found")
	case err != nil:
		s.writeStoreError(w, "update endpoint", err, prefix)
	default:
		s.log.Info("endpoint updated", "id", id, "kind", updated.Kind)
		s.applyConfig("update endpoint") // slots routed through it are rebuilt
		writeJSON(w, http.StatusOK, toEndpointResponse(updated))
	}
}

func (s *Server) handleDeleteEndpoint(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := s.store.Update(func(c *config.Config) error {
		i := endpointIndex(c.Endpoints, id)
		if i < 0 {
			return errEndpointNotFound
		}
		if ve := endpointInUse(c.Slots, id); ve != nil {
			return ve
		}
		c.Endpoints = append(c.Endpoints[:i], c.Endpoints[i+1:]...)
		return nil
	})
	switch {
	case errors.Is(err, errEndpointNotFound):
		writeError(w, http.StatusNotFound, "endpoint not found")
	case err != nil:
		s.writeStoreError(w, "delete endpoint", err, "")
	default:
		s.log.Info("endpoint deleted", "id", id)
		w.WriteHeader(http.StatusNoContent)
	}
}

// endpointInUse returns a field error on "id" for every slot whose route uses id.
func endpointInUse(slots []config.Slot, id string) config.ValidationErrors {
	var ve config.ValidationErrors
	for _, sl := range slots {
		for _, hop := range sl.Route {
			if hop == id {
				ve = append(ve, config.FieldError{Field: "id",
					Message: fmt.Sprintf("endpoint is used in the route of slot %d; remove it from the route first", sl.ID)})
				break
			}
		}
	}
	return ve
}

// decodeEndpointRequest reads a size-limited JSON body; on failure it writes a 400.
func decodeEndpointRequest(w http.ResponseWriter, r *http.Request) (endpointRequest, bool) {
	var req endpointRequest
	ok := decodeJSON(w, r, &req, "endpoint")
	return req, ok
}

// decodeJSON reads exactly one JSON object of at most maxEndpointBody bytes into v.
// On failure it writes a 400 with a "body" field error and returns false.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any, what string) bool {
	if !requireJSON(w, r) {
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxEndpointBody))
	err := dec.Decode(v)
	if err == nil && dec.Decode(&struct{}{}) != io.EOF {
		err = errors.New("trailing data")
	}
	if err == nil {
		return true
	}
	msg := "must be a single JSON " + what + " object"
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		msg = fmt.Sprintf("must be at most %d bytes", maxEndpointBody)
	}
	writeFieldErrors(w, []config.FieldError{{Field: "body", Message: msg}})
	return false
}

func endpointIndex(eps []config.Endpoint, id string) int {
	for i, e := range eps {
		if e.ID == id {
			return i
		}
	}
	return -1
}

// newEndpointID returns a random "ep-<16 hex>" id not already in eps.
func newEndpointID(eps []config.Endpoint) (string, error) {
	for {
		b := make([]byte, 8)
		if _, err := rand.Read(b); err != nil {
			return "", fmt.Errorf("generate endpoint id: %w", err)
		}
		id := "ep-" + hex.EncodeToString(b)
		if endpointIndex(eps, id) < 0 {
			return id, nil
		}
	}
}
