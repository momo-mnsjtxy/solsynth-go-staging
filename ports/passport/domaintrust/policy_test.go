// SPDX-License-Identifier: AGPL-3.0-only
package domaintrust

import (
	"strings"
	"testing"
)

func TestStrictURLNormalization(t *testing.T) {
	for _, tc := range []struct {
		url, host string
		port      int
	}{{"https://EXAMPLE.test./private?q=secret#frag", "example.test", 0}, {"https://bücher.test/", "xn--bcher-kva.test", 0}, {"https://[2001:db8::1]:8443/path", "2001:db8::1", 8443}, {"https://example.test:443/", "example.test", 0}, {"http://example.test:80/", "example.test", 0}, {"https://[::ffff:127.0.0.1]/", "127.0.0.1", 0}} {
		got, e := Parse(tc.url)
		if e != nil || got.Host != tc.host {
			t.Fatal(tc, got, e)
		}
		port := 0
		if got.Port != nil {
			port = *got.Port
		}
		if port != tc.port {
			t.Fatal(tc, got)
		}
	}
	for _, bad := range []string{"", "example.test", "ftp://example.test/", "https://user:pass@example.test/", "https://@example.test/", "https://example.test:", "https://example.test:65536", "https://example.test:abc", "https://[example.test]/", "https://[fe80::1%25zone]/", "https://127.000.0.1/", "https://2130706433/", "https://0x7f000001/", "https://example.test/a%0d%0aX", "https://example.test/?x=%ZZ", "https://example.test/\n", "https://example.test\\@evil.test/", "https://-bad.test/", "https://example..test/", "https://example.test/" + strings.Repeat("x", 8192)} {
		if _, e := Parse(bad); e == nil {
			t.Fatal("accepted", bad)
		}
	}
}
func TestSourceWildcardSemantics(t *testing.T) {
	for _, tc := range []struct {
		p, h string
		want bool
	}{{"*", "x.test", true}, {"*.example.test", "example.test", false}, {"*.example.test", "a.b.example.test", true}, {"**.example.test", "example.test", true}, {"**.example.test", "a.example.test", true}, {"*.example.test", "badexample.test", false}, {"x?.test", "xa.test", false}, {"*x?.test", "aaxq.test", true}, {"foo*bar.test", "foobar.test", true}, {"foo*bar.test", "foo.bar.test", true}, {"*.x*.test", "a.xx.test", false}} {
		if got := Matches(tc.p, tc.h); got != tc.want {
			t.Fatal(tc, got)
		}
	}
}
func TestDefaultsPrecedeRules(t *testing.T) {
	a, _ := Parse("http://example.test/")
	r := Rule{DomainPattern: "*", IsActive: true, TrustLevel: Verified}
	out, e := EvaluateRules(a, Policy{}, []Rule{r})
	if e != nil || out.IsAllowed || out.MatchedSource == nil || *out.MatchedSource != "default_http_block" {
		t.Fatal(out, e)
	}
	a, _ = Parse("https://example.test:5432/")
	out, _ = EvaluateRules(a, Policy{}, []Rule{r})
	if out.IsAllowed {
		t.Fatal("port bypass")
	}
	a, _ = Parse("https://127.0.0.1/")
	out, _ = EvaluateRules(a, Policy{AllowIPAddresses: true}, []Rule{r})
	if out.IsAllowed {
		t.Fatal("private check coupled to IP flag")
	}
	a, _ = Parse("https://example.test/")
	out, e = EvaluateRules(a, Policy{}, []Rule{r})
	if e != nil || !out.IsAllowed || !out.IsVerified || out.MatchedRule != nil {
		t.Fatal(out, e)
	}
}
func TestExplicitSchemePortAndBoundedRuleWork(t *testing.T) {
	https := "https"
	port := 443
	a, _ := Parse("https://example.test:443/")
	r := Rule{DomainPattern: "*", Protocol: &https, PortRestriction: &port, IsActive: true, TrustLevel: Blocked}
	out, _ := EvaluateRules(a, Policy{}, []Rule{r})
	if !out.IsAllowed {
		t.Fatal("default port represented as explicit restriction")
	}
	rules := make([]Rule, 1000)
	for i := range rules {
		rules[i] = Rule{DomainPattern: strings.Repeat("x", 510) + "*", IsActive: true, TrustLevel: Blocked}
	}
	a.Host = strings.Repeat("a", 200) + ".test"
	if _, e := EvaluateRules(a, Policy{}, rules); e == nil {
		t.Fatal("unbounded glob work")
	}
	if _, e := ValidateRule(Rule{DomainPattern: "*", TrustLevel: 3}); e == nil {
		t.Fatal("unknown enum")
	}
}
