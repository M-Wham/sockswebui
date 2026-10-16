package api

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/M-Wham/sockswebui/internal/config"
)

// maxImportConfigBody is the config import request limit.
const maxImportConfigBody = 1 << 20

// importConfigForm is the parsed multipart body of POST /api/config/import. Pointers
// distinguish absent from empty.
type importConfigForm struct {
	file            []byte
	hasFile         bool
	currentPassword *string
	passphrase      *string
}

// handleImportConfig serves POST /api/config/import. Order: login rate limit
// (shared per-IP bucket) → hash queue (503 when full) → 1 MiB size limit → form shape → admin password → decode/decrypt
// → session IDs → replace with backup (slots stopped) → apply. Its own handler: endpoint
// import (import.go) has a different limit and field.
func (s *Server) handleImportConfig(w http.ResponseWriter, r *http.Request) {
	if !s.limitAuth(w, r, "import") {
		return
	}
	if !tryEnterHashQueue(w) {
		return
	}
	defer leaveHashQueue()
	form, ok := readImportConfigForm(w, r)
	if !ok {
		return
	}
	if errs := importFormErrors(form); len(errs) > 0 {
		writeFieldErrors(w, errs)
		return
	}
	caller, err := r.Cookie(sessionCookieName)
	if err != nil { // requireAuth guarantees a cookie; fail closed regardless
		writeError(w, http.StatusUnauthorized, "login required")
		return
	}
	verifiedHash := s.store.Get().Settings.AdminPasswordHash
	if !s.verifyAdminPassword(w, r, "config import", *form.currentPassword, verifiedHash) {
		return
	}
	payload, mode, ok := s.openImportFile(w, r, form)
	if !ok {
		return
	}
	// A template without current_id gets one; a file's current_id is
	// kept (the one exception to "server-owned").
	for i := range payload.Endpoints {
		e := &payload.Endpoints[i]
		if e.Session != (config.Session{}) && e.Session.CurrentID == "" {
			if err := ensureSessionID(e, s.randReader()); err != nil {
				s.log.Error("config import: generate session id", "err", err)
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
		}
	}
	for i := range payload.Slots {
		payload.Slots[i].Enabled = false // nothing starts automatically
	}
	s.replaceImported(w, r, payload, mode, verifiedHash, caller.Value)
}

// readImportConfigForm reads the whole body (at most maxImportConfigBody, else 413) into
// memory, then parses it as multipart/form-data with exactly the parts file,
// current_password and an optional passphrase, each at most once. Anything else is 400.
func readImportConfigForm(w http.ResponseWriter, r *http.Request) (importConfigForm, bool) {
	var form importConfigForm
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxImportConfigBody))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("upload must be at most %d bytes", maxImportConfigBody))
			return form, false
		}
		writeError(w, http.StatusBadRequest, "could not read request body")
		return form, false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	mr, err := r.MultipartReader()
	if err != nil {
		writeFieldErrors(w, []config.FieldError{{Field: "body", Message: "must be multipart/form-data"}})
		return form, false
	}
	bad := func(field, msg string) (importConfigForm, bool) {
		writeFieldErrors(w, []config.FieldError{{Field: field, Message: msg}})
		return importConfigForm{}, false
	}
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			return form, true
		}
		if err != nil {
			return bad("body", "must be multipart/form-data")
		}
		data, err := io.ReadAll(p)
		if err != nil {
			return bad("body", "must be multipart/form-data")
		}
		switch name := p.FormName(); name {
		case "file":
			if form.hasFile {
				return bad("file", "exactly one file is allowed")
			}
			form.file, form.hasFile = data, true
		case "current_password":
			if form.currentPassword != nil {
				return bad("current_password", "must be sent once")
			}
			v := string(data)
			form.currentPassword = &v
		case "passphrase":
			if form.passphrase != nil {
				return bad("passphrase", "must be sent once")
			}
			v := string(data)
			form.passphrase = &v
		default:
			return bad("body", "unexpected part; allowed: file, current_password, passphrase")
		}
	}
}

// importFormErrors checks the form shape before any argon2 work.
func importFormErrors(f importConfigForm) []config.FieldError {
	var errs []config.FieldError
	if !f.hasFile {
		errs = append(errs, config.FieldError{Field: "file", Message: "is required"})
	}
	switch {
	case f.currentPassword == nil:
		errs = append(errs, config.FieldError{Field: "current_password", Message: "is required"})
	case len(*f.currentPassword) > maxPasswordLen:
		errs = append(errs, config.FieldError{Field: "current_password", Message: "must be at most 1024 bytes"})
	}
	if f.passphrase != nil && len(*f.passphrase) > maxExportPassphrase {
		errs = append(errs, config.FieldError{Field: "passphrase", Message: "must be at most 1024 bytes"})
	}
	return errs
}

// openImportFile runs steps 1-3: header checks (before any key derivation), decryption
// and payload decoding. A wrong passphrase and a tampered file share config.ErrDecrypt's
// one message; structural problems are 400 with their message; a bad endpoint id is a
// field error. Nothing from the file is logged.
func (s *Server) openImportFile(w http.ResponseWriter, r *http.Request, f importConfigForm) (config.ExportPayload, string, bool) {
	var pass []byte
	if f.passphrase != nil {
		pass = []byte(*f.passphrase)
	}
	payload, mode, err := config.OpenExport(f.file, pass, s.exportKDF(r.Context()))
	clear(pass)
	if r.Context().Err() != nil {
		return config.ExportPayload{}, "", false // client gone while waiting for a hash slot
	}
	if err == nil {
		return payload, mode, true
	}
	switch fes, isFields := asFieldErrors(err, ""); {
	case errors.Is(err, config.ErrDecrypt):
		writeError(w, http.StatusBadRequest, config.ErrDecrypt.Error())
	case errors.Is(err, config.ErrExportFormat):
		writeError(w, http.StatusBadRequest, err.Error())
	case isFields:
		writeFieldErrors(w, fes)
	default:
		s.log.Error("config import: open file", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
	return config.ExportPayload{}, "", false
}
