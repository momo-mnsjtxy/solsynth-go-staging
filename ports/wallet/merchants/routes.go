// SPDX-License-Identifier: AGPL-3.0-only
package merchants

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func date(value string, end bool) (time.Time, error) {
	if len(value) == 10 {
		d, e := time.Parse("2006-01-02", value)
		if e != nil {
			return d, ErrInvalid
		}
		if end {
			d = d.AddDate(0, 0, 1)
		}
		return d, nil
	}
	d, e := time.Parse(time.RFC3339Nano, value)
	if e != nil {
		return d, ErrInvalid
	}
	return d.UTC(), nil
}
func query(r *http.Request, catalog bool) (Query, error) {
	p := Query{Take: 20}
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		return p, ErrInvalid
	}
	for k, v := range q {
		if len(v) != 1 {
			return p, ErrInvalid
		}
		allowed := k == "take" || k == "offset"
		if !catalog {
			allowed = allowed || k == "from" || k == "to" || k == "currency" || k == "status" || k == "app_id" || k == "product_identifier"
		}
		if !allowed {
			return p, ErrUnsupported
		}
	}
	for key, value := range map[string]*int{"take": &p.Take, "offset": &p.Offset} {
		if q.Has(key) {
			*value, e = strconv.Atoi(q.Get(key))
			if e != nil {
				return p, ErrInvalid
			}
		}
	}
	if p.Take < 1 || p.Take > 100 || p.Offset < 0 || p.Offset > 100000 {
		return p, ErrInvalid
	}
	if q.Has("status") {
		v, e := strconv.Atoi(q.Get("status"))
		if e != nil {
			return p, ErrInvalid
		}
		p.Status = &v
	}
	if q.Has("from") {
		d, e := date(q.Get("from"), false)
		if e != nil {
			return p, e
		}
		p.From = &d
	}
	if q.Has("to") {
		d, e := date(q.Get("to"), true)
		if e != nil {
			return p, e
		}
		p.To = &d
	}
	p.Currency = q.Get("currency")
	p.AppID = q.Get("app_id")
	p.ProductIdentifier = q.Get("product_identifier")
	return p, nil
}
func NewHandler(repo Repository, auth Auth) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if repo == nil || auth == nil {
			failure(w, ErrUnavailable)
			return
		}
		actor, e := auth.Authenticate(r)
		if e != nil {
			failure(w, e)
			return
		}
		if actor == "" {
			failure(w, ErrUnauthenticated)
			return
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		var out any
		status := 200
		if len(parts) >= 4 && strings.Join(parts[:4], "/") == "api/admin/subscriptions/catalog" {
			if e = auth.Authorize(r, "subscriptions.groups.manage"); e != nil {
				failure(w, e)
				return
			}
			switch {
			case len(parts) == 4 && r.Method == "GET":
				var q Query
				q, e = query(r, true)
				var total int64
				if e == nil {
					out, total, e = repo.Catalog(r.Context(), actor, q)
					w.Header().Set("X-Total", strconv.FormatInt(total, 10))
				}
			case len(parts) == 4 && r.Method == "POST":
				var v DefinitionInput
				e = decode(w, r, &v)
				if e == nil {
					var d Definition
					var created bool
					d, created, e = repo.UpsertDefinition(r.Context(), actor, v)
					out = d
					if created {
						status = 201
						w.Header().Set("Location", "/api/admin/subscriptions/catalog/"+url.PathEscape(d.Identifier))
					}
				}
			case len(parts) == 5 && r.Method == "GET":
				out, e = repo.Definition(r.Context(), actor, parts[4])
			case len(parts) == 5 && r.Method == "DELETE":
				e = empty(w, r)
				if e == nil {
					e = repo.DeleteDefinition(r.Context(), actor, parts[4])
					status = 204
				}
			default:
				http.NotFound(w, r)
				return
			}
		} else if len(parts) >= 3 && parts[0] == "api" && parts[1] == "merchants" {
			publisher := parts[2]
			switch {
			case len(parts) == 3 && r.Method == "GET":
				out, e = repo.Profile(r.Context(), actor, publisher)
			case len(parts) == 6 && parts[3] == "apps" && parts[5] == "wallet":
				switch r.Method {
				case "GET":
					out, e = repo.Binding(r.Context(), actor, publisher, parts[4])
				case "PATCH":
					e = auth.Authorize(r, "merchants.manage")
					var v WalletPatch
					if e == nil {
						e = decode(w, r, &v)
					}
					if e == nil {
						out, e = repo.BindWallet(r.Context(), actor, publisher, parts[4], v)
					}
				default:
					http.NotFound(w, r)
					return
				}
			case (len(parts) == 4 || len(parts) == 5 && parts[4] == "report") && parts[3] == "orders" && r.Method == "GET":
				if publisher == "me" {
					publisher = ""
					e = auth.Authorize(r, "orders.view")
				}
				var q Query
				if e == nil {
					q, e = query(r, false)
				}
				if e == nil {
					if len(parts) == 5 {
						out, e = repo.Report(r.Context(), actor, publisher, q)
					} else {
						var n int64
						out, n, e = repo.Orders(r.Context(), actor, publisher, q)
						w.Header().Set("X-Total", strconv.FormatInt(n, 10))
					}
				}
			case len(parts) >= 4 && parts[3] == "settlements":
				e = ErrUnsupported
			default:
				http.NotFound(w, r)
				return
			}
		} else {
			http.NotFound(w, r)
			return
		}
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, status, out)
	})
}
