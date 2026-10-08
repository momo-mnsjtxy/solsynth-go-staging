// SPDX-License-Identifier: AGPL-3.0-only
package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"solsynth.local/go-migration/ports/authority"
)

const syntheticID = "b0000000-0000-4000-8000-000000000001"

type syntheticAuthority struct{}

func (syntheticAuthority) AuthenticateRequest(r *http.Request) (authority.Principal, error) {
	if r.Header.Get("Authorization") != "Bearer synthetic" {
		return authority.Principal{}, authority.ErrInvalid
	}
	return authority.Principal{Account: authority.Account{ID: syntheticID}}, nil
}
func (syntheticAuthority) AuthenticatePrincipal(_ context.Context, p authority.Principal) (authority.Principal, error) {
	return p, nil
}
func (syntheticAuthority) AuthorizePrincipal(_ context.Context, p authority.Principal, _ ...string) (authority.Principal, error) {
	return p, nil
}

type syntheticRepo struct {
	uploads int
	body    string
	request Upload
}
type readSeekClose struct{ *strings.Reader }

func (readSeekClose) Close() error { return nil }
func syntheticFile() File {
	return File{ID: syntheticID, AccountID: syntheticID, Name: "sample.txt", MIMEType: "text/plain", Size: 5, SHA256: strings.Repeat("a", 64), Public: true, State: "ready", CreatedAt: time.Unix(100, 0), UpdatedAt: time.Unix(100, 0)}
}
func (s *syntheticRepo) Upload(_ context.Context, _ string, u Upload, r io.Reader) (File, bool, error) {
	b, e := io.ReadAll(r)
	if e != nil {
		return File{}, false, e
	}
	s.uploads++
	s.body = string(b)
	s.request = u
	return syntheticFile(), false, nil
}
func (s *syntheticRepo) UploadDirect(_ context.Context, _ string, u Upload, r io.Reader) (File, error) {
	b, e := io.ReadAll(io.LimitReader(r, u.Size+1))
	if e != nil {
		return File{}, e
	}
	if int64(len(b)) > u.Size {
		return File{}, ErrTooLarge
	}
	s.uploads++
	s.body = string(b)
	s.request = u
	f := syntheticFile()
	f.Size = int64(len(b))
	sum := sha256.Sum256(b)
	f.SHA256 = hex.EncodeToString(sum[:])
	return f, nil
}
func (*syntheticRepo) Info(context.Context, string, string) (File, error) {
	return syntheticFile(), nil
}
func (*syntheticRepo) Open(context.Context, string, string) (File, io.ReadSeekCloser, error) {
	return syntheticFile(), readSeekClose{strings.NewReader("hello")}, nil
}
func (*syntheticRepo) Delete(context.Context, string, string) (File, error) {
	f := syntheticFile()
	f.State = "deleted"
	return f, nil
}
func (*syntheticRepo) Quota(context.Context, string) (Quota, error) {
	return Quota{LimitBytes: 20, UsedBytes: 5}, nil
}
func handler() (http.Handler, *syntheticRepo) {
	repo := &syntheticRepo{}
	return (&Service{Repository: repo, Authority: syntheticAuthority{}, MaxFileBytes: 1024}).Handler(), repo
}

