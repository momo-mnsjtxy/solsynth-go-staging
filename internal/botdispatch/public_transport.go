// SPDX-License-Identifier: AGPL-3.0-only
package botdispatch

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/netip"
	"solsynth.local/go-migration/ports/sphere/reader"
	"time"
)

type publicResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}
type publicTransport struct {
	policy   reader.Policy
	timeout  time.Duration
	resolver publicResolver
	exchange func(*http.Request, netip.Addr) (*http.Response, error)
}

// PublicClient is a configured exact-origin transport. Construction performs
// no DNS or network I/O; every dispatch checks all answers and pins its dial.
func PublicClient(policy reader.Policy, timeout time.Duration) (*http.Client, error) {
	if !policy.Configured() || timeout < time.Millisecond || timeout > 30*time.Second {
		return nil, ErrDelivery
	}
	tr := &publicTransport{policy: policy, timeout: timeout, resolver: net.DefaultResolver}
	tr.exchange = tr.send
	return &http.Client{Transport: tr, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
func (t *publicTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r == nil || r.URL == nil || r.Method != http.MethodPost {
		return rejectPublicRequest(r)
	}
	canonical, e := t.policy.Normalize(r.URL.String())
	if e != nil || canonical != r.URL.String() || r.URL.RawQuery != "" || r.URL.Fragment != "" || r.Host != "" && r.Host != r.URL.Host {
		return rejectPublicRequest(r)
	}
	ips, e := t.resolver.LookupNetIP(r.Context(), "ip", r.URL.Hostname())
	if e != nil || len(ips) == 0 || len(ips) > 16 {
		return rejectPublicRequest(r)
	}
	for _, ip := range ips {
		if !reader.PublicAddress(ip) {
			return rejectPublicRequest(r)
		}
	}
	return t.exchange(r, ips[0].Unmap())
}

// Before handing off to the HTTP transport, this RoundTripper owns the body
// and must close it even when destination validation fails.
func rejectPublicRequest(r *http.Request) (*http.Response, error) {
	if r != nil && r.Body != nil {
		_ = r.Body.Close()
	}
	return nil, ErrDelivery
}

func (t *publicTransport) send(r *http.Request, ip netip.Addr) (*http.Response, error) {
	host := r.URL.Hostname()
	tr := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}, DisableKeepAlives: true, DisableCompression: true, MaxResponseHeaderBytes: 16384, ResponseHeaderTimeout: t.timeout, DialContext: func(ctx context.Context, network, destination string) (net.Conn, error) {
		if destination != net.JoinHostPort(host, "443") {
			return nil, ErrDelivery
		}
		return (&net.Dialer{Timeout: t.timeout}).DialContext(ctx, network, net.JoinHostPort(ip.String(), "443"))
	}}
	response, e := tr.RoundTrip(r)
	if e != nil {
		tr.CloseIdleConnections()
		return nil, ErrDelivery
	}
	response.Body = &publicBody{ReadCloser: response.Body, transport: tr}
	return response, nil
}

type publicBody struct {
	io.ReadCloser
	transport *http.Transport
}

func (b *publicBody) Close() error {
	e := b.ReadCloser.Close()
	b.transport.CloseIdleConnections()
	return e
}
