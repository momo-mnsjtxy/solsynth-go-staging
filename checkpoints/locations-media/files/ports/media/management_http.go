// SPDX-License-Identifier: AGPL-3.0-only
package media

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"solsynth.local/go-migration/ports/authority"
	"strconv"
	"strings"
)

func decodeManagement(w http.ResponseWriter, r *http.Request, out any) error {
	if r.Header.Get("Content-Type") != "application/json" {
		return ErrInvalid
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	b, e := io.ReadAll(r.Body)
	if e != nil {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	if e = uniqueJSON(d, 0); e != nil {
		return e
	}
	if _, e = d.Token(); e != io.EOF {
		return ErrInvalid
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(out); e != nil {
		return ErrInvalid
	}
	return nil
}
func uniqueJSON(d *json.Decoder, depth int) error {
	if depth > 32 {
		return ErrInvalid
	}
	v, e := d.Token()
	if e != nil {
		return ErrInvalid
	}
	delimiter, ok := v.(json.Delim)
	if !ok {
		return nil
	}
	if delimiter == '{' {
		seen := map[string]bool{}
		for d.More() {
			v, e = d.Token()
			if e != nil {
				return ErrInvalid
			}
			k, ok := v.(string)
			if !ok || seen[k] {
				return ErrInvalid
			}
			seen[k] = true
			if e = uniqueJSON(d, depth+1); e != nil {
				return e
			}
		}
	} else if delimiter == '[' {
		for d.More() {
			if e = uniqueJSON(d, depth+1); e != nil {
				return e
			}
		}
	} else {
		return ErrInvalid
	}
	_, e = d.Token()
	if e != nil {
		return ErrInvalid
	}
	return nil
}
func managementPage(r *http.Request) (FileQuery, error) {
	q := FileQuery{Take: 50, Order: "date", Descending: true}
	for key, values := range r.URL.Query() {
		if len(values) != 1 {
			return q, ErrInvalid
		}
		v := values[0]
		switch key {
		case "take", "offset":
			n, e := strconv.Atoi(v)
			if e != nil || strconv.Itoa(n) != v {
				return q, ErrInvalid
			}
			if key == "take" {
				q.Take = n
			} else {
				q.Offset = n
			}
		case "query":
			q.Query = v
		case "order":
			q.Order = v
		case "orderDesc":
			if v != "true" && v != "false" {
				return q, ErrInvalid
			}
			q.Descending = v == "true"
		case "pool":
			q.PoolID = v
		default:
			return q, ErrInvalid
		}
	}
	return q, nil
}
func (s *Service) managementHTTP(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	parts := strings.Split(strings.TrimPrefix(p, "/api/"), "/")
	if len(parts) < 1 {
		return false
	}
	manage, ok := s.Repository.(ManagementRepository)
	if !ok {
		return false
	}
	recognized := p == "/api/pools" || strings.HasPrefix(p, "/api/bundles") || p == "/api/files/upload/stats" || p == "/api/files/upload/tasks/cleanup" || len(parts) == 5 && parts[0] == "files" && parts[1] == "upload" && parts[2] == "tasks" && parts[4] == "details" || p == "/api/files/folders" || p == "/api/files/batches" || p == "/api/files/me" || p == "/api/files/unindexed" || len(parts) == 3 && parts[0] == "files" && (parts[2] == "children" || parts[2] == "hierarchy" || parts[2] == "meta" || parts[2] == "sensitive" || parts[2] == "name" || parts[2] == "references" || parts[2] == "e2ee") || p == "/api/files/upload/remote"
	if !recognized {
		if p != "/api/files/compositions/live-photo" && !(len(parts) == 3 && parts[0] == "files" && parts[1] == "compositions") {
			return false
		}
	}
	// Only list endpoints and the explicit bundle passcode read accept query
	// fields. Ignored query inputs must not silently change mutation semantics.
	list := r.Method == "GET" && (p == "/api/files/me" || p == "/api/files/unindexed" || p == "/api/bundles/me" || len(parts) == 3 && parts[0] == "files" && parts[2] == "children")
	bundleRead := r.Method == "GET" && len(parts) == 2 && parts[0] == "bundles" && authority.ValidID(parts[1])
	if len(r.URL.Query()) != 0 && !list && !bundleRead {
		failure(w, ErrInvalid)
		return true
	}
	permission := ReadPermission
	if r.Method == "POST" {
		permission = CreatePermission
	}
	if r.Method == "PATCH" || r.Method == "PUT" {
		permission = UpdatePermission
	}
	if r.Method == "DELETE" {
		permission = DeletePermission
	}
	if p == "/api/pools" && r.Method == "POST" {
		permission = "media.pools.manage"
	}
	principal, e := s.principal(r, permission)
	if e != nil {
		failure(w, e)
		return true
	}
	actor := principal.Account.ID
	var out any
	status := 200
	if strings.HasPrefix(p, "/api/bundles") {
		bundles, ok := s.Repository.(BundleRepository)
		if !ok {
			failure(w, ErrUnavailable)
			return true
		}
		switch {
		case len(parts) == 1 && r.Method == "POST" || len(parts) == 2 && authority.ValidID(parts[1]) && r.Method == "PUT":
			var v BundleRequest
			e = decodeManagement(w, r, &v)
			id := ""
			if len(parts) == 2 {
				id = parts[1]
			}
			if e == nil {
				out, e = bundles.SaveBundle(r.Context(), actor, id, v)
			}
		case len(parts) == 2 && parts[1] == "me" && r.Method == "GET":
			q, err := managementPage(r)
			e = err
			if e == nil {
				out, e = bundles.Bundles(r.Context(), actor, q.Take, q.Offset)
			}
		case len(parts) == 2 && authority.ValidID(parts[1]) && r.Method == "GET":
			for key, v := range r.URL.Query() {
				if key != "passcode" || len(v) != 1 || len(v[0]) > 256 {
					e = ErrInvalid
				}
			}
			if e == nil {
				out, e = bundles.Bundle(r.Context(), actor, parts[1], r.URL.Query().Get("passcode"))
			}
		case len(parts) == 2 && authority.ValidID(parts[1]) && r.Method == "DELETE":
			e = bundles.DeleteBundle(r.Context(), actor, parts[1])
			status = 204
		default:
			e = ErrNotFound
		}
	} else {
		switch {
		case p == "/api/pools" && r.Method == "GET":
			out, e = manage.Pools(r.Context(), actor)
		case p == "/api/files/compositions/live-photo" && r.Method == "POST":
			compositions, ok := s.Repository.(CompositionRepository)
			if !ok {
				e = ErrUnavailable
				break
			}
			var v LivePhotoRequest
			e = decodeManagement(w, r, &v)
			if e == nil {
				out, e = compositions.ComposeLivePhoto(r.Context(), actor, v)
			}
		case len(parts) == 3 && parts[1] == "compositions" && r.Method == "GET":
			compositions, ok := s.Repository.(CompositionRepository)
			if !ok {
				e = ErrUnavailable
				break
			}
			out, e = compositions.LivePhoto(r.Context(), actor, parts[2])
		case len(parts) == 3 && parts[1] == "compositions" && r.Method == "DELETE":
			compositions, ok := s.Repository.(CompositionRepository)
			if !ok {
				e = ErrUnavailable
				break
			}
			e = compositions.DeleteLivePhoto(r.Context(), actor, parts[2])
			status = 204
		case p == "/api/pools" && r.Method == "POST":
			var v Pool
			e = decodeManagement(w, r, &v)
			if e == nil {
				out, e = manage.CreatePool(r.Context(), actor, v)
			}
		case p == "/api/files/upload/stats" && r.Method == "GET":
			out, e = manage.UploadStatistics(r.Context(), actor)
		case p == "/api/files/upload/tasks/cleanup" && r.Method == "DELETE":
			var n int
			n, e = manage.CleanupUploadTasks(r.Context(), actor)
			out = map[string]int{"count": n}
		case len(parts) == 5 && parts[4] == "details" && r.Method == "GET":
			out, e = manage.UploadTaskDetails(r.Context(), actor, parts[3])
		case p == "/api/files/upload/remote" && r.Method == "POST":
			remote, ok := s.Repository.(RemoteUploadRepository)
			if !ok {
				e = ErrUnavailable
				break
			}
			var v RemoteUploadRequest
			e = decodeManagement(w, r, &v)
			if e == nil {
				var f File
				var replay bool
				f, replay, e = remote.UploadRemote(r.Context(), actor, v)
				out = map[string]any{"file": f, "replayed": replay}
			}
		case p == "/api/files/folders" && r.Method == "POST":
			var v struct {
				Name     string  `json:"name"`
				ParentID *string `json:"parent_id"`
			}
			e = decodeManagement(w, r, &v)
			if e == nil {
				out, e = manage.CreateFolder(r.Context(), actor, v.Name, v.ParentID)
			}
		case p == "/api/files/batches" && r.Method == "DELETE":
			var v struct {
				IDs []string `json:"file_ids"`
			}
			e = decodeManagement(w, r, &v)
			if e == nil {
				var n int
				n, e = manage.DeleteFiles(r.Context(), actor, v.IDs)
				out = map[string]int{"count": n}
			}
		case (p == "/api/files/me" || p == "/api/files/unindexed" || len(parts) == 3 && parts[2] == "children") && r.Method == "GET":
			q, err := managementPage(r)
			e = err
			q.Unindexed = p == "/api/files/unindexed"
			q.All = p == "/api/files/me"
			if len(parts) == 3 && parts[1] != "root" {
				q.ParentID = parts[1]
			}
			if e == nil {
				var total int64
				out, total, e = manage.ListFiles(r.Context(), actor, q)
				w.Header().Set("X-Total", strconv.FormatInt(total, 10))
			}
		case len(parts) == 3 && parts[2] == "references" && r.Method == "GET":
			out, e = manage.FileReferences(r.Context(), actor, parts[1])
		case len(parts) == 3 && parts[2] == "e2ee" && r.Method == "GET":
			var v FileRecord
			v, e = manage.FileRecord(r.Context(), actor, parts[1])
			if e == nil && v.EncryptionScheme == "" {
				e = ErrNotFound
			}
			out = map[string]string{"scheme": v.EncryptionScheme, "header": v.EncryptionHeader, "signature": v.EncryptionSignature}
		case len(parts) == 3 && (r.Method == "PATCH" || r.Method == "PUT"):
			var v FilePatch
			if parts[2] == "meta" {
				v.Meta = map[string]json.RawMessage{}
				e = decodeManagement(w, r, &v.Meta)
			} else {
				e = decodeManagement(w, r, &v)
				v.ParentPresent = parts[2] == "hierarchy"
			}
			if e == nil {
				out, e = manage.PatchFile(r.Context(), actor, parts[1], v)
			}
		default:
			e = ErrNotFound
		}
	}
	if e != nil {
		failure(w, e)
	} else if status == 204 {
		w.WriteHeader(status)
	} else {
		reply(w, status, out)
	}
	return true
}
