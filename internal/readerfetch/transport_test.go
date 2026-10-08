// SPDX-License-Identifier: AGPL-3.0-only
package readerfetch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"solsynth.local/go-migration/ports/sphere/reader"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeResolver func(context.Context, string, string) ([]netip.Addr, error)

func (f fakeResolver) LookupNetIP(c context.Context, n, h string) ([]netip.Addr, error) {
	return f(c, n, h)
}
func syntheticFetcher(t *testing.T) *Fetcher {
	t.Helper()
	f, e := New(Config{Policy: testPolicy(t), MaxRedirects: 3, MaxBytes: 1024, MaxConcurrent: 1, Timeout: time.Second})
	if e != nil {
		t.Fatal(e)
	}
	f.resolver = fakeResolver(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	})
	f.exchange = func(context.Context, *url.URL, netip.Addr) (*http.Response, error) {
		t.Error("unconfigured synthetic exchange called")
		return nil, errors.New("unconfigured")
	}
	return f
}
func response(status int, body string, headers map[string]string) *http.Response {
	h := make(http.Header)
	for k, v := range headers {
		h.Set(k, v)
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))}
}
func TestFetchValidatesEveryRedirectAndResolvedAddress(t *testing.T) {
	f := syntheticFetcher(t)
	calls := 0
	f.exchange = func(ctx context.Context, u *url.URL, ip netip.Addr) (*http.Response, error) {
		calls++
		if ip.String() != "93.184.216.34" {
			t.Fatal(ip)
		}
		if calls == 1 {
			return response(302, "", map[string]string{"Location": "/final"}), nil
		}
		return response(200, "<title>Synthetic final</title>", map[string]string{"Content-Type": "text/html; charset=utf-8"}), nil
	}
	v, e := f.Fetch(context.Background(), "https://preview.example/start")
	if e != nil || calls != 2 || v.Title != "Synthetic final" || v.URL != "https://preview.example/final" {
		t.Fatal(v, calls, e)
	}
	calls = 0
	resolutions := 0
	f.resolver = fakeResolver(func(context.Context, string, string) ([]netip.Addr, error) {
		resolutions++
		if resolutions == 2 {
			return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("10.0.0.1")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	})
	if _, e = f.Fetch(context.Background(), "https://preview.example/start"); !errors.Is(e, reader.ErrBlocked) || calls != 1 {
		t.Fatal("redirect destination was not revalidated", calls, e)
	}
	for _, target := range []string{"http://preview.example/final", "https://unconfigured.example/final", "https://user:pass@preview.example/final"} {
		f = syntheticFetcher(t)
		calls = 0
		f.exchange = func(context.Context, *url.URL, netip.Addr) (*http.Response, error) {
			calls++
			return response(302, "", map[string]string{"Location": target}), nil
		}
		if _, e = f.Fetch(context.Background(), "https://preview.example/start"); e == nil || calls != 1 {
			t.Fatal(target, calls, e)
		}
	}
}
func TestFetchBoundsConcurrencyBodiesAndErrors(t *testing.T) {
	for _, tc := range []struct {
		status  int
		body    string
		headers map[string]string
		want    error
	}{{200, strings.Repeat("a", 1025), nil, reader.ErrLimit}, {500, "", nil, reader.ErrFetch}, {200, "compressed", map[string]string{"Content-Encoding": "gzip"}, reader.ErrFetch}, {200, "<title>unsupported charset</title>", map[string]string{"Content-Type": "text/html;charset=shift_jis"}, reader.ErrFetch}, {302, "", map[string]string{"Location": "/"}, reader.ErrFetch}} {
		f := syntheticFetcher(t)
		f.exchange = func(context.Context, *url.URL, netip.Addr) (*http.Response, error) {
			return response(tc.status, tc.body, tc.headers), nil
		}
		if _, e := f.Fetch(context.Background(), "https://preview.example/"); !errors.Is(e, tc.want) {
			t.Fatal(e, tc.want)
		}
	}
	f := syntheticFetcher(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	var requests atomic.Int32
	f.exchange = func(ctx context.Context, u *url.URL, ip netip.Addr) (*http.Response, error) {
		requests.Add(1)
		close(entered)
		select {
		case <-release:
			return response(200, "synthetic bytes", map[string]string{"Content-Type": "application/pdf"}), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	go func() { _, e := f.Fetch(context.Background(), "https://preview.example/file"); done <- e }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("synthetic fetch did not start")
	}
	if _, e := f.Fetch(context.Background(), "https://preview.example/other"); !errors.Is(e, reader.ErrBusy) {
		t.Fatal(e)
	}
	close(release)
	if e := <-done; e != nil || requests.Load() != 1 {
		t.Fatal(requests.Load(), e)
	}
	f = syntheticFetcher(t)
	f.exchange = func(context.Context, *url.URL, netip.Addr) (*http.Response, error) {
		return response(200, "synthetic bytes", map[string]string{"Content-Type": "application/pdf"}), nil
	}
	v, e := f.Fetch(context.Background(), "https://preview.example/file")
	if e != nil || v.Title != "preview.example" || value(v.ContentType) != "application/pdf" {
		t.Fatal(v, e)
	}
}

func TestFetchDeadlineUnknownLengthAndRedirectBudget(t *testing.T) {
	f := syntheticFetcher(t)
	f.exchange = func(context.Context, *url.URL, netip.Addr) (*http.Response, error) {
		r := response(200, strings.Repeat("x", 1025), nil)
		r.ContentLength = -1
		return r, nil
	}
	if _, e := f.Fetch(context.Background(), "https://preview.example/"); !errors.Is(e, reader.ErrLimit) {
		t.Fatal("unknown-length response escaped byte limit", e)
	}
	requests := 0
	f.exchange = func(context.Context, *url.URL, netip.Addr) (*http.Response, error) {
		requests++
		return response(302, "", map[string]string{"Location": "/hop/" + strconv.Itoa(requests)}), nil
	}
	if _, e := f.Fetch(context.Background(), "https://preview.example/"); !errors.Is(e, reader.ErrFetch) || requests != 4 {
		t.Fatal("redirect budget", requests, e)
	}
	f.resolver = fakeResolver(func(ctx context.Context, network, host string) ([]netip.Addr, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, e := f.Fetch(ctx, "https://preview.example/"); !errors.Is(e, reader.ErrFetch) || !errors.Is(ctx.Err(), context.DeadlineExceeded) || requests != 4 {
		t.Fatal("deadline did not bound resolution", requests, e, ctx.Err())
	}
}
