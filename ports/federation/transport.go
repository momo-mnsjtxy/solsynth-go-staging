// SPDX-License-Identifier: AGPL-3.0-only
package federation

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
)

// HTTPTransport never trusts ambient proxies or follows redirects. Every
// resolved address is checked; dialing the selected numeric address prevents a
// second DNS resolution changing the validated destination. No I/O on creation.
type HTTPTransport struct {
	config   Config
	resolver *net.Resolver
}

func NewHTTPTransport(c Config) (*HTTPTransport, error) {
	if e := c.Validate(); e != nil {
		return nil, e
	}
	return &HTTPTransport{config: c, resolver: net.DefaultResolver}, nil
}
func publicAddress(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsUnspecified() {
		return false
	}
	// Currently supported public IPv6 peers must use the global-unicast block.
	// IsGlobalUnicast alone also accepts deprecated site-local/reserved space.
	if a.Is6() && !netip.MustParsePrefix("2000::/3").Contains(a) {
		return false
	}
	// Also exclude shared, benchmarking, documentation, reserved, and translation
	// space; these are not suitable remote federation destinations.
	for _, cidr := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20"} {
		if netip.MustParsePrefix(cidr).Contains(a) {
			return false
		}
	}
	return true
}
func (t *HTTPTransport) Send(ctx context.Context, r *http.Request) (int, error) {
	if t == nil || r == nil || r.URL == nil || r.Method != "POST" || !t.config.Allows(r.URL.String()) || r.Host != r.URL.Host {
		return 0, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(ctx, t.config.DeliveryTimeout)
	defer cancel()
	addresses, e := t.resolver.LookupIPAddr(ctx, r.URL.Hostname())
	if e != nil || len(addresses) == 0 {
		return 0, ErrUnavailable
	}
	for _, a := range addresses {
		if !publicAddress(a.IP) {
			return 0, ErrForbidden
		}
	}
	host := r.URL.Hostname()
	port := 443
	tr := &http.Transport{
		Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host},
		DisableKeepAlives: true, MaxResponseHeaderBytes: 16384, ResponseHeaderTimeout: t.config.DeliveryTimeout,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			expected := net.JoinHostPort(host, strconv.Itoa(port))
			if address != expected {
				return nil, errors.New("federation: destination changed")
			}
			d := net.Dialer{Timeout: t.config.DeliveryTimeout}
			return d.DialContext(ctx, network, net.JoinHostPort(addresses[0].IP.String(), strconv.Itoa(port)))
		},
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: t.config.DeliveryTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, e := client.Do(r.Clone(ctx))
	if e != nil {
		return 0, ErrUnavailable
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	return response.StatusCode, nil
}
