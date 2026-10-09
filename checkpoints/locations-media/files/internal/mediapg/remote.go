// SPDX-License-Identifier: AGPL-3.0-only
package mediapg

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"solsynth.local/go-migration/ports/media"
	"strings"
	"time"
)

type HTTPSRemoteFetcher struct {
	hosts  map[string]bool
	client *http.Client
}

func NewHTTPSRemoteFetcher(hosts []string) (*HTTPSRemoteFetcher, error) {
	if len(hosts) == 0 || len(hosts) > 100 {
		return nil, media.ErrInvalid
	}
	f := &HTTPSRemoteFetcher{hosts: map[string]bool{}}
	for _, h := range hosts {
		h = strings.ToLower(h)
		if h == "" || strings.ContainsAny(h, "/:@*\\") || net.ParseIP(h) != nil {
			return nil, media.ErrInvalid
		}
		f.hosts[h] = true
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, MaxIdleConns: 8, MaxIdleConnsPerHost: 2, MaxConnsPerHost: 4, ResponseHeaderTimeout: 10 * time.Second, TLSHandshakeTimeout: 10 * time.Second, DisableCompression: true}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil || port != "443" || !f.hosts[strings.ToLower(host)] {
			return nil, media.ErrForbidden
		}
		ips, e := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if e != nil || len(ips) == 0 || len(ips) > 32 {
			return nil, media.ErrUnavailable
		}
		for _, ip := range ips {
			if !publicRemoteIP(ip) {
				return nil, media.ErrForbidden
			}
		}
		dialer := net.Dialer{Timeout: 10 * time.Second}
		for _, ip := range ips {
			conn, e := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if e == nil {
				return conn, nil
			}
		}
		return nil, media.ErrUnavailable
	}
	f.client = &http.Client{Transport: transport, Timeout: 45 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 || !f.urlOK(req.URL) {
			return media.ErrForbidden
		}
		return nil
	}}
	return f, nil
}
func publicRemoteIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() {
		return false
	}
	for _, s := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "::/96", "64:ff9b::/96", "64:ff9b:1::/48", "2002::/16", "2001::/32"} {
		if netip.MustParsePrefix(s).Contains(ip) {
			return false
		}
	}
	return true
}
func (f *HTTPSRemoteFetcher) urlOK(u *url.URL) bool {
	return u != nil && u.Scheme == "https" && u.User == nil && u.Fragment == "" && (u.Port() == "" || u.Port() == "443") && f.hosts[strings.ToLower(u.Hostname())]
}
func (f *HTTPSRemoteFetcher) Fetch(ctx context.Context, raw string, limit int64) (io.ReadCloser, error) {
	u, e := url.Parse(raw)
	if e != nil || !f.urlOK(u) || limit < 1 {
		return nil, media.ErrForbidden
	}
	req, e := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if e != nil {
		return nil, e
	}
	resp, e := f.client.Do(req)
	if e != nil {
		return nil, media.ErrUnavailable
	}
	if resp.StatusCode != 200 || resp.ContentLength > limit || resp.Header.Get("Content-Encoding") != "" {
		resp.Body.Close()
		return nil, media.ErrUnavailable
	}
	return &boundedRemoteBody{ReadCloser: resp.Body, left: limit}, nil
}

type boundedRemoteBody struct {
	io.ReadCloser
	left int64
}

func (b *boundedRemoteBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		return 0, errors.New("remote media size limit")
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, e := b.ReadCloser.Read(p)
	b.left -= int64(n)
	return n, e
}
