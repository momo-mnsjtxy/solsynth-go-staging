// SPDX-License-Identifier: AGPL-3.0-only
package media

import (
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"solsynth.local/go-migration/ports/authority"
)

func (s *Service) resumableHTTP(w http.ResponseWriter, r *http.Request) bool {
	const prefix = "/api/files/upload/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		return false
	}
	repo, ok := s.Repository.(ResumableRepository)
	if !ok {
		failure(w, ErrUnsupported)
		return true
	}
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	parts := strings.Split(rest, "/")
	permission := ReadPermission
	if r.Method == http.MethodPost || r.Method == http.MethodDelete {
		permission = CreatePermission
	}
	p, err := s.principal(r, permission)
	if err != nil {
		failure(w, err)
		return true
	}
	if len(r.Header.Values("Content-Type")) > 1 || len(r.Header.Values("Idempotency-Key")) > 1 {
		failure(w, ErrInvalid)
		return true
	}
	if rest == "tasks" || rest == "tasks/recent" {
		if r.Method != http.MethodGet {
			resumableMethod(w, "GET")
			return true
		}
		query, err := taskQuery(r, rest == "tasks/recent")
		if err != nil {
			failure(w, err)
			return true
		}
		tasks, err := repo.ListResumable(r.Context(), p.Account.ID, query)
		if err != nil {
			failure(w, err)
		} else {
			reply(w, 200, tasks)
		}
		return true
	}
	if len(r.URL.Query()) != 0 {
		failure(w, ErrInvalid)
		return true
	}
	if rest == "create" {
		if r.Method != http.MethodPost {
			resumableMethod(w, "POST")
			return true
		}
		var request CreateUploadRequest
		if err := decodeTaskRequest(w, r, &request); err != nil {
			failure(w, err)
			return true
		}
		if request.FileSize > s.MaxFileBytes {
			failure(w, ErrTooLarge)
			return true
		}
		task, err := repo.CreateResumable(r.Context(), p.Account.ID, request, r.Header.Get("Idempotency-Key"))
		if err != nil {
			failure(w, err)
			return true
		}
		reply(w, 200, map[string]any{"file_exists": false, "file": nil, "task_id": task.TaskID, "chunk_size": task.ChunkSize, "chunks_count": task.ChunksCount})
		return true
	}
	if len(parts) == 3 && parts[0] == "chunk" {
		if r.Method != http.MethodPost {
			resumableMethod(w, "POST")
			return true
		}
		index, err := strconv.Atoi(parts[2])
		if err != nil || index < 0 || index >= MaxUploadChunks || strconv.Itoa(index) != parts[2] || !authority.ValidID(parts[1]) {
			failure(w, ErrInvalid)
			return true
		}
		bound := min(s.MaxFileBytes, MaxChunkBytes) + directOverhead
		if r.ContentLength > bound {
			failure(w, ErrTooLarge)
			return true
		}
		r.Body = http.MaxBytesReader(w, r.Body, bound)
		reader, err := r.MultipartReader()
		if err != nil {
			failure(w, ErrInvalid)
			return true
		}
		part, err := reader.NextRawPart()
		if err != nil || part.FormName() != "chunk" || part.FileName() == "" {
			failure(w, ErrInvalid)
			return true
		}
		replayed, err := repo.UploadChunk(r.Context(), p.Account.ID, parts[1], index, &singleChunkReader{part: part, reader: reader})
		if err != nil {
			failure(w, err)
			return true
		}
		if replayed {
			reply(w, 200, map[string]string{"message": "Chunk already uploaded"})
		} else {
			w.WriteHeader(200)
		}
		return true
	}
	if len(parts) == 2 && (parts[0] == "complete" || parts[0] == "progress" || parts[0] == "resume") {
		if !authority.ValidID(parts[1]) {
			failure(w, ErrNotFound)
			return true
		}
		if parts[0] == "complete" {
			if r.Method != http.MethodPost {
				resumableMethod(w, "POST")
				return true
			}
			if r.ContentLength != 0 {
				failure(w, ErrInvalid)
				return true
			}
			if err := repo.CompleteResumable(r.Context(), p.Account.ID, parts[1]); err != nil {
				failure(w, err)
				return true
			}
			reply(w, 202, map[string]string{"message": "Upload is being processed", "taskId": parts[1], "status": "processing"})
		} else {
			if r.Method != http.MethodGet {
				resumableMethod(w, "GET")
				return true
			}
			task, err := repo.ResumableProgress(r.Context(), p.Account.ID, parts[1])
			if err != nil {
				failure(w, err)
			} else {
				reply(w, 200, task)
			}
		}
		return true
	}
	if len(parts) == 1 && authority.ValidID(parts[0]) {
		if r.Method != http.MethodDelete {
			resumableMethod(w, "DELETE")
			return true
		}
		if r.ContentLength != 0 {
			failure(w, ErrInvalid)
			return true
		}
		if err := repo.CancelResumable(r.Context(), p.Account.ID, parts[0]); err != nil {
			failure(w, err)
		} else {
			reply(w, 200, map[string]string{"message": "Upload task cancelled"})
		}
		return true
	}
	failure(w, ErrUnsupported)
	return true
}

