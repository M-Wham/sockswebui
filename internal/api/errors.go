package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/M-Wham/sockswebui/internal/config"
)

// errorBody is the JSON error shape: {"error": "...", "errors": [{"field","message"}]}.
type errorBody struct {
	Error  string              `json:"error"`
	Errors []config.FieldError `json:"errors,omitempty"`
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}

func writeFieldErrors(w http.ResponseWriter, errs []config.FieldError) {
	writeJSON(w, http.StatusBadRequest, errorBody{Error: "invalid input", Errors: errs})
}

// asFieldErrors extracts validation errors from a store error. Paths under the given
// config prefix (e.g. "endpoints[2]") are rewritten relative to the request body
// ("endpoints[2].host" -> "host"); other paths are kept as-is.
func asFieldErrors(err error, prefix string) ([]config.FieldError, bool) {
	var ve config.ValidationErrors
	if !errors.As(err, &ve) {
		return nil, false
	}
	out := make([]config.FieldError, len(ve))
	for i, fe := range ve {
		if prefix != "" && strings.HasPrefix(fe.Field, prefix+".") {
			fe.Field = strings.TrimPrefix(fe.Field, prefix+".")
		}
		out[i] = fe
	}
	return out, true
}

// writeStoreError maps a failed store.Update to 400 (validation) or 500 (anything else).
func (s *Server) writeStoreError(w http.ResponseWriter, op string, err error, prefix string) {
	if fes, ok := asFieldErrors(err, prefix); ok {
		writeFieldErrors(w, fes)
		return
	}
	s.log.Error(op+": save config", "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func endpointPath(i int) string { return fmt.Sprintf("endpoints[%d]", i) }
