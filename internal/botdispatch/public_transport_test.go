// SPDX-License-Identifier: AGPL-3.0-only
package botdispatch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"solsynth.local/go-migration/ports/sphere/reader"
	"strings"
	"testing"
	"time"
)

type syntheticDNS struct {
	answers []netip.Addr
	calls   int
	err     error
}

func (d *syntheticDNS) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	d.calls++
	return d.answers, d.err
}
func TestPublicBotTransportPinsValidatedAnswersWithoutNetwork(t *testing.T) {
	policy, e := reader.NewPolicy([]string{"https://bot.example.com/"})
	if e != nil {
		t.Fatal(e)
	}
	dns := &syntheticDNS{answers: []netip.Addr{netip.MustParseAddr("8.8.8.8")}}
	calls := 0
	tr := &publicTransport{policy: policy, timeout: time.Second, resolver: dns, exchange: func(r *http.Request, ip netip.Addr) (*http.Response, error) {
		calls++
		if ip.String() != "8.8.8.8" || r.URL.Host != "bot.example.com" {
			t.Fatal("unpinned destination")
		}
		return &http.Response{StatusCode: 202, Body: io.NopCloser(strings.NewReader("accepted"))}, nil
	}}
	request, _ := http.NewRequest("POST", "https://bot.example.com/inbox", strings.NewReader("synthetic"))
	response, e := tr.RoundTrip(request)
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	for _, answers := range [][]netip.Addr{{netip.MustParseAddr("127.0.0.1")}, {netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("10.0.0.1")}, {netip.MustParseAddr("::1")}, nil} {
		dns.answers = answers
		if _, e = tr.RoundTrip(request); e == nil {
			t.Fatal("private/rebound DNS allowed")
		}
	}
	if calls != 1 || dns.calls != 5 {
		t.Fatal(calls, dns.calls)
	}
	for _, target := range []string{"https://other.example.com/inbox", "https://bot.example.com:444/inbox", "http://bot.example.com/inbox", "https://bot.example.com/inbox?secret=x", "https://bot.example.com/inbox#fragment"} {
		r, _ := http.NewRequest("POST", target, nil)
		if _, e = tr.RoundTrip(r); e == nil {
			t.Fatal("invalid target accepted")
		}
	}
	client, e := PublicClient(policy, time.Second)
	if e != nil || client.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("redirect policy", e)
	}
}

type trackedPublicBody struct{ closed bool }

func (*trackedPublicBody) Read([]byte) (int, error) { return 0, io.EOF }
func (b *trackedPublicBody) Close() error {
	b.closed = true
	return errors.New("synthetic private close error")
}

func TestPublicBotTransportRejectsAndClosesOffline(t *testing.T) {
	policy, err := reader.NewPolicy([]string{"https://bot.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		change   func(*http.Request)
		answers  []netip.Addr
		dnsError error
	}{
		{name: "wrong method", change: func(r *http.Request) { r.Method = http.MethodGet }},
		{name: "missing URL", change: func(r *http.Request) { r.URL = nil }},
		{name: "different host header", change: func(r *http.Request) { r.Host = "other.example.com" }},
		{name: "DNS error", dnsError: errors.New("synthetic private DNS details")},
		{name: "empty DNS"},
		{name: "too many answers", answers: make([]netip.Addr, 17)},
		{name: "invalid answer", answers: []netip.Addr{{}}},
		{name: "mixed IPv6", answers: []netip.Addr{netip.MustParseAddr("2606:4700:4700::1111"), netip.MustParseAddr("fd00::1")}},
		{name: "mapped private", answers: []netip.Addr{netip.MustParseAddr("::ffff:127.0.0.1")}},
		{name: "documentation", answers: []netip.Addr{netip.MustParseAddr("203.0.113.1")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &trackedPublicBody{}
			r, err := http.NewRequest(http.MethodPost, "https://bot.example.com/inbox", body)
			if err != nil {
				t.Fatal(err)
			}
			if tc.change != nil {
				tc.change(r)
			}
			tr := &publicTransport{policy: policy, resolver: &syntheticDNS{answers: tc.answers, err: tc.dnsError}, exchange: func(*http.Request, netip.Addr) (*http.Response, error) {
				t.Fatal("rejected destination reached exchange")
				return nil, nil
			}}
			if _, err = tr.RoundTrip(r); err != ErrDelivery || !body.closed {
				t.Fatal("preflight did not redact failure and close body")
			}
		})
	}
	var tr publicTransport
	if _, err := tr.RoundTrip(nil); err != ErrDelivery {
		t.Fatal("nil request accepted")
	}
}

func TestPublicBotTransportFreshPublicAnswerAndRedirectOffline(t *testing.T) {
	policy, err := reader.NewPolicy([]string{"https://bot.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	client, err := PublicClient(policy, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	tr := client.Transport.(*publicTransport)
	dns := &syntheticDNS{answers: []netip.Addr{netip.MustParseAddr("8.8.8.8")}}
	tr.resolver = dns
	var pins []netip.Addr
	tr.exchange = func(r *http.Request, ip netip.Addr) (*http.Response, error) {
		pins = append(pins, ip)
		return &http.Response{StatusCode: http.StatusTemporaryRedirect, Header: http.Header{"Location": {"https://bot.example.com/redirected"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	}
	for _, ip := range []string{"8.8.8.8", "2606:4700:4700::1111"} {
		dns.answers = []netip.Addr{netip.MustParseAddr(ip)}
		request, err := http.NewRequest(http.MethodPost, "https://bot.example.com/inbox", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusTemporaryRedirect {
			t.Fatal("redirect status changed")
		}
	}
	if dns.calls != 2 || len(pins) != 2 || pins[0].String() != "8.8.8.8" || pins[1].String() != "2606:4700:4700::1111" {
		t.Fatal("DNS was reused or redirect followed")
	}
	for _, timeout := range []time.Duration{0, time.Microsecond, 31 * time.Second} {
		if _, err := PublicClient(policy, timeout); err != ErrDelivery {
			t.Fatal("invalid timeout accepted")
		}
	}
	if _, err := PublicClient(reader.Policy{}, time.Second); err != ErrDelivery {
		t.Fatal("unconfigured policy accepted")
	}
}
