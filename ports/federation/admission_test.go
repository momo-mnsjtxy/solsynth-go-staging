// SPDX-License-Identifier: AGPL-3.0-only
package federation

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

func admissionPolicy() PublicAdmissionPolicy {
	return PublicAdmissionPolicy{Origin: "https://remote.example.test", Incarnation: "10000000-0000-4000-8000-000000000001"}
}
func TestFirstContactDocumentBindingAndNoAliases(t *testing.T) {
	c, k := keyFixture(t)
	target, jrd, actor := discoveryDocuments(t, k)
	policy := admissionPolicy()
	mutate := func(b []byte, name string, v any) []byte {
		var m map[string]any
		if json.Unmarshal(b, &m) != nil {
			t.Fatal("fixture")
		}
		m[name] = v
		out, _ := json.Marshal(m)
		return out
	}
	cases := []struct {
		name       string
		jrd, actor []byte
		want       bool
		calls      int
	}{
		{"valid", jrd, actor, true, 2},
		{"webfinger_alias", mutate(jrd, "aliases", []string{target.Actor}), actor, false, 1},
		{"webfinger_subject", mutate(jrd, "subject", "acct:other@remote.example.test"), actor, false, 1},
		{"webfinger_case", mutate(jrd, "SUBJECT", target.Resource), actor, false, 1},
		{"actor_alias", jrd, mutate(actor, "alsoKnownAs", []string{"https://remote.example.test/other"}), false, 2},
		{"actor_move", jrd, mutate(actor, "movedTo", "https://remote.example.test/other"), false, 2},
		{"actor_id", jrd, mutate(actor, "id", "https://remote.example.test/other"), false, 2},
		{"actor_inbox", jrd, mutate(actor, "inbox", "https://internal.example.test/inbox"), false, 2},
		{"actor_case", jrd, mutate(actor, "ID", target.Actor), false, 2},
		{"bad_owner", jrd, mutate(actor, "publicKey", PublicKey{ID: k.remote.KeyID, Owner: "https://remote.example.test/other", PEM: "bad"}), false, 2},
		{"wrong_key_anchor", jrd, []byte(strings.Replace(string(actor), "#main-key", "#rotated", 1)), false, 2},
		{"duplicate", jrd, []byte(strings.Replace(string(actor), `"id":`, `"id":"bad","id":`, 1)), false, 2},
		{"oversize", jrd, []byte(strings.Repeat("x", MaxBody+1)), false, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			fetch := syntheticFetch(func(ctx context.Context, u, a string) (FetchResponse, error) {
				calls++
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("unbounded")
				}
				b := tc.actor
				if u == target.WebFingerURL() {
					b = tc.jrd
				}
				return FetchResponse{200, a, b}, nil
			})
			out, key, retry, e := DiscoverFirstContact(context.Background(), c, policy, target, fetch)
			if (e == nil) != tc.want || retry || calls != tc.calls {
				t.Fatal(out, retry, e, calls)
			}
			if tc.want && (out.Observation != "https_origin_first_contact" || out.Name != "&lt;script&gt;unsafe&lt;/script&gt;" || key.Actor != target.Actor || key.Incarnation != policy.Incarnation) {
				t.Fatal(out, key)
			}
		})
	}
	for _, origin := range []string{"https://local.example.test", "https://remote.example.test:443", "http://remote.example.test", "https://remote.example.test/", "https://remote.example.test.", "https://127.0.0.1"} {
		p := policy
		p.Origin = origin
		if p.Validate(c) == nil {
			t.Fatal("noncanonical policy", origin)
		}
	}
}

type admissionRoundTrip func(*http.Request) (*http.Response, error)

