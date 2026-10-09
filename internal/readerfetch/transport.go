// SPDX-License-Identifier: AGPL-3.0-only
package readerfetch

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"solsynth.local/go-migration/ports/sphere/reader"
)

type Config struct {
	Policy        reader.Policy
	Timeout       time.Duration
	MaxBytes      int64
	MaxConcurrent int
	MaxRedirects  int
}
type resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}
type Fetcher struct {
	config   Config
	resolver resolver
	slots    chan struct{}
	exchange func(context.Context, *url.URL, netip.Addr) (*http.Response, error)
}

// New explicitly enables outbound work for configured callers; construction
// itself performs no network I/O. Tests inject synthetic transport internally.
func New(c Config) (*Fetcher, error) {
	if !c.Policy.Configured() {
		return nil, reader.ErrInvalid
	}
	if c.Timeout == 0 {
		c.Timeout = 3 * time.Second
	}
	if c.MaxBytes == 0 {
		c.MaxBytes = 2 << 20
	}
	if c.MaxConcurrent == 0 {
		c.MaxConcurrent = 8
	}
	if c.Timeout < time.Second || c.Timeout > 10*time.Second || c.MaxBytes < 1024 || c.MaxBytes > 2<<20 || c.MaxConcurrent < 1 || c.MaxConcurrent > 32 || c.MaxRedirects < 0 || c.MaxRedirects > 5 {
		return nil, reader.ErrInvalid
	}
	f := &Fetcher{config: c, resolver: net.DefaultResolver, slots: make(chan struct{}, c.MaxConcurrent)}
	f.exchange = f.send
	return f, nil
}
func (f *Fetcher) Fetch(ctx context.Context, raw string) (reader.Link, error) {
	if f == nil {
		return reader.Link{}, reader.ErrUnavailable
	}
	select {
	case f.slots <- struct{}{}:
		defer func() { <-f.slots }()
	default:
		return reader.Link{}, reader.ErrBusy
	}
	ctx, cancel := context.WithTimeout(ctx, f.config.Timeout)
	defer cancel()
	seen := map[string]bool{}
	for hop := 0; ; hop++ {
		canonical, e := f.config.Policy.Normalize(raw)
		if e != nil {
			return reader.Link{}, e
		}
		if seen[canonical] {
			return reader.Link{}, reader.ErrFetch
		}
		seen[canonical] = true
		u, _ := url.Parse(canonical)
		addresses, e := f.resolver.LookupNetIP(ctx, "ip", u.Hostname())
		if e != nil || len(addresses) == 0 {
			return reader.Link{}, reader.ErrFetch
		}
		if len(addresses) > 16 {
			return reader.Link{}, reader.ErrLimit
		}
		for _, address := range addresses {
			if !reader.PublicAddress(address) {
				return reader.Link{}, reader.ErrBlocked
			}
		}
		response, e := f.exchange(ctx, u, addresses[0].Unmap())
		if e != nil {
			if response != nil && response.Body != nil {
				response.Body.Close()
			}
			return reader.Link{}, reader.ErrFetch
		}
		if response == nil || response.Body == nil {
			return reader.Link{}, reader.ErrFetch
		}
		if response.StatusCode == 301 || response.StatusCode == 302 || response.StatusCode == 303 || response.StatusCode == 307 || response.StatusCode == 308 {
			location := response.Header.Get("Location")
			response.Body.Close()
			if hop >= f.config.MaxRedirects || location == "" || len(location) > reader.MaxURLBytes {
				return reader.Link{}, reader.ErrFetch
			}
			next, e := url.Parse(location)
			if e != nil {
				return reader.Link{}, reader.ErrInvalid
			}
			raw = u.ResolveReference(next).String()
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			response.Body.Close()
			return reader.Link{}, reader.ErrFetch
		}
		encoding := response.Header.Get("Content-Encoding")
		if encoding != "" && !strings.EqualFold(encoding, "identity") {
			response.Body.Close()
			return reader.Link{}, reader.ErrFetch
		}
		if response.ContentLength > f.config.MaxBytes {
			response.Body.Close()
			return reader.Link{}, reader.ErrLimit
		}
		body, e := io.ReadAll(io.LimitReader(response.Body, f.config.MaxBytes+1))
		response.Body.Close()
		if e != nil || ctx.Err() != nil {
			return reader.Link{}, reader.ErrFetch
		}
		if int64(len(body)) > f.config.MaxBytes {
			return reader.Link{}, reader.ErrLimit
		}
		contentType, params, e := mime.ParseMediaType(response.Header.Get("Content-Type"))
		if e != nil {
			contentType = "application/octet-stream"
		}
		if contentType != "text/html" {
			return reader.Link{URL: canonical, Title: u.Hostname(), ContentType: &contentType}, nil
		}
		if charset := strings.ToLower(params["charset"]); charset != "" && charset != "utf-8" && charset != "us-ascii" {
			return reader.Link{}, reader.ErrFetch
		}
		return Extract(f.config.Policy, canonical, body)
	}
}
func (f *Fetcher) send(ctx context.Context, u *url.URL, address netip.Addr) (*http.Response, error) {
	host := u.Hostname()
	tr := &http.Transport{
		Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host},
		DisableKeepAlives: true, DisableCompression: true, MaxResponseHeaderBytes: 32768, ResponseHeaderTimeout: f.config.Timeout,
		DialContext: func(ctx context.Context, network, destination string) (net.Conn, error) {
			if destination != net.JoinHostPort(host, "443") {
				return nil, errors.New("reader destination changed")
			}
			return (&net.Dialer{Timeout: f.config.Timeout}).DialContext(ctx, network, net.JoinHostPort(address.String(), "443"))
		},
	}
	request, e := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if e != nil {
		return nil, e
	}
	request.Header.Set("User-Agent", "SolsynthOwnedReader/1.0")
	request.Header.Set("Accept", "text/html, application/xhtml+xml;q=0.8, */*;q=0.1")
	request.Header.Set("Accept-Encoding", "identity")
	response, e := tr.RoundTrip(request)
	if e != nil {
		tr.CloseIdleConnections()
		return nil, e
	}
	response.Body = &closeTransportBody{ReadCloser: response.Body, transport: tr}
	return response, nil
}

type closeTransportBody struct {
	io.ReadCloser
	transport *http.Transport
}

func (b *closeTransportBody) Close() error {
	e := b.ReadCloser.Close()
	b.transport.CloseIdleConnections()
	return e
}
