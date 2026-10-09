// SPDX-License-Identifier: AGPL-3.0-only
package federation

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

type syntheticFetch func(context.Context, string, string) (FetchResponse, error)

func (fn syntheticFetch) Fetch(c context.Context, u, a string) (FetchResponse, error) {
	return fn(c, u, a)
}
func discoveryDocuments(t *testing.T, k testKeys) (DiscoveryTarget, []byte, []byte) {
	t.Helper()
	target := DiscoveryTarget{Resource: "acct:alice@remote.example.test", Actor: k.remote.Actor}
	jrd, _ := json.Marshal(map[string]any{"subject": target.Resource, "links": []any{map[string]any{"rel": "self", "type": MediaType, "href": target.Actor}}})
	pub, e := PEM(k.key)
	if e != nil {
		t.Fatal(e)
	}
	actor, _ := json.Marshal(map[string]any{"id": target.Actor, "type": "Person", "preferredUsername": "alice", "name": "<script>unsafe</script>", "summary": "<b>bio</b>", "inbox": k.remote.Inbox, "publicKey": PublicKey{ID: k.remote.KeyID, Owner: k.remote.Actor, PEM: pub}})
	return target, jrd, actor
}
func TestDiscoveryPinnedTwoDocumentObservation(t *testing.T) {
	c, k := keyFixture(t)
	target, jrd, actor := discoveryDocuments(t, k)
	calls := 0
	fetch := syntheticFetch(func(ctx context.Context, uri, accept string) (FetchResponse, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded fetch")
		}
		calls++
		if calls == 1 && uri == target.WebFingerURL() && accept == "application/jrd+json" {
			return FetchResponse{200, accept, jrd}, nil
		}
		if calls == 2 && uri == target.Actor && accept == MediaType {
			return FetchResponse{200, accept, actor}, nil
		}
		t.Fatal("unconfigured fetch", uri, accept)
		return FetchResponse{}, ErrInvalid
	})
	out, retry, e := Discover(context.Background(), c, target, k.remote, fetch)
	if e != nil || retry || calls != 2 || out.ID != target.Actor || out.Name != "&lt;script&gt;unsafe&lt;/script&gt;" || out.Observation != "unsigned_document_matches_configured_pin" {
		t.Fatal(out, retry, e, calls)
	}
	var mapped map[string]any
	raw, _ := json.Marshal(out)
	_ = json.Unmarshal(raw, &mapped)
	if _, ok := mapped["publicKey"]; ok {
		t.Fatal("observation exported a key authority")
	}
	original, _ := target.Binding(c, k.remote)
	copy := k.remote.Snapshot()
	copy.PublicKey.N.SetInt64(7)
	later, _ := target.Binding(c, k.remote)
	if original != later {
		t.Fatal("pin snapshot mutated")
	}
}
func TestDiscoveryRejectsIdentityDriftAndAmbiguousDocuments(t *testing.T) {
	c, k := keyFixture(t)
	target, jrd, actor := discoveryDocuments(t, k)
	cases := []struct {
		name       string
		jrd, actor []byte
		maxCalls   int
	}{
		{"subject", []byte(strings.Replace(string(jrd), target.Resource, "acct:other@remote.example.test", 1)), actor, 1},
		{"cross_origin_link", []byte(strings.Replace(string(jrd), target.Actor, "https://evil.example.test/actor", 1)), actor, 1},
		{"duplicate_self", []byte(`{"subject":"` + target.Resource + `","links":[{"rel":"self","type":"` + MediaType + `","href":"` + target.Actor + `"},{"rel":"self","type":"` + MediaType + `","href":"` + target.Actor + `"}]}`), actor, 1},
		{"id", jrd, []byte(strings.Replace(string(actor), `"id":"`+target.Actor+`"`, `"id":"https://remote.example.test/other"`, 1)), 2},
		{"inbox", jrd, []byte(strings.Replace(string(actor), k.remote.Inbox, "https://remote.example.test/other-inbox", 1)), 2},
		{"key_id", jrd, []byte(strings.Replace(string(actor), k.remote.KeyID, k.remote.Actor+"#other", 1)), 2},
		{"key_owner", jrd, []byte(strings.Replace(string(actor), `"owner":"`+target.Actor+`"`, `"owner":"https://remote.example.test/other"`, 1)), 2},
		{"case_alias", jrd, []byte(strings.Replace(string(actor), `"id":`, `"ID":`, 1)), 2},
		{"duplicate", jrd, []byte(strings.Replace(string(actor), `"id":`, `"id":"bad","id":`, 1)), 2},
		{"username", jrd, []byte(strings.Replace(string(actor), `"alice"`, `"other"`, 1)), 2},
		{"huge_name", jrd, []byte(strings.Replace(string(actor), `"name":`, `"name":"`+strings.Repeat("x", 257)+`","ignored":`, 1)), 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			fetch := syntheticFetch(func(_ context.Context, uri, accept string) (FetchResponse, error) {
				calls++
				if uri == target.WebFingerURL() {
					return FetchResponse{200, accept, tc.jrd}, nil
				}
				return FetchResponse{200, accept, tc.actor}, nil
			})
			out, retry, e := Discover(context.Background(), c, target, k.remote, fetch)
			if e == nil || retry || out.ID != "" || calls != tc.maxCalls {
				t.Fatal("accepted drift", out, retry, e, calls)
			}
		})
	}
}
func TestDiscoveryHTTPStatusBodyAndScope(t *testing.T) {
	c, k := keyFixture(t)
	target, _, _ := discoveryDocuments(t, k)
	for _, tc := range []struct {
		status int
		retry  bool
	}{{301, false}, {302, false}, {401, false}, {404, false}, {408, true}, {429, true}, {500, true}, {503, true}, {600, false}} {
		_, retry, e := Discover(context.Background(), c, target, k.remote, syntheticFetch(func(context.Context, string, string) (FetchResponse, error) {
			return FetchResponse{Status: tc.status}, nil
		}))
		if e == nil || retry != tc.retry {
			t.Fatal(tc, retry, e)
		}
	}
	for _, response := range []FetchResponse{{200, "text/html", []byte(`{}`)}, {200, "application/jrd+json", []byte(strings.Repeat(" ", MaxBody+1))}, {200, "application/jrd+json", []byte(`null`)}, {200, "application/jrd+json", []byte(`{"subject":null}`)}} {
		_, retry, e := Discover(context.Background(), c, target, k.remote, syntheticFetch(func(context.Context, string, string) (FetchResponse, error) { return response, nil }))
		if e == nil || retry {
			t.Fatal(response.ContentType, retry, e)
		}
	}
	for _, err := range []error{ErrForbidden, ErrInvalid} {
		_, retry, e := Discover(context.Background(), c, target, k.remote, syntheticFetch(func(context.Context, string, string) (FetchResponse, error) { return FetchResponse{}, err }))
		if e == nil || retry {
			t.Fatal(err, retry, e)
		}
	}
	fetch, e := NewHTTPFetcher(c, []DiscoveryTarget{target})
	if e != nil {
		t.Fatal(e)
	}
	for _, uri := range []string{"http://remote.example.test/actor", "https://127.0.0.1/actor", target.Actor + "?redirect=1", target.WebFingerURL() + "&resource=other", "https://evil.example.test/actor"} {
		if _, e = fetch.Fetch(context.Background(), uri, MediaType); !errors.Is(e, ErrForbidden) {
			t.Fatal("request reached network", uri, e)
		}
	}
	for _, bad := range []DiscoveryTarget{{"acct:alice@evil.example.test", target.Actor}, {"acct:alice@remote.example.test", target.Actor + "#fragment"}, {"acct:alice@remote.example.test", "https://127.0.0.1/actor"}} {
		if bad.Validate(c) == nil {
			t.Fatal("invalid target", bad)
		}
	}
}
func TestServerActorPublicKeyProjection(t *testing.T) {
	c, k := keyFixture(t)
	pub, e := PEM(k.key)
	if e != nil {
		t.Fatal(e)
	}
	cfg := ServerConfig{Username: "synthetic-server", Name: "<b>server</b>", Summary: "<script/>", PublicKeyPEM: pub}
	actor, e := cfg.Actor(c)
	if e != nil || actor.Type != "Application" || actor.ID != c.Origin+"/activitypub/actor" || actor.PublicKey.Owner != actor.ID || !actor.InstanceActor || actor.Name != "&lt;b&gt;server&lt;/b&gt;" {
		t.Fatal(actor, e)
	}
	for _, invalid := range []string{pub + pub, "not a key", strings.Replace(pub, "PUBLIC KEY", "PRIVATE KEY", -1)} {
		cfg.PublicKeyPEM = invalid
		if _, e = cfg.Actor(c); e == nil {
			t.Fatal("invalid server key")
		}
	}
}

