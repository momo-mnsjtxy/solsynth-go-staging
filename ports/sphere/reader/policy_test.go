// SPDX-License-Identifier: AGPL-3.0-only
package reader

import (
	"net/netip"
	"testing"
)

func TestPolicyNormalizationAndBoundaries(t *testing.T) {
	p, e := NewPolicy([]string{"https://Preview.Example:443"})
	if e != nil {
		t.Fatal(e)
	}
	got, e := p.Normalize("https://PREVIEW.EXAMPLE:443/A%2fb?q=Value+Case&x=%2F#fragment")
	if e != nil || got != "https://preview.example/A%2fb?q=Value+Case&x=%2F" {
		t.Fatal(got, e)
	}
	for _, raw := range []string{"http://preview.example/", "https://preview.example:8443/", "https://user:secret@preview.example/", "https://preview.example./", "https://preview.example\\@other.example/", "https://other.example/", "https://127.0.0.1/", "https://[::1]/", "https://preview.example/%zz", "https://preview.example/ a", "https://preview.example:/"} {
		if _, e = p.Normalize(raw); e == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	for _, raw := range []string{"https://preview.example/path", "https://preview.example?x=1", "https://preview.example#fragment", "https://preview.local"} {
		if _, e = NewPolicy([]string{raw}); e == nil {
			t.Errorf("accepted origin %q", raw)
		}
	}
	for _, raw := range []string{"https://preview.example/Case", "https://preview.example/case", "https://preview.example/path/", "https://preview.example/path", "https://preview.example/?q=%252F", "https://preview.example/?q=%2F"} {
		if got, e = p.Normalize(raw); e != nil || got != raw {
			t.Errorf("changed path/query %q -> %q %v", raw, got, e)
		}
	}
}
func TestPublicAddressRestrictions(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.1.2.3", "172.16.1.1", "192.168.1.1", "169.254.169.254", "100.64.1.1", "192.0.2.1", "198.18.1.1", "198.51.100.3", "203.0.113.5", "255.255.255.255", "0.1.2.3", "224.0.0.1", "::1", "::ffff:127.0.0.1", "fc00::1", "fe80::1", "2001:db8::1", "2002:5db8:d822::1", "64:ff9b::808:808", "3fff::1"} {
		if PublicAddress(netip.MustParseAddr(raw)) {
			t.Errorf("accepted reserved address %s", raw)
		}
	}
	for _, raw := range []string{"93.184.216.34", "8.8.8.8", "2606:4700:4700::1111", "::ffff:93.184.216.34"} {
		if !PublicAddress(netip.MustParseAddr(raw)) {
			t.Errorf("rejected public address %s", raw)
		}
	}
}