func TestHTTPAuthorityAndPrivateHeaders(t *testing.T) {
	for _, tc := range []struct {
		name, key, value string
		status           int
	}{{"anonymous", "", "", 200}, {"empty bearer", "Authorization", "", 401}, {"invalid bearer", "Authorization", "Bearer bad", 401}, {"identity header", "X-Account-Id", syntheticID, 403}} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := handler()
			r := httptest.NewRequest("GET", "/api/files/"+syntheticID, nil)
			if tc.key != "" {
				r.Header[tc.key] = []string{tc.value}
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatal(w.Code, w.Body.String())
			}
			if w.Code == 200 && (w.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment")) {
				t.Fatal(w.Header())
			}
		})
	}
}
func TestHTTPRangeHeadAndUnsupported(t *testing.T) {
	h, _ := handler()
	r := httptest.NewRequest("GET", "/api/files/"+syntheticID+".txt", nil)
	r.Header.Set("Range", "bytes=1-3")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 206 || w.Body.String() != "ell" {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, p := range []string{"/api/files/" + syntheticID, "/api/files/" + syntheticID + "/info"} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("HEAD", p, nil))
		if w.Code != 200 || w.Body.Len() != 0 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, p := range []string{"/api/files/" + syntheticID + "?thumbnail=true", "/api/files/" + syntheticID + "/e2ee", "/api/files/upload/create"} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if w.Code != 501 {
			t.Fatal(p, w.Code, w.Body.String())
		}
	}
}
func multipartRequest(t *testing.T, trailing map[string]string, secondFile bool) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	p, e := writer.CreateFormFile("file", "sample.txt")
	if e != nil {
		t.Fatal(e)
	}
	p.Write([]byte("hello"))
	for k, v := range trailing {
		if e = writer.WriteField(k, v); e != nil {
			t.Fatal(e)
		}
	}
	if secondFile {
		p, e = writer.CreateFormFile("file", "two.txt")
		if e != nil {
			t.Fatal(e)
		}
		p.Write([]byte("second"))
	}
	writer.Close()
	r := httptest.NewRequest("POST", "/api/files/upload/direct", &body)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	r.Header.Set("Authorization", "Bearer synthetic")
	return r
}
func TestDirectMultipartStreamingAndTrailingValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields map[string]string
		second bool
		status int
	}{{"one file", nil, false, 200}, {"blank legacy option", map[string]string{"pool_id": ""}, false, 200}, {"remote pool", map[string]string{"pool_id": syntheticID}, false, 501}, {"encrypted", map[string]string{"encryption_scheme": "file.aesgcm.v1"}, false, 501}, {"unknown option", map[string]string{"custom": "value"}, false, 501}, {"two files", nil, true, 400}} {
		t.Run(tc.name, func(t *testing.T) {
			h, repo := handler()
			w := httptest.NewRecorder()
			h.ServeHTTP(w, multipartRequest(t, tc.fields, tc.second))
			if w.Code != tc.status {
				t.Fatal(w.Code, w.Body.String())
			}
			if tc.status == 200 {
				if repo.body != "hello" || repo.uploads != 1 || repo.request.Public {
					t.Fatal(repo)
				}
				var v map[string]any
				if e := json.Unmarshal(w.Body.Bytes(), &v); e != nil {
					t.Fatal(e)
				}
				if v["hash"] != nil || v["has_thumbnail"] != false || v["uploaded_at"] != nil {
					t.Fatal("invented remote processing", v)
				}
			} else if repo.uploads != 0 {
				t.Fatal("invalid multipart committed")
			}
		})
	}
}
func TestRawUploadRequiresLengthAndRejectsOversize(t *testing.T) {
	h, repo := handler()
	for _, length := range []int64{-1, 1025} {
		r := httptest.NewRequest("POST", "/api/media/files", strings.NewReader("hello"))
		r.ContentLength = length
		r.Header.Set("Authorization", "Bearer synthetic")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		want := 400
		if length > 1024 {
			want = 413
		}
		if w.Code != want || repo.uploads != 0 {
			t.Fatal(w.Code, repo.uploads)
		}
	}
	r := httptest.NewRequest("POST", "/api/media/files", strings.NewReader("hello"))
	r.Header.Set("Authorization", "Bearer synthetic")
	r.Header.Set("Content-Type", "text/plain")
	r.Header.Set("X-File-Name", "sample.txt")
	r.Header.Set("X-Content-SHA256", strings.Repeat("a", 64))
	r.Header.Set("Idempotency-Key", "request-1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 201 || repo.body != "hello" || repo.request.Public || repo.request.Size != 5 {
		t.Fatal(w.Code, repo)
	}
}