func TestDiscoveryBindingIncludesOriginalExecutionPolicy(t *testing.T) {
	c, k := keyFixture(t)
	target, _, _ := discoveryDocuments(t, k)
	original, e := target.Binding(c, k.remote)
	if e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(*Config){func(c *Config) { c.MaxAttempts++ }, func(c *Config) { c.DeliveryTimeout += time.Second }, func(c *Config) { c.Origin = "https://changed.example.test" }, func(c *Config) { c.AllowedOrigins = append(c.AllowedOrigins, "https://added.example.test") }} {
		changed := c
		changed.AllowedOrigins = append([]string{}, c.AllowedOrigins...)
		change(&changed)
		binding, e := target.Binding(changed, k.remote)
		if e != nil || binding == original {
			t.Fatal("policy extension retained old binding", binding, e)
		}
	}
}

type discoveryResolverFixture struct{ addresses []net.IPAddr }

func (r discoveryResolverFixture) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return r.addresses, nil
}
func TestDiscoveryFetcherRejectsNonPublicDNSBeforeDial(t *testing.T) {
	c, k := keyFixture(t)
	target, _, _ := discoveryDocuments(t, k)
	for _, address := range []string{"127.0.0.1", "10.1.2.3", "169.254.169.254", "100.64.0.1", "192.0.2.1", "::1", "fc00::1", "fec0::1", "4000::1", "3fff::1"} {
		fetch, e := NewHTTPFetcher(c, []DiscoveryTarget{target})
		if e != nil {
			t.Fatal(e)
		}
		fetch.resolver = discoveryResolverFixture{[]net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP(address)}}}
		if _, e = fetch.Fetch(context.Background(), target.Actor, MediaType); !errors.Is(e, ErrForbidden) {
			t.Fatal("non-public mixed answer reached dial", address, e)
		}
	}
	for _, address := range []string{"8.8.8.8", "2606:4700:4700::1111"} {
		if !publicAddress(net.ParseIP(address)) {
			t.Fatal("public fixture denied", address)
		}
	}
}
