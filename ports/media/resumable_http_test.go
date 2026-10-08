// SPDX-License-Identifier: AGPL-3.0-only
package media

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type resumableFixtureRepo struct {
	syntheticRepo
	created, chunks, completed, canceled int
	input                                CreateUploadRequest
	data                                 string
	query                                UploadTaskQuery
}

func (*resumableFixtureRepo) task() UploadTask {
	return UploadTask{TaskID: syntheticID, FileName: "sample.bin", FileSize: 5, ContentType: "application/octet-stream", ChunkSize: 3, ChunksCount: 2, UploadedChunks: []int{}, Status: "InProgress"}
}
func (s *resumableFixtureRepo) CreateResumable(_ context.Context, _ string, r CreateUploadRequest, _ string) (UploadTask, error) {
	s.created++
	s.input = r
	return s.task(), nil
}
func (s *resumableFixtureRepo) UploadChunk(_ context.Context, _ string, _ string, _ int, r io.Reader) (bool, error) {
	b, e := io.ReadAll(r)
	if e != nil {
		return false, e
	}
	s.data = string(b)
	s.chunks++
	return s.chunks > 1, nil
}
func (s *resumableFixtureRepo) CompleteResumable(context.Context, string, string) error {
	s.completed++
	return nil
}
func (s *resumableFixtureRepo) ResumableProgress(context.Context, string, string) (UploadTask, error) {
	return s.task(), nil
}
func (s *resumableFixtureRepo) ListResumable(_ context.Context, _ string, q UploadTaskQuery) ([]UploadTask, error) {
	s.query = q
	return []UploadTask{s.task()}, nil
}
func (s *resumableFixtureRepo) CancelResumable(context.Context, string, string) error {
	s.canceled++
	return nil
}
func resumableHandler() (http.Handler, *resumableFixtureRepo) {
	repo := &resumableFixtureRepo{}
	return (&Service{Repository: repo, Authority: syntheticAuthority{}, MaxFileBytes: 1024}).Handler(), repo
}
func uploadHTTPRequest(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer synthetic")
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestResumableHTTPDocumentedRoutesAndAsyncShape(t *testing.T) {
	h, repo := resumableHandler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, uploadHTTPRequest("POST", "/api/files/upload/create", `{"hash":"5d41402abc4b2a76b9719d911017c592","file_name":"sample.bin","file_size":5,"content_type":"application/octet-stream","chunk_size":3}`))
	if w.Code != 200 || repo.created != 1 || repo.input.ChunkSize != 3 {
		t.Fatal(w.Code, w.Body.String())
	}
	var data map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &data); e != nil {
		t.Fatal(e)
	}
	if data["file_exists"] != false || data["file"] != nil || data["task_id"] != syntheticID || data["chunks_count"] != float64(2) {
		t.Fatal(data)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, uploadHTTPRequest("POST", "/api/files/upload/complete/"+syntheticID, ""))
	if w.Code != 202 || repo.completed != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	data = map[string]any{}
	if e := json.Unmarshal(w.Body.Bytes(), &data); e != nil {
		t.Fatal(e)
	}
	if data["status"] != "processing" || data["taskId"] != syntheticID || data["message"] != "Upload is being processed" || len(data) != 3 {
		t.Fatal(data)
	}
	for _, path := range []string{"progress/" + syntheticID, "resume/" + syntheticID, "tasks", "tasks/recent"} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, uploadHTTPRequest("GET", "/api/files/upload/"+path, ""))
		if w.Code != 200 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	if repo.query.Limit != 10 || repo.query.SortBy != "lastActivity" || !repo.query.Descending {
		t.Fatal(repo.query)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, uploadHTTPRequest("DELETE", "/api/files/upload/"+syntheticID, ""))
	if w.Code != 200 || repo.canceled != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestResumableHTTPRejectsAmbiguousInputs(t *testing.T) {
	for _, body := range []string{`null`, `[]`, `{"file_size":5,"file_size":6}`, `{"unknown":true}`, `{"file_size":5} {}`, `{"file_size":5,}`, `{"file_size":1.5}`} {
		h, repo := resumableHandler()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, uploadHTTPRequest("POST", "/api/files/upload/create", body))
		if w.Code != 400 || repo.created != 0 {
			t.Fatal(body, w.Code, w.Body.String())
		}
	}
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "create", "", 405}, {"POST", "complete/" + syntheticID, "{}", 400}, {"GET", "progress/" + syntheticID + "?token=bad", "", 400},
		{"GET", "tasks?limit=1&limit=2", "", 400}, {"GET", "tasks?unexpected=x", "", 400}, {"GET", "tasks/recent?offset=1", "", 400},
		{"POST", "chunk/" + syntheticID + "/-1", "", 400}, {"POST", "chunk/" + syntheticID + "/01", "", 400}, {"POST", "chunk/" + syntheticID + "/10000", "", 400},
		{"GET", "stats", "", 501}, {"GET", "tasks/" + syntheticID + "/details", "", 501}, {"DELETE", "tasks/cleanup", "", 501},
	} {
		h, _ := resumableHandler()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, uploadHTTPRequest(tc.method, "/api/files/upload/"+tc.path, tc.body))
		if w.Code != tc.status {
			t.Fatal(tc.path, w.Code, w.Body.String())
		}
	}
	h, repo := resumableHandler()
	r := uploadHTTPRequest("POST", "/api/files/upload/create", `{}`)
	r.Header.Del("Authorization")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 || repo.created != 0 {
		t.Fatal(w.Code)
	}
}

func chunkHTTPRequest(t *testing.T, field string, trailing bool) *http.Request {
	t.Helper()
	var b bytes.Buffer
	writer := multipart.NewWriter(&b)
	part, e := writer.CreateFormFile(field, "chunk.bin")
	if e != nil {
		t.Fatal(e)
	}
	part.Write([]byte("hel"))
	if trailing {
		writer.WriteField("metadata", "unexpected")
	}
	writer.Close()
	r := httptest.NewRequest("POST", "/api/files/upload/chunk/"+syntheticID+"/0", &b)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	r.Header.Set("Authorization", "Bearer synthetic")
	return r
}
func TestResumableHTTPValidatesMultipartBeforeCommitAndOnReplay(t *testing.T) {
	h, repo := resumableHandler()
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, chunkHTTPRequest(t, "chunk", false))
		if w.Code != 200 || repo.data != "hel" {
			t.Fatal(w.Code, w.Body.String())
		}
		if i == 1 && !strings.Contains(w.Body.String(), "Chunk already uploaded") {
			t.Fatal(w.Body.String())
		}
	}
	for _, r := range []*http.Request{chunkHTTPRequest(t, "file", false), chunkHTTPRequest(t, "chunk", true)} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 || repo.chunks != 2 {
			t.Fatal(w.Code, w.Body.String(), repo.chunks)
		}
	}
}
