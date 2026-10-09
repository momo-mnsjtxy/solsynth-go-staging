// SPDX-License-Identifier: AGPL-3.0-only
// Source-supported IP-check DTO/routes; independently owned proxy/data policy.
package ipcheck

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"solsynth.local/go-migration/ports/authority"
	"strings"
)

type Geo struct {
	City            *string  `json:"city"`
	Country         *string  `json:"country"`
	CountryCode     *string  `json:"country_code"`
	Subdivision     *string  `json:"subdivision"`
	SubdivisionCode *string  `json:"subdivision_code"`
	ContinentCode   *string  `json:"continent_code"`
	TimeZone        *string  `json:"time_zone"`
	Latitude        *float64 `json:"latitude"`
	Longitude       *float64 `json:"longitude"`
}
type Provider interface {
	Lookup(context.Context, netip.Addr) (*Geo, error)
}
type Authority interface{ AuthorizeDiagnostic(*http.Request) error }
type Config struct {
	Provider        Provider
	TrustedProxies  []netip.Prefix
	ForwardedHeader string
}
type Service struct {
	config Config
	auth   Authority
}
type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (e *Failure) Error() string { return e.Code }

var (
	ErrInvalid     = &Failure{"PASSPORT_IP_INVALID", "Invalid IP-check request.", 400}
	ErrUnavailable = &Failure{"PASSPORT_IP_GEO_UNAVAILABLE", "Caller geolocation is not configured.", 503}
	ErrNotFound    = &Failure{"PASSPORT_IP_GEO_NOT_FOUND", "Could not resolve geo location for client IP.", 404}
	ErrCoordinates = &Failure{"PASSPORT_IP_GEO_NO_COORDINATES", "Geo location has no coordinates.", 404}
	ErrForbidden   = &Failure{"FORBIDDEN", "Current global administrator authority is required.", 403}
)

func New(c Config, auth Authority) (*Service, error) {
	switch c.ForwardedHeader {
	case "", "X-Forwarded-For", "X-Real-IP", "CF-Connecting-IP":
	default:
		return nil, ErrInvalid
	}
	if len(c.TrustedProxies) > 128 || (len(c.TrustedProxies) == 0) != (c.ForwardedHeader == "") {
		return nil, ErrInvalid
	}
	for _, p := range c.TrustedProxies {
		if !p.IsValid() || p.Bits() == 0 || p != p.Masked() || p.Addr().Is4In6() {
			return nil, ErrInvalid
		}
	}
	c.TrustedProxies = append([]netip.Prefix(nil), c.TrustedProxies...)
	return &Service{c, auth}, nil
}
func (s *Service) trusted(a netip.Addr) bool {
	for _, p := range s.config.TrustedProxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
func ip(raw string) (netip.Addr, error) {
	a, e := netip.ParseAddr(strings.TrimSpace(raw))
	if e != nil || a.Zone() != "" {
		return netip.Addr{}, ErrInvalid
	}
	return a.Unmap(), nil
}
func (s *Service) client(r *http.Request) (client, peer netip.Addr, e error) {
	raw := r.RemoteAddr
	if host, _, x := net.SplitHostPort(raw); x == nil {
		raw = host
	}
	peer, e = ip(raw)
	if e != nil {
		return
	}
	client = peer
	if s.config.ForwardedHeader == "" || !s.trusted(peer) {
		return
	}
	values := r.Header.Values(s.config.ForwardedHeader)
	if len(values) == 0 {
		return
	}
	if len(values) != 1 || len(values[0]) > 4096 {
		return client, peer, ErrInvalid
	}
	parts := strings.Split(values[0], ",")
	if len(parts) > 32 || s.config.ForwardedHeader != "X-Forwarded-For" && len(parts) != 1 {
		return client, peer, ErrInvalid
	}
	addresses := make([]netip.Addr, len(parts))
	for i, p := range parts {
		addresses[i], e = ip(p)
		if e != nil {
			return
		}
	}
	for i := len(addresses) - 1; i >= 0 && s.trusted(client); i-- {
		client = addresses[i]
	}
	return
}
func Route(p string) bool { return p == "/api/ip-check" || p == "/api/ip-check/geo" }
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Vary", "Authorization")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, e error) {
	var a *authority.Failure
	if errors.As(e, &a) {
		write(w, a.Status, a)
		return
	}
	var f *Failure
	if errors.As(e, &f) {
		write(w, f.Status, f)
		return
	}
	write(w, 503, ErrUnavailable)
}
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s == nil || !Route(r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	if r.Method != "GET" {
		w.Header().Set("Allow", "GET")
		write(w, 405, &Failure{"METHOD_NOT_ALLOWED", "GET is required.", 405})
		return
	}
	if r.URL.RawQuery != "" || r.URL.RawPath != "" || r.URL.User != nil {
		fail(w, ErrInvalid)
		return
	}
	diagnostic := r.URL.Path == "/api/ip-check"
	if diagnostic {
		if s.auth == nil {
			fail(w, ErrForbidden)
			return
		}
		if e := s.auth.AuthorizeDiagnostic(r); e != nil {
			fail(w, e)
			return
		}
	}
	client, peer, e := s.client(r)
	if e != nil {
		fail(w, e)
		return
	}
	var g *Geo
	if s.config.Provider != nil {
		g, e = s.config.Provider.Lookup(r.Context(), client)
		if e != nil {
			fail(w, ErrUnavailable)
			return
		}
	}
	if g != nil && !validGeo(*g) {
		fail(w, ErrUnavailable)
		return
	}
	if !diagnostic {
		if s.config.Provider == nil {
			fail(w, ErrUnavailable)
			return
		}
		if g == nil {
			fail(w, ErrNotFound)
			return
		}
		if g.Latitude == nil || g.Longitude == nil {
			fail(w, ErrCoordinates)
			return
		}
		write(w, 200, g)
		return
	}
	out := diagnostics{ClientIP: client.String(), RemoteIP: peer.String(), Geo: g}
	out.XForwardedFor = header(r, "X-Forwarded-For")
	out.XForwardedProto = header(r, "X-Forwarded-Proto")
	out.XForwardedHost = header(r, "X-Forwarded-Host")
	out.XRealIP = header(r, "X-Real-IP")
	out.CFConnectingIP = header(r, "CF-Connecting-IP")
	lines := []string{}
	for _, name := range []string{"X-Forwarded-For", "X-Forwarded-Proto", "X-Forwarded-Host", "X-Real-IP", "CF-Connecting-IP", "User-Agent", "Accept", "Accept-Language"} {
		if v := header(r, name); v != nil {
			lines = append(lines, name+": "+*v)
		}
	}
	out.Headers = strings.Join(lines, "\n")
	write(w, 200, out)
}
func header(r *http.Request, key string) *string {
	v := r.Header.Get(key)
	if v == "" {
		return nil
	}
	if len(v) > 512 {
		v = v[:512]
	}
	v = strings.Map(func(c rune) rune {
		if c < 32 || c == 127 {
			return -1
		}
		return c
	}, v)
	return &v
}

type diagnostics struct {
	ClientIP        string  `json:"client_ip"`
	RemoteIP        string  `json:"remote_ip"`
	XForwardedFor   *string `json:"x_forwarded_for"`
	XForwardedProto *string `json:"x_forwarded_proto"`
	XForwardedHost  *string `json:"x_forwarded_host"`
	XRealIP         *string `json:"x_real_ip"`
	CFConnectingIP  *string `json:"cf_connecting_ip"`
	Geo             *Geo    `json:"geo"`
	Headers         string  `json:"headers"`
}
