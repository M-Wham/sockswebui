package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
)

const (
	// maxExportBody bounds the export request: two 1024-byte strings even fully \u-escaped.
	maxExportBody       = 16 << 10
	minExportPassphrase = 12   // bytes
	maxExportPassphrase = 1024 // bytes
	maxExportHostLen    = 63
)

// exportRequest is the POST /api/config/export body. Pointers distinguish
// absent from empty.
type exportRequest struct {
	CurrentPassword *string `json:"current_password"`
	Mode            *string `json:"mode"`
	Passphrase      *string `json:"passphrase"`
}

// handleExportConfig serves POST /api/config/export. Order: login rate limit
// (shared per-IP bucket) → hash queue (503 when full) → body size limit → decode/shape check → admin password → work.
func (s *Server) handleExportConfig(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) { // before the limiter and argon2
		return
	}
	if !s.limitAuth(w, r, "export") {
		return
	}
	if !tryEnterHashQueue(w) {
		return
	}
	defer leaveHashQueue()
	var req exportRequest
	if !decodeExportRequest(w, r, &req) {
		return
	}
	mode, errs := exportRequestErrors(req)
	if len(errs) > 0 {
		writeFieldErrors(w, errs)
		return
	}
	cfg := s.store.Get()
	if !s.verifyAdminPassword(w, r, "config export", *req.CurrentPassword, cfg.Settings.AdminPasswordHash) {
		return
	}
	// The payload type carries only endpoints and slots: the admin hash, session secret and
	// UI bind address can never enter the file.
	payload := config.ExportPayload{Endpoints: cfg.Endpoints, Slots: cfg.Slots}
	now := s.clock()
	var file []byte
	var err error
	if mode == config.ExportModeEncrypted {
		file, err = config.SealEncrypted(s.randReader(), payload, []byte(*req.Passphrase),
			s.exportKDF(r.Context()), now, s.appVersion)
	} else {
		file, err = config.SealNoSecrets(payload, now, s.appVersion)
	}
	if r.Context().Err() != nil {
		return // client gone while waiting for a hash slot
	}
	if err != nil {
		s.log.Error("config export: build file", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Disposition", `attachment; filename="`+exportFilename(s.osHostname(), now)+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(file)
	s.log.Info("config exported", "mode", mode, "remote", clientKey(r))
}

// decodeExportRequest reads one strict JSON object of at most maxExportBody bytes. An
// oversize body is 413; anything else malformed is a 400 "body" field error.
func decodeExportRequest(w http.ResponseWriter, r *http.Request, req *exportRequest) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExportBody))
	dec.DisallowUnknownFields()
	err := dec.Decode(req)
	if err == nil {
		if _, terr := dec.Token(); terr != io.EOF {
			err = errors.New("trailing data")
			var mbe *http.MaxBytesError
			if errors.As(terr, &mbe) {
				err = terr
			}
		}
	}
	if err == nil {
		return true
	}
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("request body must be at most %d bytes", maxExportBody))
		return false
	}
	writeFieldErrors(w, []config.FieldError{{Field: "body",
		Message: "must be a JSON object with current_password, mode and passphrase"}})
	return false
}

// exportRequestErrors checks the request shape before any argon2 work and returns the mode
// (encrypted when absent).
func exportRequestErrors(req exportRequest) (string, []config.FieldError) {
	var errs []config.FieldError
	switch {
	case req.CurrentPassword == nil:
		errs = append(errs, config.FieldError{Field: "current_password", Message: "is required"})
	case len(*req.CurrentPassword) > maxPasswordLen:
		errs = append(errs, config.FieldError{Field: "current_password", Message: "must be at most 1024 bytes"})
	}
	mode := config.ExportModeEncrypted
	if req.Mode != nil {
		mode = *req.Mode
	}
	switch mode {
	case config.ExportModeEncrypted:
		switch {
		case req.Passphrase == nil:
			errs = append(errs, config.FieldError{Field: "passphrase", Message: "is required for an encrypted export"})
		case len(*req.Passphrase) < minExportPassphrase || len(*req.Passphrase) > maxExportPassphrase:
			errs = append(errs, config.FieldError{Field: "passphrase", Message: "must be 12 to 1024 bytes"})
		}
	case config.ExportModeNoSecrets:
		if req.Passphrase != nil {
			errs = append(errs, config.FieldError{Field: "passphrase", Message: "must not be sent with no-secrets"})
		}
	default:
		errs = append(errs, config.FieldError{Field: "mode", Message: "must be encrypted or no-secrets"})
	}
	return mode, errs
}

// exportFilename is sockswebui-<host>-<UTC yyyymmdd-hhmmss>.json.
func exportFilename(host string, now time.Time) string {
	return "sockswebui-" + exportHost(host) + "-" + now.UTC().Format("20060102-150405") + ".json"
}

// exportHost reduces a hostname to [A-Za-z0-9.-]{1,63}, falling back to "device". The
// result is safe inside a quoted Content-Disposition filename.
func exportHost(h string) string {
	var b strings.Builder
	for i := 0; i < len(h) && b.Len() < maxExportHostLen; i++ {
		c := h[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' {
			b.WriteByte(c)
		}
	}
	if b.Len() == 0 {
		return "device"
	}
	return b.String()
}

func (s *Server) osHostname() string {
	f := s.hostname
	if f == nil {
		f = os.Hostname
	}
	h, err := f()
	if err != nil {
		return ""
	}
	return h
}

func (s *Server) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}
