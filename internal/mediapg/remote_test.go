// SPDX-License-Identifier: AGPL-3.0-only
package mediapg

import (
	"context"
	"net/netip"
	"testing"
)

func TestRemoteFetchAllowlistAndAddresses(t *testing.T) {
	f, e := NewHTTPSRemoteFetcher([]string{"media.example.com"})
	if e != nil {
		t.Fatal(e)
	}
	for _, raw := range []string{"http://media.example.com/file", "https://127.0.0.1/file", "https://media.example.com:444/file", "https://media.example.com@private.example/file", "file:///etc/passwd", "https://private.example/file", "https://media.example.com/file#secret"} {
		if _, e = f.Fetch(context.Background(), raw, 10); e == nil {
			t.Fatal("unsafe URL accepted", raw)
		}
	}
	for _, s := range []string{"127.0.0.1", "::1", "169.254.169.254", "10.1.2.3", "100.64.0.1", "198.18.0.1", "::ffff:127.0.0.1", "2001:db8::1"} {
		if publicRemoteIP(netip.MustParseAddr(s)) {
			t.Fatal("private/reserved address accepted", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "2606:4700:4700::1111"} {
		if !publicRemoteIP(netip.MustParseAddr(s)) {
			t.Fatal("public address rejected", s)
		}
	}
}
