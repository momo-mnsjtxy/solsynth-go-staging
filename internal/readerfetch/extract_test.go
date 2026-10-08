// SPDX-License-Identifier: AGPL-3.0-only
package readerfetch

import (
	"solsynth.local/go-migration/ports/sphere/reader"
	"strings"
	"testing"
)

func testPolicy(t *testing.T) reader.Policy {
	t.Helper()
	p, e := reader.NewPolicy([]string{"https://preview.example", "https://images.example"})
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func value(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
func TestExtractAuthoredMetadataPrecedence(t *testing.T) {
	markup := `<!doctype html><html><head><title>Plain title</title><meta name="twitter:title" content="Twitter title"><meta name="og:title" content="Name title must not outrank property"><meta property="og:title" content="Synthetic &amp; authored"><meta property="og:description" content="A synthetic preview."><meta property="og:image" content="/Image.PNG?Case=Kept"><meta property="og:site_name" content="Synthetic site"><meta property="og:type" content="article"><meta name="author" content="Synthetic author"><meta property="article:published_time" content="2026-10-08T12:30:00+02:00"><link rel="canonical" href="/Canonical"><link rel="apple-touch-icon" href="https://images.example/icon.png"></head><body><h1>Heading</h1></body></html>`
	v, e := Extract(testPolicy(t), "https://preview.example/path", []byte(markup))
	if e != nil || v.Title != "Synthetic & authored" || v.URL != "https://preview.example/Canonical" || value(v.Description) != "A synthetic preview." || value(v.ImageURL) != "https://preview.example/Image.PNG?Case=Kept" || value(v.FaviconURL) != "https://images.example/icon.png" || value(v.SiteName) != "Synthetic site" || value(v.Author) != "Synthetic author" || value(v.ContentType) != "article" || v.PublishedDate == nil || v.PublishedDate.Format("2006-01-02T15:04:05Z") != "2026-10-08T10:30:00Z" {
		t.Fatal(v, e)
	}
}
func TestExtractJSONLDAndPlainFallbacks(t *testing.T) {
	markup := `<script type="application/ld+json">invalid</script><script type="application/ld+json">[null,3,{"headline":"LD title","description":"LD description","image":{"url":"/image.png"},"publisher":{"name":"LD site"},"author":[{"name":"LD author"}],"@type":"Article"}]</script><title>Later plain title</title>`
	v, e := Extract(testPolicy(t), "https://preview.example/", []byte(markup))
	if e != nil || v.Title != "LD title" || value(v.Description) != "LD description" || value(v.Author) != "LD author" || value(v.SiteName) != "LD site" || value(v.ImageURL) != "https://preview.example/image.png" {
		t.Fatal(v, e)
	}
	v, e = Extract(testPolicy(t), "https://preview.example/", []byte(`<h1>Heading &amp; words</h1><p>This synthetic paragraph is long enough for a meaningful preview.</p><img width="1" height="1" src="/tiny.png"><img src="/first.png">`))
	if e != nil || v.Title != "Heading & words" || value(v.ImageURL) != "https://preview.example/first.png" || value(v.Description) == "" || value(v.FaviconURL) != "https://preview.example/favicon.ico" {
		t.Fatal(v, e)
	}
}
func TestExtractRejectsUnsafeMetadataAndBounds(t *testing.T) {
	v, e := Extract(testPolicy(t), "https://preview.example/source", []byte(`<base href="https://unconfigured.example/"><link rel="canonical" href="https://images.example/misleading"><meta property="og:image" content="http://127.0.0.1/image"><link rel="icon" href="data:image/png;base64,abc"><title>Safe title</title>`))
	if e != nil || v.URL != "https://preview.example/source" || v.ImageURL != nil || v.FaviconURL != nil {
		t.Fatal(v, e)
	}
	for _, body := range []string{strings.Repeat("x", 65537), strings.Repeat("<span></span>", 26000), strings.Repeat("<div>", 520)} {
		if _, e = Extract(testPolicy(t), "https://preview.example/", []byte(body)); e == nil {
			t.Fatal("oversized or excessive nesting accepted")
		}
	}
}
