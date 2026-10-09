// SPDX-License-Identifier: AGPL-3.0-only
package blogs

import "testing"

func TestBlogURLAndDomainRejectAuthorityConfusion(t *testing.T) {
	for _, s := range []string{"", "localhost", "127.0.0.1", "foo.internal", "example.com/path", "example.com:443", "user@example.com", "foo..example", "foo.example.", "☃.example", "https://example.com"} {
		if _, e := NormalizeDomain(s); e == nil {
			t.Fatal(s)
		}
	}
	for _, s := range []string{"https://user:pass@example.com/", "http://example.com/", "https://example.com:8080/", "https://example.com\\@attacker.example/", "https://127.0.0.1/", "https://example.com/a\nb", "javascript:alert(1)"} {
		if _, _, e := NormalizeURL(s); e == nil {
			t.Fatal(s)
		}
	}
	canonical, host, e := NormalizeURL(" HTTPS://Blog.Example:443/Case?Q=Value#fragment ")
	if e != nil || canonical != "https://blog.example/Case?Q=Value" || host != "blog.example" {
		t.Fatal(canonical, host, e)
	}
}
