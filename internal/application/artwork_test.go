// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"solsynth.local/go-migration/ports/passport/artwork"
)

type artworkReader struct{ *bytes.Reader }

func (artworkReader) Close() error { return nil }

type artworkRepository struct {
	data           []byte
	actor          string
	uploads, reads int
}

func (r *artworkRepository) view() artwork.Artwork {
	d := sha256.Sum256(r.data)
	h := "sha256:" + hex.EncodeToString(d[:])
	return artwork.Artwork{Hash: h, MIMEType: "image/png", Size: int64(len(r.data)), URL: "/api/presence/artworks/" + h}
}
func (r *artworkRepository) UploadArtwork(_ context.Context, actor, typ string, data []byte) (artwork.Artwork, error) {
	r.actor = actor
	r.data = append([]byte(nil), data...)
	r.uploads++
	return r.view(), nil
}
func (r *artworkRepository) OpenArtwork(_ context.Context, actor, hash string) (artwork.Artwork, io.ReadSeekCloser, error) {
	r.actor = actor
	r.reads++
	return r.view(), artworkReader{bytes.NewReader(r.data)}, nil
}
func artworkMultipart(t *testing.T, typ string, data []byte, duplicate bool) ([]byte, string) {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	for i := 0; i < 1+boolInt(duplicate); i++ {
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", `form-data; name="File"; filename="synthetic.png"`)
		header.Set("Content-Type", typ)
		p, e := w.CreatePart(header)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = p.Write(data); e != nil {
			t.Fatal(e)
		}
	}
	if e := w.Close(); e != nil {
		t.Fatal(e)
	}
	return b.Bytes(), w.FormDataContentType()
}
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
func TestArtworkCanonicalHostMultipartAndRange(t *testing.T) {
	issuer, verifier, store := hostIdentity(t)
	a := hostToken(t, issuer, hostAccountA, "*")
	b := hostToken(t, issuer, hostAccountB, "*")
	repo := &artworkRepository{}
	h := router{modules{artwork: bindIdentity(verifier, (&artwork.Service{Repository: repo, Authority: proofIdentity{identity{verifier}}}).Handler())}}
	post := func(typ string, data []byte, duplicate bool) *httptest.ResponseRecorder {
		raw, ct := artworkMultipart(t, typ, data, duplicate)
		req := httptest.NewRequest("POST", "/api/presence/artworks", bytes.NewReader(raw))
		req.Header.Set("Content-Type", ct)
		req.Header.Set("Authorization", "Bearer "+a)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	w := post("image/png", []byte("synthetic artwork"), false)
	if w.Code != 200 || repo.actor != hostAccountA || repo.uploads != 1 {
		t.Fatal(w.Code, w.Body.String(), repo)
	}
	var dto artwork.Artwork
	if e := json.Unmarshal(w.Body.Bytes(), &dto); e != nil || dto != repo.view() {
		t.Fatal(dto, e)
	}
	req := httptest.NewRequest("GET", dto.URL, nil)
	req.Header.Set("Range", "bytes=0-3")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 206 || w.Body.String() != "synt" || repo.actor != "" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal(w.Code, w.Body.String(), repo.actor)
	}
	if w = hostRequest(h, "GET", dto.URL, b, ""); w.Code != 200 || repo.actor != hostAccountB {
		t.Fatal(w.Code, repo.actor)
	}
	if w = hostRequest(h, "HEAD", dto.URL, "", ""); w.Code != 200 || w.Body.Len() != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
	before := repo.reads
	if w = hostRequest(h, "GET", dto.URL, "invalid", ""); w.Code != 401 || repo.reads != before {
		t.Fatal(w.Code, repo.reads)
	}
	store.accounts[hostAccountB].account.Disabled = true
	if w = hostRequest(h, "GET", dto.URL, b, ""); w.Code != 423 || repo.reads != before {
		t.Fatal(w.Code, repo.reads)
	}
	for _, tc := range []struct {
		typ       string
		data      []byte
		duplicate bool
		status    int
	}{{"text/plain", []byte("not image"), false, 400}, {"image/png", nil, false, 400}, {"image/png", []byte("duplicate"), true, 400}, {"image/png", bytes.Repeat([]byte("x"), artwork.MaxBytes), false, 413}} {
		if w = post(tc.typ, tc.data, tc.duplicate); w.Code != tc.status || repo.uploads != 1 {
			t.Fatal(w.Code, w.Body.String(), repo.uploads)
		}
	}
	if w = hostRequest(h, "GET", "/api/presence/artworks/invalid", "", ""); w.Code != 400 || repo.reads != before {
		t.Fatal(w.Code)
	}
	w = hostRequest(h, "POST", "/api/presence/artworks", a, `{"file":"sha256:`+strings.Repeat("a", 64)+`"}`)
	if w.Code != 400 || repo.uploads != 1 {
		t.Fatal("hash-only upload accepted", w.Code)
	}
}
