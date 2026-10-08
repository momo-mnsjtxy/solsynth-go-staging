// SPDX-License-Identifier: AGPL-3.0-only
package readerfetch

import (
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
	"solsynth.local/go-migration/ports/sphere/reader"
)

func clean(s string, limit int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && !unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > limit {
		s = string(r[:limit])
	}
	return s
}
func optional(s string, limit int) *string {
	s = clean(s, limit)
	if s == "" {
		return nil
	}
	return &s
}
func first(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
func attribute(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
func nodeText(n *html.Node, limit int) string {
	var out strings.Builder
	stack := []*html.Node{n}
	for len(stack) > 0 && out.Len() < limit {
		x := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if x.Type == html.TextNode {
			out.WriteString(x.Data)
			out.WriteByte(' ')
		}
		if x.Type == html.ElementNode && (x.Data == "script" || x.Data == "style" || x.Data == "template" || x.Data == "noscript") {
			continue
		}
		for child := x.LastChild; child != nil; child = child.PrevSibling {
			stack = append(stack, child)
		}
	}
	return clean(out.String(), limit)
}
func safeURL(p reader.Policy, base *url.URL, raw string) *string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	ref, e := url.Parse(strings.TrimSpace(raw))
	if e != nil {
		return nil
	}
	resolved, e := p.Normalize(base.ResolveReference(ref).String())
	if e != nil {
		return nil
	}
	return &resolved
}

// Extract returns bounded metadata only. Scripts, raw article HTML, resources,
// refresh directives and <base> elements never execute or trigger requests.
func Extract(policy reader.Policy, raw string, body []byte) (reader.Link, error) {
	canonical, e := policy.Normalize(raw)
	if e != nil {
		return reader.Link{}, e
	}
	if len(body) > 2<<20 || !utf8.Valid(body) {
		return reader.Link{}, reader.ErrLimit
	}
	z := html.NewTokenizer(bytes.NewReader(body))
	z.SetMaxBuf(65536)
	for count := 0; ; count++ {
		if count > 50000 {
			return reader.Link{}, reader.ErrLimit
		}
		if z.Next() == html.ErrorToken {
			if z.Err() != io.EOF {
				return reader.Link{}, reader.ErrLimit
			}
			break
		}
	}
	doc, e := html.Parse(bytes.NewReader(body))
	if e != nil {
		return reader.Link{}, reader.ErrFetch
	}
	base, _ := url.Parse(canonical)
	meta := map[string]string{}
	named := map[string]string{}
	var title, h1, paragraph, image, canonicalLink, appleIcon, icon string
	ld := map[string]string{}
	stack := []*html.Node{doc}
	scripts := 0
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for child := n.LastChild; child != nil; child = child.PrevSibling {
			stack = append(stack, child)
		}
		if n.Type != html.ElementNode {
			continue
		}
		switch n.Data {
		case "meta":
			content := clean(attribute(n, "content"), 4096)
			property, name := strings.ToLower(attribute(n, "property")), strings.ToLower(attribute(n, "name"))
			if property != "" && meta[property] == "" {
				meta[property] = content
			}
			if name != "" && named[name] == "" {
				named[name] = content
			}
		case "title":
			if title == "" {
				title = nodeText(n, 512)
			}
		case "h1":
			if h1 == "" {
				h1 = nodeText(n, 512)
			}
		case "p":
			if paragraph == "" {
				text := nodeText(n, 501)
				length := utf8.RuneCountInString(text)
				if length > 30 && length < 500 {
					paragraph = clean(text, 300)
				}
			}
		case "img":
			if image != "" {
				continue
			}
			w, ew := strconv.Atoi(attribute(n, "width"))
			h, eh := strconv.Atoi(attribute(n, "height"))
			if ew == nil && eh == nil && w < 50 && h < 50 {
				continue
			}
			if v := safeURL(policy, base, attribute(n, "src")); v != nil {
				image = *v
			}
		case "link":
			rel := " " + strings.ToLower(strings.Join(strings.Fields(attribute(n, "rel")), " ")) + " "
			href := attribute(n, "href")
			if strings.Contains(rel, " canonical ") && canonicalLink == "" {
				canonicalLink = href
			}
			if strings.Contains(rel, " apple-touch-icon ") && appleIcon == "" {
				appleIcon = href
			}
			if strings.Contains(rel, " icon ") && icon == "" {
				icon = href
			}
		case "script":
			if strings.EqualFold(attribute(n, "type"), "application/ld+json") && scripts < 8 {
				scripts++
				if n.FirstChild != nil && n.FirstChild.Type == html.TextNode {
					extractLD([]byte(n.FirstChild.Data), ld)
				}
			}
		}
	}
	for key, value := range named {
		if meta[key] == "" {
			meta[key] = value
		}
	}
	link := reader.Link{URL: canonical, Title: clean(first(meta["og:title"], meta["twitter:title"], ld["title"], meta["title"], title, h1, base.Hostname()), 512)}
	if v := safeURL(policy, base, canonicalLink); v != nil {
		u, _ := url.Parse(*v)
		if u.Host == base.Host {
			link.URL = *v
		}
	}
	link.Description = optional(first(meta["og:description"], meta["twitter:description"], ld["description"], meta["description"], paragraph), 2048)
	link.SiteName = optional(first(meta["og:site_name"], ld["site"], named["application-name"], base.Hostname()), 256)
	link.ContentType = optional(first(meta["og:type"], ld["type"]), 128)
	link.Author = optional(first(meta["author"], meta["article:author"], ld["author"]), 256)
	link.ImageURL = safeURL(policy, base, first(meta["og:image"], meta["twitter:image"], ld["image"], image))
	link.FaviconURL = safeURL(policy, base, first(appleIcon, icon, "/favicon.ico"))
	date := first(meta["article:published_time"], meta["datepublished"], meta["pubdate"])
	for _, layout := range []string{time.RFC3339Nano, time.RFC1123Z, time.RFC1123, "2006-01-02"} {
		if parsed, e := time.Parse(layout, date); e == nil {
			parsed = parsed.UTC()
			link.PublishedDate = &parsed
			break
		}
	}
	return link, nil
}
func stringField(m map[string]any, key string) string { s, _ := m[key].(string); return s }
func objectName(v any) string {
	switch x := v.(type) {
	case map[string]any:
		return stringField(x, "name")
	case []any:
		if len(x) > 0 {
			if m, ok := x[0].(map[string]any); ok {
				return stringField(m, "name")
			}
		}
	}
	return ""
}
func ldImage(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case map[string]any:
		return stringField(x, "url")
	case []any:
		if len(x) > 0 {
			s, _ := x[0].(string)
			return s
		}
	}
	return ""
}
func extractLD(data []byte, out map[string]string) {
	if len(data) > 65536 {
		return
	}
	var root any
	if json.Unmarshal(data, &root) != nil {
		return
	}
	entries, ok := root.([]any)
	if !ok {
		entries = []any{root}
	}
	for i, entry := range entries {
		if i >= 64 {
			break
		}
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		fields := map[string]string{"title": first(stringField(m, "headline"), stringField(m, "name")), "description": stringField(m, "description"), "type": stringField(m, "@type"), "site": objectName(m["publisher"]), "author": objectName(m["author"]), "image": ldImage(m["image"])}
		for k, v := range fields {
			if out[k] == "" {
				out[k] = clean(v, 4096)
			}
		}
	}
}
