// SPDX-License-Identifier: AGPL-3.0-only
package tencenttranslation

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"solsynth.local/go-migration/ports/sphere/translation"
	"strings"
	"testing"
	"time"
)

type fakeResolver func(context.Context, string, string) ([]netip.Addr, error)

func (f fakeResolver) LookupNetIP(c context.Context, n, h string) ([]netip.Addr, error) {
	return f(c, n, h)
}
func syntheticProvider(t *testing.T) *Provider {
	// Independently invented test-only ID/key, never a credential provisioned by
	// Tencent. The fixed vector below was computed with Python hashlib/hmac.
	t.Helper()
	p, e := New(Config{SecretID: "AKIDSYNTHETIC0001", SecretKey: []byte("synthetic-secret-material-for-tests"), Region: "ap-guangzhou", MaxConcurrent: 1, Now: func() time.Time { return time.Unix(1791390600, 0).UTC() }})
	if e != nil {
		t.Fatal(e)
	}
	p.resolver = fakeResolver(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	})
	p.exchange = func(context.Context, *http.Request, netip.Addr) (*http.Response, error) {
		return nil, errors.New("synthetic transport is not configured")
	}
	return p
}
func syntheticResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, ContentLength: int64(len(body)), Body: io.NopCloser(strings.NewReader(body))}
}
func TestTencentSignedRequestAndResponse(t *testing.T) {
	p := syntheticProvider(t)
	calls := 0
	p.exchange = func(ctx context.Context, r *http.Request, ip netip.Addr) (*http.Response, error) {
		calls++
		b, e := io.ReadAll(r.Body)
		if e != nil {
			t.Fatal(e)
		}
		wantBody := `{"SourceText":"synthetic hello","Source":"auto","Target":"fr","ProjectId":0}`
		wantAuth := "TC3-HMAC-SHA256 Credential=AKIDSYNTHETIC0001/2026-10-07/tmt/tc3_request, SignedHeaders=content-type;host, Signature=c8a0272c1bd4b52be49582da770d54219276d7a45e849a1648a704f9793fbca2"
		if string(b) != wantBody || r.Method != "POST" || r.URL.String() != "https://tmt.tencentcloudapi.com/" || r.Host != host || ip.String() != "93.184.216.34" || r.Header.Get("Authorization") != wantAuth || r.Header.Get("X-TC-Action") != "TextTranslate" || r.Header.Get("X-TC-Version") != "2018-03-21" || r.Header.Get("X-TC-Timestamp") != "1791390600" || r.Header.Get("X-TC-Region") != "ap-guangzhou" || r.Header.Get("Cookie") != "" || r.Header.Get("Accept-Encoding") != "identity" {
			t.Fatal("synthetic request contract mismatch")
		}
		return syntheticResponse(200, `{"Response":{"TargetText":"Synthetic French output","Source":"en","Target":"fr","RequestId":"synthetic"}}`), nil
	}
	text, e := p.Translate(context.Background(), translation.Request{Text: "synthetic hello", Target: "fr"})
	if e != nil || text != "Synthetic French output" || calls != 1 {
		t.Fatal(text, calls, e)
	}
	if _, e = p.Translate(context.Background(), translation.Request{Text: "synthetic hello", Target: "fr"}); !errors.Is(e, translation.ErrBusy) || calls != 1 {
		t.Fatal("per-process request rate", calls, e)
	}
}
func TestTencentRejectsMalformedFailedAndOversizedResponses(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{{302, `{}`}, {500, `{}`}, {200, `null`}, {200, `{"Response":{"TargetText":"x","Target":"de"}}`}, {200, `{"Response":{"TargetText":"x","Target":"fr","Error":{"Code":"SyntheticError","Message":"must not be forwarded"}}}`}, {200, `{"Response":{"TargetText":"","Target":"fr"}}`}, {200, `{"Response":{"TargetText":"x","Target":"fr"}} {}`}, {200, strings.Repeat("x", 65537)}} {
		p := syntheticProvider(t)
		p.exchange = func(context.Context, *http.Request, netip.Addr) (*http.Response, error) {
			r := syntheticResponse(tc.status, tc.body)
			r.ContentLength = -1
			return r, nil
		}
		if value, e := p.Translate(context.Background(), translation.Request{Text: "synthetic", Target: "fr"}); !errors.Is(e, translation.ErrProvider) || value != "" {
			t.Fatal("provider failure returned output", value, e)
		}
	}
}
func TestTencentDestinationDeadlineAndConcurrencyBounds(t *testing.T) {
	p := syntheticProvider(t)
	calls := 0
	p.resolver = fakeResolver(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("127.0.0.1")}, nil
	})
	p.exchange = func(context.Context, *http.Request, netip.Addr) (*http.Response, error) {
		calls++
		return syntheticResponse(200, `{}`), nil
	}
	if _, e := p.Translate(context.Background(), translation.Request{Text: "synthetic", Target: "fr"}); !errors.Is(e, translation.ErrProvider) || calls != 0 {
		t.Fatal("unsuitable DNS destination reached transport", calls, e)
	}
	p = syntheticProvider(t)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	p.exchange = func(ctx context.Context, r *http.Request, a netip.Addr) (*http.Response, error) {
		close(entered)
		select {
		case <-release:
			return syntheticResponse(200, `{"Response":{"TargetText":"Synthetic output","Target":"fr"}}`), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	go func() {
		_, e := p.Translate(context.Background(), translation.Request{Text: "synthetic", Target: "fr"})
		done <- e
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("synthetic request not entered")
	}
	if _, e := p.Translate(context.Background(), translation.Request{Text: "synthetic", Target: "fr"}); !errors.Is(e, translation.ErrBusy) {
		t.Fatal(e)
	}
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	p = syntheticProvider(t)
	p.resolver = fakeResolver(func(ctx context.Context, n, h string) ([]netip.Addr, error) { <-ctx.Done(); return nil, ctx.Err() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, e := p.Translate(ctx, translation.Request{Text: "synthetic", Target: "fr"}); !errors.Is(e, translation.ErrProvider) || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatal(e, ctx.Err())
	}
}
