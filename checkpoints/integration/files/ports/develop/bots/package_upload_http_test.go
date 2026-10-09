// SPDX-License-Identifier: AGPL-3.0-only
package bots

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/apps"
	"strings"
	"testing"
)

const uploadID = "00000000-0000-4000-8000-000000000001"

type uploadAuthority struct{ denied bool }

func (a uploadAuthority) AuthenticateRequest(*http.Request) (authority.Principal, error) {
	if a.denied {
		return authority.Principal{}, authority.ErrInvalid
	}
	return authority.Principal{Account: authority.Account{ID: uploadID}}, nil
}
func (a uploadAuthority) AuthenticatePrincipal(_ context.Context, p authority.Principal) (authority.Principal, error) {
	return p, nil
}
func (a uploadAuthority) AuthorizePrincipal(_ context.Context, p authority.Principal, _ ...string) (authority.Principal, error) {
	return p, nil
}

type uploadRepository struct {
	Repository
	calls int
	name  string
	body  []byte
	scope apps.Scope
}

func (r *uploadRepository) ResolveMiniScope(_ context.Context, s apps.Scope, id string) (apps.Scope, error) {
	if id != uploadID {
		return s, ErrNotFound
	}
	s.App = uploadID
	return s, nil
}
func (r *uploadRepository) UploadPackage(_ context.Context, s apps.Scope, id, name string, input io.Reader) (PackageResult, error) {
	r.calls++
	r.name = name
	r.scope = s
	b, e := io.ReadAll(io.LimitReader(input, MaxPackageBytes+1))
	if e != nil || int64(len(b)) > MaxPackageBytes {
		return PackageResult{}, ErrInvalid
	}
	r.body = b
	return PackageResult{Key: uploadID, URL: "/api/miniapps/synthetic/package", FileName: name, ContentType: "application/zip", Size: int64(len(b)), SHA256: strings.Repeat("a", 64)}, nil
}
func multipartPackage(t *testing.T, field, name string, data []byte, tail bool) ([]byte, string) {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	p, e := w.CreateFormFile(field, name)
	if e != nil {
		t.Fatal(e)
	}
	p.Write(data)
	if tail {
		w.WriteField("unexpected", "value")
	}
	w.Close()
	return b.Bytes(), w.FormDataContentType()
}
func TestMultipartPackageAuthoredRouteAndShape(t *testing.T) {
	for _, field := range []string{"file", "File"} {
		repo := &uploadRepository{}
		s := Service{Repository: repo, Authority: uploadAuthority{}}
		data, typ := multipartPackage(t, field, "inert.ZIP", []byte("inert repository fixture"), false)
		r := httptest.NewRequest("POST", "/api/private/miniapps/"+uploadID+"/package?dev=synthetic&proj="+uploadID, bytes.NewReader(data))
		r.Header.Set("Content-Type", typ)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 200 || repo.calls != 1 || repo.name != "inert.ZIP" || repo.scope.App != uploadID || repo.scope.Developer != "synthetic" || string(repo.body) != "inert repository fixture" {
			t.Fatal(w.Code, w.Body.String(), repo)
		}
		if !strings.Contains(w.Body.String(), `"file_name":"inert.ZIP"`) || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(w.Body.String())
		}
	}
}
func TestMultipartPackageRejectsAmbiguousIncompleteAndOversize(t *testing.T) {
	for _, tc := range []struct {
		name, field, filename   string
		tail, truncated, denied bool
		bytes                   int
		want                    int
	}{
		{"wrong field", "other", "x.zip", false, false, false, 1, 400},
		{"wrong type", "file", "x.txt", false, false, false, 1, 400},
		{"late field", "file", "x.zip", true, false, false, 1, 400},
		{"truncated", "file", "x.zip", false, true, false, 1, 400},
		{"oversize", "file", "x.zip", false, false, false, int(MaxPackageBytes + 1), 400},
		{"unauthenticated", "file", "x.zip", false, false, true, 1, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &uploadRepository{}
			s := Service{Repository: repo, Authority: uploadAuthority{tc.denied}}
			data, typ := multipartPackage(t, tc.field, tc.filename, bytes.Repeat([]byte("x"), tc.bytes), tc.tail)
			if tc.truncated {
				data = data[:len(data)-12]
			}
			r := httptest.NewRequest("POST", "/api/private/miniapps/"+uploadID+"/package?dev=synthetic&proj="+uploadID, bytes.NewReader(data))
			r.Header.Set("Content-Type", typ)
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != tc.want || len(repo.body) != 0 {
				t.Fatal(w.Code, w.Body.String())
			}
			if tc.denied && repo.calls != 0 {
				t.Fatal("unauthenticated body reached storage")
			}
		})
	}
}
func TestMiniMediaReferencesRetainClearAndCanonicalIDs(t *testing.T) {
	for _, id := range []string{"", "   ", uploadID} {
		if e := ValidateMini(MiniPatch{IconID: &id, BackgroundID: &id}, false); e != nil {
			t.Fatal(id, e)
		}
	}
	for _, id := range []string{"https://example.invalid/image.png", "../file", "not-an-id"} {
		if e := ValidateMini(MiniPatch{IconID: &id}, false); !errors.Is(e, ErrInvalid) {
			t.Fatal(id, e)
		}
	}
}

func TestMultipartPackageChunkedRequestLimitIncludesEpilogue(t *testing.T) {
	repo := &uploadRepository{}
	s := Service{Repository: repo, Authority: uploadAuthority{}}
	data, typ := multipartPackage(t, "file", "x.zip", bytes.Repeat([]byte("x"), int(MaxPackageBytes)), false)
	data = append(data, bytes.Repeat([]byte("e"), 70<<10)...)
	r := httptest.NewRequest("POST", "/api/private/miniapps/"+uploadID+"/package?dev=synthetic&proj="+uploadID, bytes.NewReader(data))
	r.ContentLength = -1
	r.Header.Set("Content-Type", typ)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 400 || len(repo.body) != 0 {
		t.Fatal("chunked request exceeded framing bound", w.Code)
	}
}