func (f admissionRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type admissionResolver func(context.Context, string) ([]net.IPAddr, error)

func (f admissionResolver) LookupIPAddr(c context.Context, h string) ([]net.IPAddr, error) {
	return f(c, h)
}
func TestFirstContactTransportRejectsRedirectsAndRebinding(t *testing.T) {
	c, k := keyFixture(t)
	target, _, _ := discoveryDocuments(t, k)
	for _, location := range []string{"https://remote.example.test/redirect", "https://other.example.test/actor", "http://169.254.169.254/latest/meta-data", "https://127.0.0.1/", "file:///etc/passwd"} {
		t.Run(location, func(t *testing.T) {
			fetch, e := NewPublicAdmissionFetcher(c, []PublicAdmissionPolicy{admissionPolicy()})
			if e != nil {
				t.Fatal(e)
			}
			lookups, calls := 0, 0
			fetch.resolver = admissionResolver(func(ctx context.Context, h string) ([]net.IPAddr, error) {
				lookups++
				if h != "remote.example.test" {
					t.Fatal(h)
				}
				return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
			})
			fetch.roundTrip = func(tr *http.Transport) http.RoundTripper {
				if tr.Proxy != nil || !tr.DisableCompression || tr.TLSClientConfig.ServerName != "remote.example.test" {
					t.Fatal("unsafe transport")
				}
				return admissionRoundTrip(func(r *http.Request) (*http.Response, error) {
					calls++
					if r.Header.Get("Authorization") != "" || r.Header.Get("Signature") != "" || r.Header.Get("Cookie") != "" {
						t.Fatal("ambient identity")
					}
					return &http.Response{StatusCode: 302, Header: http.Header{"Location": {location}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
				})
			}
			v, e := fetch.Fetch(context.Background(), target.Actor, MediaType)
			if e != nil || v.Status != 302 || lookups != 1 || calls != 1 {
				t.Fatal(v, e, lookups, calls)
			}
		})
	}
	fetch, _ := NewPublicAdmissionFetcher(c, []PublicAdmissionPolicy{admissionPolicy()})
	calls, lookups := 0, 0
	fetch.resolver = admissionResolver(func(context.Context, string) ([]net.IPAddr, error) {
		lookups++
		if lookups == 1 {
			return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
		}
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("169.254.169.254")}}, nil
	})
	fetch.roundTrip = func(*http.Transport) http.RoundTripper {
		return admissionRoundTrip(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {MediaType}}, Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
		})
	}
	if _, e := fetch.Fetch(context.Background(), target.Actor, MediaType); e != nil {
		t.Fatal(e)
	}
	if _, e := fetch.Fetch(context.Background(), target.Actor, MediaType); !errors.Is(e, ErrForbidden) || calls != 1 || lookups != 2 {
		t.Fatal("rebinding accepted", e, calls, lookups)
	}
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "192.168.0.1", "172.16.0.1", "169.254.169.254", "168.63.129.16", "100.100.100.200", "::1", "fe80::1", "fc00::1", "fec0::1", "::ffff:127.0.0.1", "::ffff:168.63.129.16", "2001:db8::1"} {
		fetch.resolver = discoveryResolverFixture{[]net.IPAddr{{IP: net.ParseIP(ip)}}}
		if _, e := fetch.Fetch(context.Background(), target.Actor, MediaType); !errors.Is(e, ErrForbidden) {
			t.Fatal(ip, e)
		}
	}
}
func TestFirstContactTransportBoundsAndURLAllowlist(t *testing.T) {
	c, k := keyFixture(t)
	target, _, _ := discoveryDocuments(t, k)
	for _, test := range []struct {
		name, body string
		headers    http.Header
		length     int64
		want       error
	}{{"valid", "{}", http.Header{"Content-Type": {MediaType}}, 2, nil}, {"large", strings.Repeat("x", MaxBody+1), http.Header{"Content-Type": {MediaType}}, -1, ErrInvalid}, {"declared_large", "", http.Header{"Content-Type": {MediaType}}, MaxBody + 1, ErrInvalid}, {"duplicate_media", "{}", http.Header{"Content-Type": {MediaType, MediaType}}, 2, ErrInvalid}, {"compressed", "{}", http.Header{"Content-Type": {MediaType}, "Content-Encoding": {"gzip"}}, 2, ErrInvalid}} {
		t.Run(test.name, func(t *testing.T) {
			fetch, _ := NewPublicAdmissionFetcher(c, []PublicAdmissionPolicy{admissionPolicy()})
			fetch.resolver = discoveryResolverFixture{[]net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}}
			fetch.roundTrip = func(*http.Transport) http.RoundTripper {
				return admissionRoundTrip(func(r *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: test.headers, ContentLength: test.length, Body: io.NopCloser(strings.NewReader(test.body)), Request: r}, nil
				})
			}
			_, e := fetch.Fetch(context.Background(), target.Actor, MediaType)
			if !errors.Is(e, test.want) {
				t.Fatal(e)
			}
		})
	}
	fetch, _ := NewPublicAdmissionFetcher(c, []PublicAdmissionPolicy{admissionPolicy()})
	fetch.resolver = admissionResolver(func(context.Context, string) ([]net.IPAddr, error) {
		t.Fatal("invalid URL reached resolver")
		return nil, nil
	})
	for _, u := range []string{target.Actor + "?q=x", target.Actor + "#key", "https://remote.example.test:443/actor", "https://remote.example.test/%61ctor", "https://remote.example.test/a/../actor", "https://other.example.test/a", "https://remote.example.test/.well-known/webfinger?resource=acct:x@else.example.test"} {
		if _, e := fetch.Fetch(context.Background(), u, MediaType); !errors.Is(e, ErrForbidden) {
			t.Fatal(u, e)
		}
	}
}

