// SPDX-License-Identifier: AGPL-3.0-only
package readerfetch

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"solsynth.local/go-migration/ports/sphere/reader"
	"strings"
	"testing"
)

func TestDomainVerificationPinsOriginBoundsBodyAndMatchesWholeLines(t *testing.T) {
	for _, tc := range []struct {
		body   string
		status int
		want   bool
		err    error
	}{
		{"other\n SYNTHETIC-NAME \r\n", 200, true, nil},
		{"synthetic-name.example\n", 200, false, nil},
		{"<html>synthetic-name</html>", 200, false, nil},
		{strings.Repeat("x", 1025), 200, false, reader.ErrLimit},
		{"synthetic-name", 302, false, reader.ErrFetch},
		{"synthetic-name", 500, false, reader.ErrFetch},
	} {
		f := syntheticFetcher(t)
		calls := 0
		f.exchange = func(_ context.Context, u *url.URL, address netip.Addr) (*http.Response, error) {
			calls++
			if u.String() != "https://preview.example/.well-known/dyson-domains.txt" || address.String() != "93.184.216.34" {
				t.Fatal(u, address)
			}
			return response(tc.status, tc.body, map[string]string{"Location": "https://preview.example/other"}), nil
		}
		ok, e := f.VerifyDomain(context.Background(), "PREVIEW.EXAMPLE", "synthetic-name")
		if ok != tc.want || !errors.Is(e, tc.err) || calls != 1 {
			t.Fatal(ok, e, calls, tc)
		}
	}
}
func TestDomainVerificationDeniesUnconfiguredAndPrivateDestinations(t *testing.T) {
	f := syntheticFetcher(t)
	if _, e := f.VerifyDomain(context.Background(), "unconfigured.example", "name"); !errors.Is(e, reader.ErrBlocked) {
		t.Fatal(e)
	}
	f.resolver = fakeResolver(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("127.0.0.1")}, nil
	})
	if _, e := f.VerifyDomain(context.Background(), "preview.example", "name"); !errors.Is(e, reader.ErrBlocked) {
		t.Fatal(e)
	}
}
