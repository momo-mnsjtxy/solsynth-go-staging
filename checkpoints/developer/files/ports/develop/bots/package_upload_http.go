// SPDX-License-Identifier: AGPL-3.0-only
package bots

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path"
	"solsynth.local/go-migration/ports/develop/apps"
	"strings"
)

const MaxPackageBytes int64 = 5 << 20
const MaxPackageRequestBytes = MaxPackageBytes + 64<<10

type PackageUploader interface {
	UploadPackage(context.Context, apps.Scope, string, string, io.Reader) (PackageResult, error)
}

// MiniScopeResolver supplies the owned app association absent in authored URLs.
// Existing-resource routes resolve by immutable miniapp ID; ambiguous project
// collections fail closed rather than silently choosing an app.
type MiniScopeResolver interface {
	ResolveMiniScope(context.Context, apps.Scope, string) (apps.Scope, error)
}

func (s *Service) uploadPackage(w http.ResponseWriter, r *http.Request, ctx context.Context, scope apps.Scope, id string) {
	repo, ok := s.Repository.(PackageUploader)
	if !ok {
		failure(w, ErrUnsupported, true)
		return
	}
	if r.Body == nil || r.ContentLength > MaxPackageRequestBytes {
		failure(w, ErrInvalid, true)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxPackageRequestBytes)
	reader, err := r.MultipartReader()
	if err != nil {
		failure(w, ErrInvalid, true)
		return
	}
	part, err := reader.NextRawPart()
	if err != nil {
		failure(w, ErrInvalid, true)
		return
	}
	defer part.Close()
	name := part.FileName()
	typ, _, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
	if !strings.EqualFold(part.FormName(), "file") || name == "" || part.Header.Get("Content-Transfer-Encoding") != "" || (!strings.EqualFold(path.Ext(name), ".zip") && (err != nil || typ != "application/zip" && typ != "application/x-zip-compressed")) {
		failure(w, ErrInvalid, true)
		return
	}
	value, err := repo.UploadPackage(ctx, scope, id, name, &singlePackageReader{part: part, multipart: reader, body: r.Body})
	if err != nil {
		failure(w, err, true)
		return
	}
	send(w, 200, value)
}

// EOF includes validation of the complete multipart body. An extra field/file
// or truncated final boundary aborts the transaction before storage publication.
type singlePackageReader struct {
	part      *multipart.Part
	multipart *multipart.Reader
	body      io.Reader
	done      bool
}

func (r *singlePackageReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	n, err := r.part.Read(p)
	if err != io.EOF {
		return n, err
	}
	if _, tail := r.multipart.NextRawPart(); tail != io.EOF {
		return n, ErrInvalid
	}
	if _, err := io.Copy(io.Discard, r.body); err != nil {
		return n, ErrInvalid
	}
	r.done = true
	return n, io.EOF
}
