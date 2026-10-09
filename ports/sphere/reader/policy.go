// SPDX-License-Identifier: AGPL-3.0-only
package reader

import (
	"net/netip"
	"net/url"
	"strings"
	"unicode/utf8"
)

const MaxURLBytes = 4096

// Policy is an exact origin allowlist. Construction and normalization perform
// no DNS or outbound I/O. Network adapters must separately validate addresses.
type Policy struct{ origins map[string]bool }

func (p Policy) Configured() bool { return len(p.origins) > 0 }
func NewPolicy(origins []string) (Policy, error) {
	p := Policy{origins: map[string]bool{}}
	if len(origins) == 0 || len(origins) > 128 {
		return Policy{}, ErrInvalid
	}
	for _, raw := range origins {
		u, e := canonicalURL(raw)
		if e != nil || u.Path != "/" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			return Policy{}, ErrInvalid
		}
		p.origins[u.Scheme+"://"+u.Host] = true
	}
	return p, nil
}
func (p Policy) Normalize(raw string) (string, error) {
	u, e := canonicalURL(raw)
	if e != nil {
		return "", e
	}
	if !p.origins[u.Scheme+"://"+u.Host] {
		return "", ErrBlocked
	}
	u.Fragment = ""
	u.RawFragment = ""
	return u.String(), nil
}
func canonicalURL(raw string) (*url.URL, error) {
	if raw == "" || len(raw) > MaxURLBytes || !utf8.ValidString(raw) {
		return nil, ErrInvalid
	}
	for _, c := range raw {
		if c <= 32 || c == 127 || c == '\\' {
			return nil, ErrInvalid
		}
	}
	u, e := url.Parse(raw)
	if e != nil || !strings.EqualFold(u.Scheme, "https") || u.Opaque != "" || u.User != nil || u.Host == "" {
		return nil, ErrInvalid
	}
	host := strings.ToLower(u.Hostname())
	if len(host) > 253 || !strings.Contains(host, ".") || strings.HasSuffix(host, ".") {
		return nil, ErrInvalid
	}
	if _, e := netip.ParseAddr(host); e == nil {
		return nil, ErrBlocked
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return nil, ErrInvalid
		}
		for _, c := range label {
			if c < 'a' || c > 'z' {
				if c < '0' || c > '9' {
					if c != '-' {
						return nil, ErrInvalid
					}
				}
			}
		}
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".lan", ".home", ".onion"} {
		if strings.HasSuffix(host, suffix) {
			return nil, ErrBlocked
		}
	}
	if u.Port() != "" && u.Port() != "443" {
		return nil, ErrBlocked
	}
	if u.Host != u.Hostname() && !strings.EqualFold(u.Host, u.Hostname()+":443") {
		return nil, ErrInvalid
	}
	u.Scheme, u.Host = "https", host
	if u.Path == "" {
		u.Path = "/"
	}
	return u, nil
}

// PublicAddress excludes private, local, documentation, translation, benchmark
// and reserved ranges. IPv6 is limited to currently assigned global space.
func PublicAddress(a netip.Addr) bool {
	if a.Zone() != "" {
		return false
	}
	a = a.Unmap()
	if !a.IsValid() || !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.Zone() != "" {
		return false
	}
	if a.Is6() && !netip.MustParsePrefix("2000::/3").Contains(a) {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

var blockedPrefixes = func() []netip.Prefix {
	out := []netip.Prefix{}
	for _, s := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001::/23", "2001:db8::/32", "2002::/16", "3ffe::/16", "3fff::/20"} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()
