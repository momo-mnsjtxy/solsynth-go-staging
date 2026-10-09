// SPDX-License-Identifier: AGPL-3.0-only
package federation

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/url"
)

// HTTPFetcher is explicitly constructed and never uses ambient proxy settings,
// authentication, cookies, redirects, or a second DNS lookup after validation.
type discoveryResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type HTTPFetcher struct {
	config   Config
	allowed  map[string]string
	resolver discoveryResolver
}

func NewHTTPFetcher(c Config, targets []DiscoveryTarget) (*HTTPFetcher, error) {
	if c.Validate() != nil || len(targets) == 0 || len(targets) > 128 {
		return nil, ErrInvalid
	}
	allowed := map[string]string{}
	for _, target := range targets {
		if target.Validate(c) != nil {
			return nil, ErrInvalid
		}
		for uri, accept := range map[string]string{target.WebFingerURL(): "application/jrd+json", target.Actor: MediaType} {
			if prior, ok := allowed[uri]; ok && prior != accept {
				return nil, ErrInvalid
			}
			allowed[uri] = accept
		}
	}
	c.AllowedOrigins = append([]string{}, c.AllowedOrigins...)
	return &HTTPFetcher{config: c, allowed: allowed, resolver: net.DefaultResolver}, nil
}
func (t *HTTPFetcher) Fetch(ctx context.Context, uri, accept string) (FetchResponse, error) {
	var out FetchResponse
	if t == nil || t.allowed[uri] != accept || accept == "" {
		return out, ErrForbidden
	}
	u, e := url.Parse(uri)
	if e != nil {
		return out, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(ctx, t.config.DeliveryTimeout)
	defer cancel()
	addresses, e := t.resolver.LookupIPAddr(ctx, u.Hostname())
	if e != nil || len(addresses) == 0 {
		return out, ErrUnavailable
	}
	for _, ip := range addresses {
		if !publicAddress(ip.IP) {
			return out, ErrForbidden
		}
	}
	host := u.Hostname()
	tr := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}, DisableKeepAlives: true, DisableCompression: true, MaxResponseHeaderBytes: 16384, ResponseHeaderTimeout: t.config.DeliveryTimeout, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != net.JoinHostPort(host, "443") {
			return nil, ErrForbidden
		}
		dialer := net.Dialer{Timeout: t.config.DeliveryTimeout}
		return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].IP.String(), "443"))
	}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: t.config.DeliveryTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, e := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if e != nil {
		return out, ErrInvalid
	}
	request.Header.Set("Accept", accept)
	response, e := client.Do(request)
	if e != nil {
		return out, ErrUnavailable
	}
	defer response.Body.Close()
	out.Status = response.StatusCode
	out.ContentType = response.Header.Get("Content-Type")
	if out.Status != 200 {
		return out, nil
	}
	if response.Header.Get("Content-Encoding") != "" || len(response.Header.Values("Content-Type")) != 1 || response.ContentLength > MaxBody {
		return out, ErrInvalid
	}
	out.Body, e = io.ReadAll(io.LimitReader(response.Body, MaxBody+1))
	if e != nil {
		return FetchResponse{}, ErrUnavailable
	}
	if len(out.Body) > MaxBody {
		return FetchResponse{}, ErrInvalid
	}
	return out, nil
}