func resumableMethod(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	reply(w, 405, map[string]string{"code": "METHOD_NOT_ALLOWED"})
}

func decodeTaskRequest(w http.ResponseWriter, r *http.Request, request *CreateUploadRequest) error {
	if r.Header.Get("Content-Type") != "application/json" {
		return ErrInvalid
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return ErrInvalid
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return ErrInvalid
		}
		key, ok := token.(string)
		if !ok {
			return ErrInvalid
		}
		if _, exists := fields[key]; exists {
			return ErrInvalid
		}
		var value json.RawMessage
		if err = decoder.Decode(&value); err != nil {
			return ErrInvalid
		}
		fields[key] = value
	}
	if _, err = decoder.Token(); err != nil {
		return ErrInvalid
	}
	if _, err = decoder.Token(); !errors.Is(err, io.EOF) {
		return ErrInvalid
	}
	body, err := json.Marshal(fields)
	if err != nil {
		return ErrInvalid
	}
	d := json.NewDecoder(strings.NewReader(string(body)))
	d.DisallowUnknownFields()
	if err = d.Decode(request); err != nil {
		return ErrInvalid
	}
	return nil
}

// Validate all multipart framing before the store can publish the chunk. Even
// an idempotent replay consumes and validates the supplied body.
type singleChunkReader struct {
	part   *multipart.Part
	reader *multipart.Reader
	done   bool
}

func (r *singleChunkReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	n, err := r.part.Read(p)
	if !errors.Is(err, io.EOF) {
		return n, err
	}
	if _, err = r.reader.NextRawPart(); !errors.Is(err, io.EOF) {
		return n, ErrInvalid
	}
	r.done = true
	return n, io.EOF
}

func taskQuery(r *http.Request, recent bool) (UploadTaskQuery, error) {
	q := UploadTaskQuery{SortBy: "lastActivity", Descending: true, Limit: 50}
	if recent {
		q.Limit = 10
	}
	for key, values := range r.URL.Query() {
		if len(values) != 1 {
			return q, ErrInvalid
		}
		v := values[0]
		switch key {
		case "status":
			if recent {
				return q, ErrInvalid
			}
			q.Status = v
		case "sortBy":
			if recent {
				return q, ErrInvalid
			}
			q.SortBy = v
		case "sortDescending":
			if recent || (v != "true" && v != "false") {
				return q, ErrInvalid
			}
			q.Descending = v == "true"
		case "offset", "limit":
			if recent && key == "offset" {
				return q, ErrInvalid
			}
			n, e := strconv.Atoi(v)
			if e != nil || strconv.Itoa(n) != v || n < 0 {
				return q, ErrInvalid
			}
			if key == "offset" {
				q.Offset = n
			} else {
				q.Limit = n
			}
		default:
			return q, ErrInvalid
		}
	}
	return q, nil
}
