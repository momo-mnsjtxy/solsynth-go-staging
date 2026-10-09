// SPDX-License-Identifier: AGPL-3.0-only
package fortunes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// All content below is independently authored, explicitly synthetic fixture
// text. It is never compiled into the runtime corpus or used as a fallback.
func syntheticGeneration(label string) Generation {
	return Generation{Report: Report{Version: 2, Poem: "Synthetic poem " + label, Summary: "Synthetic summary " + label, SummaryDetail: "Synthetic details " + label, Wish: "Synthetic wish", Love: "Synthetic love", Study: "Synthetic study", Career: "Synthetic career", Health: "Synthetic health", LostItem: "Synthetic item", LuckyColor: "Synthetic color", LuckyDirection: "Synthetic direction", LuckyTime: "Synthetic time", LuckyItem: "Synthetic object", LuckyAction: "Synthetic action", AvoidAction: "Synthetic avoidance", Ritual: "Synthetic ritual"}, Tips: []Tip{{true, "Synthetic title A", "Synthetic content A"}, {true, "Synthetic title B", "Synthetic content B"}, {false, "Synthetic title C", "Synthetic content C"}, {false, "Synthetic title D", "Synthetic content D"}}}
}
func syntheticRequest() Request {
	return Request{Version: 2, AccountID: "10000000-0000-4000-8000-000000000001", CheckInID: "20000000-0000-4000-8000-000000000001", IdempotencyKey: "fortune:20000000-0000-4000-8000-000000000001:v2", Day: "2026-10-08", LocalDate: "2026-10-08", TimeZone: "UTC", Language: "en", Level: 2}
}
func TestConfiguredCorpusDeterminismAndNoFallback(t *testing.T) {
	c := Corpus{Revision: "synthetic-v1", Label: "Owned synthetic test corpus", Provenance: "Independently authored only for this test"}
	for i := 0; i < 30; i++ {
		c.Reports = append(c.Reports, Template{ID: fmt.Sprintf("fixture-%02d", i), Language: "en", Level: 2, Generation: syntheticGeneration(fmt.Sprint(i))})
	}
	p, e := NewCorpusProvider(c)
	if e != nil {
		t.Fatal(e)
	}
	r := syntheticRequest()
	a, e := p.Generate(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	c.Reports[0].Generation.Report.Poem = "mutated outside"
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		r.AccountID = fmt.Sprintf("10000000-0000-4000-8000-%012d", i+1)
		v, e := p.Generate(context.Background(), r)
		if e != nil {
			t.Fatal(e)
		}
		again, e := p.Generate(context.Background(), r)
		if e != nil || v.Report != again.Report {
			t.Fatal("unstable account/date", e)
		}
		seen[v.Report.Poem] = true
	}
	if len(seen) < 2 {
		t.Fatal("selection lost account isolation")
	}
	r = syntheticRequest()
	b, e := p.Generate(context.Background(), r)
	if e != nil || a.Report != b.Report {
		t.Fatal("mutable corpus", e)
	}
	r.Language = "zz"
	if _, e = p.Generate(context.Background(), r); !errors.Is(e, ErrUnavailable) {
		t.Fatal("fabricated language fallback", e)
	}
	if _, e = NewCorpusProvider(Corpus{}); e == nil {
		t.Fatal("empty corpus accepted")
	}
}

type syntheticTransport func(*http.Request) (*http.Response, error)

func (f syntheticTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestConfiguredHTTPProviderUsesBoundedOwnedContract(t *testing.T) {
	want := syntheticRequest()
	calls := 0
	provider, e := NewHTTPProvider("https://configured-provider.example.test/v2/report", "synthetic-http-v1", &http.Client{Transport: syntheticTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "POST" || r.Header.Get("Idempotency-Key") != want.IdempotencyKey || r.Header.Get("Authorization") != "" {
			t.Fatal(r.Method, r.Header)
		}
		b, _ := io.ReadAll(r.Body)
		var got Request
		if e := Decode(b, &got); e != nil || got != want {
			t.Fatal(got, e)
		}
		b, _ = json.Marshal(syntheticGeneration("transport"))
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(b)))}, nil
	})})
	if e != nil {
		t.Fatal(e)
	}
	v, e := provider.Generate(context.Background(), want)
	if e != nil || v.Report.Version != 2 || calls != 1 {
		t.Fatal(v, e, calls)
	}
	for _, endpoint := range []string{"http://configured-provider.example.test", "https://user:secret@configured-provider.example.test", "https://configured-provider.example.test?token=unwanted"} {
		if _, e = NewHTTPProvider(endpoint, "v1", &http.Client{}); e == nil {
			t.Fatal(endpoint)
		}
	}
}
func TestHTTPProviderRejectsInvalidAndOversizedOutput(t *testing.T) {
	valid, _ := json.Marshal(syntheticGeneration("response"))
	for _, body := range []string{`null`, `{"report":{"version":2,"version":2},"tips":[]}`, strings.Repeat("x", 65537), strings.Replace(string(valid), `"version":2`, `"version":1`, 1), strings.Replace(string(valid), `"poem":`, `"unknown":`, 1)} {
		p, e := NewHTTPProvider("https://configured-provider.example.test/report", "v1", &http.Client{Transport: syntheticTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = p.Generate(context.Background(), syntheticRequest()); !errors.Is(e, ErrProvider) {
			t.Fatal("invalid response accepted", e)
		}
	}
	calls := 0
	p, _ := NewHTTPProvider("https://configured-provider.example.test/report", "v1", &http.Client{Transport: syntheticTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://different.example.test"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})})
	if _, e := p.Generate(context.Background(), syntheticRequest()); !errors.Is(e, ErrProvider) || calls != 1 {
		t.Fatal("redirect followed", e, calls)
	}
}
func TestVersionTwoUTF16LimitsAndTipDuplicates(t *testing.T) {
	g := syntheticGeneration("validation")
	g.Report.Poem = strings.Repeat("🌙", 60)
	if e := ValidateGeneration(&g); e != nil {
		t.Fatal(e)
	}
	g.Report.Poem += "🌙"
	if e := ValidateGeneration(&g); e == nil {
		t.Fatal("UTF16 bound ignored")
	}
	g = syntheticGeneration("validation")
	g.Tips[1].Title = "Synthetic title A!"
	if e := ValidateGeneration(&g); e == nil {
		t.Fatal("equivalent tip duplicate accepted")
	}
	r := syntheticRequest()
	r.IdempotencyKey = "different-account"
	if e := ValidateRequest(r); e == nil {
		t.Fatal("unbound provider request")
	}
}
