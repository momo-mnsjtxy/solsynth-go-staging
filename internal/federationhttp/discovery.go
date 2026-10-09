// SPDX-License-Identifier: AGPL-3.0-only
package federationhttp

import (
	"encoding/json"
	"encoding/xml"
	"mime"
	"net/http"
	"net/url"
	f "solsynth.local/go-migration/ports/federation"
	"strings"
)

func (h Handler) hostMeta(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		method(w, "GET")
		return
	}
	if r.URL.RawQuery != "" {
		fail(w, f.ErrInvalid)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/xrd+xml; charset=utf-8")
	_ = xml.NewEncoder(w).Encode(struct {
		XMLName xml.Name `xml:"XRD"`
		XMLNS   string   `xml:"xmlns,attr"`
		Link    struct {
			Rel      string `xml:"rel,attr"`
			Template string `xml:"template,attr"`
		} `xml:"Link"`
	}{XMLNS: "http://docs.oasis-open.org/ns/xri/xrd-1.0", Link: struct {
		Rel      string `xml:"rel,attr"`
		Template string `xml:"template,attr"`
	}{Rel: "lrdd", Template: h.Config.Origin + "/.well-known/webfinger?resource={uri}"}})
}
func (h Handler) serverActor(w http.ResponseWriter, r *http.Request) {
	suffix := strings.TrimPrefix(r.URL.Path, "/activitypub/actor")
	if suffix == "/inbox" {
		fail(w, f.ErrUnsupported)
		return
	}
	if !f.Has([]string{"", "/main-key", "/outbox", "/followers", "/following"}, suffix) {
		http.NotFound(w, r)
		return
	}
	if r.Method != "GET" {
		method(w, "GET")
		return
	}
	page := false
	if suffix == "/outbox" || suffix == "/followers" || suffix == "/following" {
		if r.URL.RawQuery != "" {
			if r.URL.RawQuery != "page=true" {
				fail(w, f.ErrInvalid)
				return
			}
			page = true
		}
	} else if r.URL.RawQuery != "" {
		fail(w, f.ErrInvalid)
		return
	}
	repo, ok := h.Repository.(f.ServerRepository)
	if !ok {
		fail(w, f.ErrNotFound)
		return
	}
	a, e := repo.ServerActor(r.Context())
	if e != nil {
		fail(w, e)
		return
	}
	switch suffix {
	case "":
		write(w, 200, a, f.MediaType)
	case "/main-key":
		write(w, 200, f.PublicKeyDocument(a.PublicKey), f.MediaType)
	default:
		id := a.ID + suffix
		collection := map[string]any{"@context": f.Context, "id": id, "type": "OrderedCollection", "totalItems": 0, "orderedItems": []any{}, "first": id + "?page=true"}
		if page {
			collection["id"] = id + "?page=true"
			collection["type"] = "OrderedCollectionPage"
			collection["partOf"] = id
			delete(collection, "first")
		}
		write(w, 200, collection, f.MediaType)
	}
}
func (h Handler) discovery(w http.ResponseWriter, r *http.Request, actor, publisher string) {
	repo, ok := h.Repository.(f.DiscoveryRepository)
	if !ok {
		fail(w, f.ErrUnavailable)
		return
	}
	var job f.DiscoveryJob
	var e error
	switch r.Method {
	case "POST":
		if r.URL.RawQuery != "" {
			fail(w, f.ErrInvalid)
			return
		}
		media, _, parseError := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if parseError != nil || media != "application/json" || len(r.Header.Values("Content-Type")) != 1 {
			fail(w, f.ErrInvalid)
			return
		}
		var input struct {
			Resource string `json:"resource"`
		}
		body, readError := readBody(w, r, 16384)
		var fields map[string]json.RawMessage
		if readError != nil || json.Unmarshal(body, &fields) != nil || len(fields) != 1 || fields["resource"] == nil || json.Unmarshal(fields["resource"], &input.Resource) != nil || input.Resource == "" {
			fail(w, f.ErrInvalid)
			return
		}
		job, e = repo.RequestDiscovery(r.Context(), actor, publisher, input.Resource)
	case "GET":
		q, parseError := url.ParseQuery(r.URL.RawQuery)
		if parseError != nil || len(q) != 1 || len(q["resource"]) != 1 {
			fail(w, f.ErrInvalid)
			return
		}
		job, e = repo.Discovery(r.Context(), actor, publisher, q.Get("resource"))
	default:
		method(w, "GET, POST")
		return
	}
	if e != nil {
		fail(w, e)
		return
	}
	write(w, 200, job, "application/json")
}
