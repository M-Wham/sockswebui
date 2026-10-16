package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"unicode"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/importer"
)

const (
	// maxImportBody is the per-request upload limit.
	maxImportBody = 5 << 20
	// importMemory is the in-memory multipart threshold; larger parts spill to temp files
	// that are removed before the handler returns.
	importMemory = 64 << 10
	// maxDisplayNameRunes caps a file name echoed in results or logs.
	maxDisplayNameRunes = 128
)

// handleImportEndpoints is POST /api/endpoints/import: multipart field
// "files", one or more .conf/.zip files. Files are parsed outside the store lock; the final
// dedupe and append happen in ONE store.Update so concurrent imports cannot double-insert.
func (s *Server) handleImportEndpoints(w http.ResponseWriter, r *http.Request) {
	uploads, ok := readUploads(w, r)
	if !ok {
		return
	}
	snapshot := s.store.Get().Endpoints
	results, added := prepareImport(snapshot, uploads)
	if s.importParsed != nil {
		s.importParsed()
	}
	var ids []string
	err := s.store.Update(func(c *config.Config) error {
		if !reflect.DeepEqual(c.Endpoints, snapshot) { // changed since parsing: redo dedupe
			results, added = prepareImport(c.Endpoints, uploads)
		}
		ids = ids[:0]
		for _, e := range added {
			id, err := newEndpointID(c.Endpoints)
			if err != nil {
				return err
			}
			e.ID = id
			ids = append(ids, id)
			c.Endpoints = append(c.Endpoints, e)
		}
		return nil
	})
	if err != nil {
		s.writeStoreError(w, "import endpoints", err, "")
		return
	}
	resp := toImportResponse(results, ids)
	s.logImport(resp)
	writeJSON(w, http.StatusOK, resp)
}

// readUploads parses the size-limited multipart body into importer files. On failure it
// writes 413 (body too large) or 400 and returns false. Temp files never outlive the call.
func readUploads(w http.ResponseWriter, r *http.Request) ([]importer.File, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxImportBody)
	if err := r.ParseMultipartForm(importMemory); err != nil {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("upload must be at most %d bytes", maxImportBody))
			return nil, false
		}
		writeFieldErrors(w, []config.FieldError{{Field: "body", Message: "must be multipart/form-data"}})
		return nil, false
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()
	fhs := r.MultipartForm.File["files"]
	if len(fhs) == 0 {
		writeFieldErrors(w, []config.FieldError{{Field: "files", Message: "at least one file is required"}})
		return nil, false
	}
	uploads := make([]importer.File, 0, len(fhs))
	for _, fh := range fhs {
		u := importer.File{Name: uploadName(fh.Filename)}
		f, err := fh.Open()
		if err == nil {
			u.Data, err = io.ReadAll(io.LimitReader(f, maxImportBody))
			_ = f.Close()
		}
		if err != nil {
			u.Err = "could not read upload"
		}
		uploads = append(uploads, u)
	}
	return uploads, true
}

// prepareImport runs the importer against existing and turns endpoints that fail config
// validation into per-file errors, so one bad file never blocks the batch.
// It returns the results (Endpoint pointers cleared) and the endpoints to add, in order.
func prepareImport(existing []config.Endpoint, uploads []importer.File) ([]importer.Result, []config.Endpoint) {
	results, _ := importer.Import(existing, uploads)
	var added []config.Endpoint
	for i := range results {
		r := &results[i]
		if r.Status != importer.StatusImported || r.Endpoint == nil {
			r.Endpoint = nil
			continue
		}
		e := *r.Endpoint
		r.Endpoint = nil
		if ve := config.ValidateEndpoint("", e); len(ve) > 0 {
			r.Status, r.Warnings, r.Reason = importer.StatusError, nil, validationReason(ve)
			continue
		}
		added = append(added, e)
	}
	return results, added
}

func validationReason(ve config.ValidationErrors) string {
	parts := make([]string, len(ve))
	for i, fe := range ve {
		parts[i] = strings.TrimPrefix(fe.Field, ".") + " " + fe.Message
	}
	return "invalid endpoint: " + strings.Join(parts, "; ")
}

// toImportResponse pairs imported results, in order, with the ids assigned to them.
func toImportResponse(results []importer.Result, ids []string) importResponse {
	out := importResponse{Results: make([]importResult, len(results))}
	n := 0
	for i, r := range results {
		ir := importResult{File: displayName(r.File), Status: r.Status, Reason: r.Reason, Warnings: r.Warnings}
		if r.Status == importer.StatusImported && n < len(ids) {
			ir.ID = ids[n]
			n++
		}
		out.Results[i] = ir
	}
	return out
}

// logImport logs counts only, never file names or contents.
func (s *Server) logImport(resp importResponse) {
	counts := map[string]int{}
	for _, r := range resp.Results {
		counts[r.Status]++
	}
	s.log.Info("endpoints import", "imported", counts[importer.StatusImported],
		"skipped", counts[importer.StatusSkippedDuplicate], "errors", counts[importer.StatusError])
}

// uploadName keeps only the last path element of a client-supplied filename (either
// separator). It is sanitized for display only when echoed back (displayName).
func uploadName(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// displayName strips control characters and caps the length at maxDisplayNameRunes runes.
func displayName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			return -1
		}
		return r
	}, s)
	if r := []rune(s); len(r) > maxDisplayNameRunes {
		s = string(r[:maxDisplayNameRunes])
	}
	if s = strings.TrimSpace(s); s == "" {
		return "upload"
	}
	return s
}
