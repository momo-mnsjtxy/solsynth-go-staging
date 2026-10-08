// SPDX-License-Identifier: AGPL-3.0-only
package signin

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"unicode/utf8"
)

func ContactRouteMatches(path string) bool {
	return path == "/stargate/contacts" || strings.HasPrefix(path, "/stargate/contacts/")
}
func (s *Service) ContactHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ContactRouteMatches(r.URL.Path) || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.User != nil || strings.ContainsAny(r.URL.Path, "\\\x00") {
			failure(w, ErrInput)
			return
		}
		for k := range r.Header {
			k = strings.ToLower(k)
			if k == "x-account-id" || k == "x-user-id" || k == "x-role" || k == "proxy-authorization" || strings.HasPrefix(k, "x-test-") || strings.HasPrefix(k, "x-synthetic-") {
				failure(w, authority.ErrForbidden)
				return
			}
		}
		token, e := authority.BearerToken(r)
		if e != nil {
			failure(w, e)
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/stargate/contacts"), "/")
		if r.URL.Path == "/stargate/contacts" {
			switch r.Method {
			case "GET":
				out, e := s.ListContacts(r.Context(), token)
				if e != nil {
					failure(w, e)
					return
				}
				respond(w, 200, out)
			case "POST":
				typ, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
				if e != nil || typ != "application/json" {
					failure(w, ErrInput)
					return
				}
				raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
				if e != nil || !utf8.Valid(raw) || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
					failure(w, ErrInput)
					return
				}
				input, e := decodeContactInput(raw)
				if e != nil {
					failure(w, ErrInput)
					return
				}
				out, e := s.CreateContact(r.Context(), token, input)
				if e != nil {
					failure(w, e)
					return
				}
				respond(w, 201, out)
			default:
				w.WriteHeader(405)
			}
			return
		}
		if len(parts) < 2 || len(parts) > 3 || !authority.ValidID(parts[1]) {
			failure(w, ErrInput)
			return
		}
		id := parts[1]
		if len(parts) == 2 && r.Method == "DELETE" {
			e = s.RemoveContact(r.Context(), token, id)
			if e != nil {
				failure(w, e)
				return
			}
			respond(w, 204, nil)
			return
		}
		if len(parts) == 3 {
			switch parts[2] {
			case "verify":
				if r.Method != "POST" {
					break
				}
				e = s.SendContactVerification(r.Context(), token, id)
				if e != nil {
					failure(w, e)
					return
				}
				respond(w, 202, map[string]string{"state": "verification_sent"})
				return
			case "primary":
				if r.Method != "POST" {
					break
				}
				out, e := s.MakeContactPrimary(r.Context(), token, id)
				if e != nil {
					failure(w, e)
					return
				}
				respond(w, 200, out)
				return
			case "public":
				if r.Method != "POST" && r.Method != "DELETE" {
					break
				}
				out, e := s.PublishContact(r.Context(), token, id, r.Method == "POST")
				if e != nil {
					failure(w, e)
					return
				}
				respond(w, 200, out)
				return
			}
		}
		w.WriteHeader(405)
	})
}

func decodeContactInput(raw []byte) (out ContactInput, e error) {
	if len(raw) > 8192 || !utf8.Valid(raw) {
		return out, ErrInput
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, e := d.Token()
	if e != nil || token != json.Delim('{') {
		return out, ErrInput
	}
	seen := map[string]bool{}
	for d.More() {
		token, e = d.Token()
		key, ok := token.(string)
		if e != nil || !ok || seen[key] {
			return out, ErrInput
		}
		seen[key] = true
		switch key {
		case "type":
			var value *int
			e = d.Decode(&value)
			if value == nil {
				return out, ErrInput
			}
			out.Type = *value
		case "content":
			e = d.Decode(&out.Content)
		default:
			return out, ErrInput
		}
		if e != nil {
			return out, ErrInput
		}
	}
	if token, e = d.Token(); e != nil || token != json.Delim('}') || !seen["type"] || !seen["content"] {
		return out, ErrInput
	}
	if _, e = d.Token(); e != io.EOF {
		return out, ErrInput
	}
	return out, ValidateContact(&out)
}
