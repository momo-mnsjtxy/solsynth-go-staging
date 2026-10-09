// SPDX-License-Identifier: AGPL-3.0-only
package livestreams

import (
	"context"
	"net/http"
	"strconv"
	"strings"
)

func (s *Service) outputsHTTP(w http.ResponseWriter, r *http.Request, ctx context.Context, actor string, parts []string) {
	repo, ok := s.Repository.(OutputRepository)
	if !ok {
		failure(w, ErrOutputUnavailable)
		return
	}
	var out any
	var e error
	switch {
	case len(parts) == 2:
		if r.Method == "GET" {
			out, e = repo.GetOutput(ctx, actor, parts[0], parts[1])
			break
		}
		if r.Method != "POST" {
			method(w, "GET, POST")
			return
		}
		if len(r.Header.Values("Idempotency-Key")) > 1 {
			failure(w, ErrInvalid)
			return
		}
		if parts[1] == "hls" {
			var q HLSRequest
			e = decode(w, r, &q, "destination_id", "playlist_name", "segment_duration", "segment_count", "layout", "hls_base_url")
			q.Key = r.Header.Get("Idempotency-Key")
			if e == nil {
				out, e = repo.StartHLS(ctx, actor, parts[0], q)
			}
		} else {
			var q EgressRequest
			e = decode(w, r, &q, "destination_ids", "rtmp_urls", "file_path")
			q.Key = r.Header.Get("Idempotency-Key")
			if e == nil {
				out, e = repo.StartEgress(ctx, actor, parts[0], q)
			}
		}
	case len(parts) == 3 && parts[2] == "stop":
		if r.Method != "POST" {
			method(w, "POST")
			return
		}
		e = empty(w, r)
		if e == nil {
			e = repo.StopOutput(ctx, actor, parts[0], parts[1])
		}
	case len(parts) == 4 && parts[1] == "hls" && parts[3] == "import":
		if r.Method != "POST" {
			method(w, "POST")
			return
		}
		if len(r.Header.Values("Idempotency-Key")) != 1 {
			e = ErrInvalid
			break
		}
		e = empty(w, r)
		if e == nil {
			out, e = repo.RequestArchiveImport(ctx, actor, parts[0], parts[2], r.Header.Get("Idempotency-Key"))
		}
	case len(parts) == 5 && parts[1] == "hls" && parts[3] == "imports":
		if r.Method != "GET" {
			method(w, "GET")
			return
		}
		out, e = repo.GetArchiveImport(ctx, actor, parts[0], parts[2], parts[4])
	case len(parts) == 4 && parts[1] == "hls":
		if r.Method != "GET" {
			method(w, "GET")
			return
		}
		var data Playback
		data, e = repo.OpenHLS(ctx, actor, parts[0], parts[2], parts[3])
		if e != nil {
			break
		}
		w.Header().Set("Content-Type", data.MIME)
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Length", strconv.Itoa(len(data.Body)))
		if r.URL.Query().Get("download") == "true" {
			w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(parts[3], `"`, "")+`"`)
		} else if r.URL.Query().Get("download") != "" {
			failure(w, ErrInvalid)
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write(data.Body)
		return
	default:
		failure(w, ErrNotFound)
		return
	}
	if e != nil {
		failure(w, e)
		return
	}
	respond(w, 200, out)
}