func TestFirstContactRejectsSpecialPurposeDestinations(t *testing.T) {
	c, k := keyFixture(t)
	target, _, _ := discoveryDocuments(t, k)
	fetch, e := NewPublicAdmissionFetcher(c, []PublicAdmissionPolicy{admissionPolicy()})
	if e != nil {
		t.Fatal(e)
	}
	fetch.roundTrip = func(*http.Transport) http.RoundTripper {
		t.Fatal("special-purpose answer reached transport")
		return nil
	}
	// IANA special-purpose IPv4/IPv6 ranges, plus platform virtual IPs. Each
	// mixed answer includes a public first choice to catch first-address-only
	// validation. IPv4-mapped answers receive the same IPv4 classification.
	for _, ip := range []string{
		"0.0.0.1", "10.0.0.1", "100.64.0.1", "127.0.0.1", "169.254.170.2", "172.16.0.1",
		"192.0.0.9", "192.0.2.1", "192.31.196.1", "192.52.193.1", "192.88.99.2", "192.168.0.1", "192.175.48.1",
		"198.18.0.1", "198.51.100.1", "203.0.113.1", "224.0.0.1", "240.0.0.1", "255.255.255.255", "168.63.129.16",
		"::", "::1", "64:ff9b::a9fe:a9fe", "64:ff9b:1::1", "100::1", "100:0:0:1::1", "2001::1",
		"2001:1::1", "2001:1::2", "2001:1::3", "2001:2::1", "2001:3::1", "2001:4:112::1", "2001:10::1", "2001:20::1", "2001:30::1",
		"2001:db8::1", "2002::1", "2620:4f:8000::1", "3fff::1", "5f00::1", "fc00::1", "fe80::1", "fec0::1", "ff02::1",
	} {
		forms := []string{ip}
		if net.ParseIP(ip).To4() != nil {
			forms = append(forms, "::ffff:"+ip)
		}
		for _, form := range forms {
			fetch.resolver = discoveryResolverFixture{[]net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP(form)}}}
			if _, e := fetch.Fetch(context.Background(), target.Actor, MediaType); !errors.Is(e, ErrForbidden) {
				t.Fatal("special-purpose DNS answer accepted", form, e)
			}
		}
	}
	for _, ip := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !publicAddress(net.ParseIP(ip)) {
			t.Fatal("ordinary public address rejected", ip)
		}
	}
}
