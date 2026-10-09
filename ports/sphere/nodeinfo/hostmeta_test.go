// SPDX-License-Identifier: AGPL-3.0-only
package nodeinfo

import (
	"encoding/xml"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHostMetaConfigurationAndHTTP(t *testing.T) {
	for _, domain := range []*string{nil, new(string)} {
		value := "localhost"
		if domain != nil {
			value = *domain
		}
		want := "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<XRD xmlns=\"http://docs.oasis-open.org/ns/xri/xrd-1.0\">\n  <Link rel=\"lrdd\" template=\"https://" + value + "/.well-known/webfinger?resource={uri}\"/>\n</XRD>"
		h := NewSyntheticCompatibilityHandler(nil, domain)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/.well-known/host-meta", nil))
		if w.Code != 200 || w.Body.String() != want || w.Header().Get("Content-Type") != "application/xrd+xml; charset=utf-8" {
			t.Fatal(w.Code, w.Header(), w.Body.String())
		}
	}
	domain := "synthetic.invalid"
	h := NewSyntheticCompatibilityHandler(nil, &domain)
	domain = "changed.invalid"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/.well-known/host-meta", nil))
	if !strings.Contains(w.Body.String(), "https://synthetic.invalid/") {
		t.Fatal("configuration was not copied")
	}
	for _, handler := range []struct {
		candidate bool
		status    int
	}{{false, 503}, {true, 403}} {
		h := NewHandler(nil, nil)
		if handler.candidate {
			h = NewSyntheticCompatibilityHandler(nil, nil)
		}
		r := httptest.NewRequest("GET", "/.well-known/host-meta", nil)
		r.Header.Set("Authorization", "Bearer fixture")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != handler.status {
			t.Fatal(w.Code)
		}
	}
}

func TestHostMetaEscapesXMLAttribute(t *testing.T) {
	domain := `fixture.invalid/?value="a&b"`
	data := HostMeta(&domain)
	var doc struct {
		Link struct {
			Template string `xml:"template,attr"`
		} `xml:"Link"`
	}
	if err := xml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Link.Template != "https://"+domain+"/.well-known/webfinger?resource={uri}" {
		t.Fatal(doc.Link.Template)
	}
}
