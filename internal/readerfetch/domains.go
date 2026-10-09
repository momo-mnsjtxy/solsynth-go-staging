// SPDX-License-Identifier: AGPL-3.0-only
package readerfetch

import (
	"context"
	"io"
	"net/url"
	"solsynth.local/go-migration/ports/sphere/blogs"
	"solsynth.local/go-migration/ports/sphere/reader"
	"strings"
)

// VerifyDomain shares the configured public-address-pinned HTTPS transport.
// Unlike ordinary previews it refuses every redirect: proof must be served by
// the claimed origin itself. No cookies, credentials or proxy are transmitted.
func (f *Fetcher) VerifyDomain(ctx context.Context, domain, publisher string) (bool, error) {
	if f == nil {
		return false, blogs.ErrUnavailable
	}
	domain, e := blogs.NormalizeDomain(domain)
	if e != nil {
		return false, e
	}
	raw, e := f.config.Policy.Normalize("https://" + domain + "/.well-known/dyson-domains.txt")
	if e != nil {
		return false, e
	}
	select {
	case f.slots <- struct{}{}:
		defer func() { <-f.slots }()
	default:
		return false, reader.ErrBusy
	}
	ctx, cancel := context.WithTimeout(ctx, f.config.Timeout)
	defer cancel()
	u, _ := url.Parse(raw)
	addresses, e := f.resolver.LookupNetIP(ctx, "ip", domain)
	if e != nil || len(addresses) == 0 {
		return false, reader.ErrFetch
	}
	if len(addresses) > 16 {
		return false, reader.ErrLimit
	}
	for _, address := range addresses {
		if !reader.PublicAddress(address) {
			return false, reader.ErrBlocked
		}
	}
	resp, e := f.exchange(ctx, u, addresses[0].Unmap())
	if e != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		return false, reader.ErrFetch
	}
	if resp == nil || resp.Body == nil {
		return false, reader.ErrFetch
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, reader.ErrFetch
	}
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return false, reader.ErrFetch
	}
	limit := int64(65536)
	if f.config.MaxBytes < limit {
		limit = f.config.MaxBytes
	}
	if resp.ContentLength > limit {
		return false, reader.ErrLimit
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if e != nil || ctx.Err() != nil {
		return false, reader.ErrFetch
	}
	if int64(len(b)) > limit {
		return false, reader.ErrLimit
	}
	return blogs.MatchesPublisher(b, publisher), nil
}
