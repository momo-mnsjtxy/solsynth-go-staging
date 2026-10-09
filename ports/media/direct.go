// SPDX-License-Identifier: AGPL-3.0-only
package media

import (
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
)

const directLimit int64 = 20 << 20
const directOverhead int64 = 64 << 10

// Direct upload is the documented single-file multipart route. Advanced pool,
// bundle, E2EE, hierarchy and processing options fail explicitly. It does not
// silently accept fields whose meaning cannot be honored by this local provider.
func (s *Service) direct(w http.ResponseWriter, r *http.Request) {
	p, e := s.principal(r, CreatePermission)
	if e != nil {
		failure(w, e)
		return
	}
	repo, ok := s.Repository.(DirectRepository)
	if !ok {
		failure(w, ErrUnsupported)
		return
	}
	if r.Header.Get("Idempotency-Key") != "" || len(r.URL.Query()) != 0 {
		failure(w, ErrUnsupported)
		return
	}
	max := min(s.MaxFileBytes, directLimit)
	if r.ContentLength < 0 {
		failure(w, ErrInvalid)
		return
	}
	if r.ContentLength > max+directOverhead {
		failure(w, ErrTooLarge)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, max+directOverhead)
	reader, e := r.MultipartReader()
	if e != nil {
		failure(w, ErrInvalid)
		return
	}
	metadata := map[string]string{}
	for {
		part, err := reader.NextRawPart()
		if err != nil {
			failure(w, ErrInvalid)
			return
		}
		if part.FileName() == "" {
			if e = readField(part, metadata); e != nil {
				failure(w, e)
				return
			}
			continue
		}
		if part.FormName() != "file" {
			failure(w, ErrInvalid)
			return
		}
		contentType := part.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		typ, params, err := mime.ParseMediaType(contentType)
		if err != nil || len(params) != 0 {
			failure(w, ErrInvalid)
			return
		}
		if e = checkFields(metadata, typ); e != nil {
			failure(w, e)
			return
		}
		tail := &multipartFileReader{part: part, reader: reader, fields: metadata, contentType: typ}
		// Content-Length includes framing, so the reservation is conservative. Only
		// measured file bytes are charged after processing and successful commit.
		f, e := repo.UploadDirect(r.Context(), p.Account.ID, Upload{Name: part.FileName(), MIMEType: typ, Size: min(r.ContentLength, max), Usage: metadata["usage"]}, tail)
		if e != nil {
			failure(w, e)
			return
		}
		reply(w, 200, legacyFile(f))
		return
	}
}
func readField(p *multipart.Part, fields map[string]string) error {
	name := p.FormName()
	if name == "" || p.FileName() != "" {
		return ErrInvalid
	}
	if _, ok := fields[name]; ok {
		return ErrInvalid
	}
	if len(fields) >= 16 {
		return ErrInvalid
	}
	b, e := io.ReadAll(io.LimitReader(p, 4097))
	if e != nil || len(b) > 4096 {
		return ErrInvalid
	}
	fields[name] = string(b)
	return nil
}
func checkFields(fields map[string]string, typ string) error {
	for k, v := range fields {
		switch k {
		case "content_type":
			if v != "" && v != typ {
				return ErrUnsupported
			}
		case "usage":
			if e := ValidateUsage(v); e != nil {
				return e
			}
		case "pool_id", "bundle_id", "encryption_scheme", "encryption_header", "encryption_signature", "expired_at", "parent_id", "application_type":
			if v != "" {
				return ErrUnsupported
			}
		default:
			return ErrUnsupported
		}
	}
	return nil
}

type multipartFileReader struct {
	part        *multipart.Part
	reader      *multipart.Reader
	fields      map[string]string
	contentType string
	done        bool
}

func (m *multipartFileReader) Read(p []byte) (int, error) {
	if m.done {
		return 0, io.EOF
	}
	n, e := m.part.Read(p)
	if !errors.Is(e, io.EOF) {
		return n, e
	}
	for {
		next, err := m.reader.NextRawPart()
		if errors.Is(err, io.EOF) {
			m.done = true
			return n, checkEOF(m.fields, m.contentType)
		}
		if err != nil {
			return n, ErrInvalid
		}
		if next.FileName() != "" {
			return n, ErrInvalid
		}
		if err = readField(next, m.fields); err != nil {
			return n, err
		}
	}
}
func checkEOF(fields map[string]string, typ string) error {
	if e := checkFields(fields, typ); e != nil {
		return e
	}
	return io.EOF
}

// A truthful subset/nullable projection of the documented SnCloudFile contract.
// No remote upload, thumbnail, MD5 deduplication or folder processing is claimed.
func legacyFile(f File) map[string]any { return LegacyFile(f) }

// LegacyFile is the shared truthful projection for upload and post attachment responses.
func LegacyFile(f File) map[string]any {
	var usage any
	if f.Usage != "" {
		usage = f.Usage
	}
	return map[string]any{
		"id": f.ID, "name": f.Name, "description": nil, "user_meta": map[string]any{}, "sensitive_marks": []string{}, "mime_type": f.MIMEType, "hash": nil, "sha256": f.SHA256, "size": f.Size,
		"has_compression": false, "has_thumbnail": false, "expired_at": nil, "uploaded_at": nil, "object_id": f.ID, "parent_id": nil, "bundle_id": nil, "indexed": false, "is_folder": false, "is_marked_recycle": false,
		"storage_id": nil, "storage_url": nil, "account_id": f.AccountID, "usage": usage, "application_type": nil, "created_at": f.CreatedAt, "updated_at": f.UpdatedAt, "is_public": f.Public, "state": f.State,
	}
}

// LegacyID supports the optional extension suffix documented on open-file URLs.
func legacyID(s string) string { id, _, _ := strings.Cut(s, "."); return id }

// UploadUsage is consulted only after the whole multipart stream has reached
// a validated EOF, so before-file and after-file metadata have the same meaning.
func (m *multipartFileReader) UploadUsage() (string, error) {
	if !m.done {
		return "", ErrInvalid
	}
	return m.fields["usage"], ValidateUsage(m.fields["usage"])
}
