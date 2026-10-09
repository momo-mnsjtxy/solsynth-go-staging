// SPDX-License-Identifier: AGPL-3.0-only
package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"testing"
)

type usageFixtureRepo struct{ syntheticRepo }

func (s *usageFixtureRepo) UploadDirect(_ context.Context, _ string, u Upload, r io.Reader) (File, error) {
	b, e := io.ReadAll(r)
	if e != nil {
		return File{}, e
	}
	usage, e := r.(UploadUsageSource).UploadUsage()
	if e != nil {
		return File{}, e
	}
	f := syntheticFile()
	f.Size = int64(len(b))
	f.Usage = usage
	s.uploads++
	s.request = u
	return f, nil
}
func TestDirectUsageBeforeAndAfterFileHasSameMeaning(t *testing.T) {
	for _, before := range []bool{true, false} {
		var body bytes.Buffer
		w := multipart.NewWriter(&body)
		if before {
			w.WriteField("usage", "post")
		}
		p, _ := w.CreateFormFile("file", "file.txt")
		p.Write([]byte("body"))
		if !before {
			w.WriteField("usage", "post")
		}
		w.Close()
		repo := &usageFixtureRepo{}
		h := (&Service{Repository: repo, Authority: syntheticAuthority{}, MaxFileBytes: 1024}).Handler()
		req := httptest.NewRequest("POST", "/api/files/upload/direct", &body)
		req.Header.Set("Authorization", "Bearer synthetic")
		req.Header.Set("Content-Type", w.FormDataContentType())
		out := httptest.NewRecorder()
		h.ServeHTTP(out, req)
		if out.Code != 200 {
			t.Fatal(before, out.Code, out.Body.String())
		}
		var file map[string]any
		if e := json.Unmarshal(out.Body.Bytes(), &file); e != nil {
			t.Fatal(e)
		}
		if file["usage"] != "post" || repo.uploads != 1 || file["size"] != float64(4) {
			t.Fatal(file, repo.uploads)
		}
	}
}
func TestUsageIsBoundedMetadataAndDefaultsRemainNullable(t *testing.T) {
	for _, value := range []string{"", "post", "profile_picture", "ticket"} {
		if e := ValidateUsage(value); e != nil {
			t.Fatal(value, e)
		}
	}
	for _, value := range []string{"POST", " post", "chat", "public", "post\x00"} {
		if e := ValidateUsage(value); !errors.Is(e, ErrUnsupported) {
			t.Fatal(value, e)
		}
		if e := checkFields(map[string]string{"usage": value}, "text/plain"); !errors.Is(e, ErrUnsupported) {
			t.Fatal(value, e)
		}
	}
	if e := checkFields(map[string]string{"usage": "post", "pool_id": "unimplemented"}, "text/plain"); !errors.Is(e, ErrUnsupported) {
		t.Fatal(e)
	}
	if f := LegacyFile(File{}); f["usage"] != nil {
		t.Fatal(f)
	}
	f := File{Usage: "post"}
	p := LegacyFile(f)
	if p["usage"] != "post" || p["is_public"] != false {
		t.Fatal(p)
	}
	source := &multipartFileReader{}
	if _, e := source.UploadUsage(); !errors.Is(e, ErrInvalid) {
		t.Fatal("premature metadata accepted", e)
	}
}
