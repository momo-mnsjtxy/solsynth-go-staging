// SPDX-License-Identifier: AGPL-3.0-only
package postpg

import (
	"context"
	"errors"
	"solsynth.local/go-migration/ports/sphere/blogs"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/reader"
	"strings"
	"testing"
)

func TestBlogMetadataRejectsCrossDomainAndOversizedPreview(t *testing.T) {
	good := reader.Link{URL: "https://blog.example/article", Title: "Synthetic title"}
	if e := validateBlogPreview(good.URL, good); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []reader.Link{{URL: "https://other.example/article", Title: "title"}, {URL: good.URL, Title: strings.Repeat("x", 1025)}, {URL: good.URL, Title: ""}, {URL: good.URL, Title: "title", ImageURL: func() *string { s := "http://localhost/private"; return &s }()}} {
		if e := validateBlogPreview(good.URL, bad); e == nil {
			t.Fatal(bad)
		}
	}
	p := posts.Post{Type: posts.Blog, Content: good.URL, Meta: posts.BlogMetadata(good)}
	b, e := encodeBlog(p)
	if e != nil {
		t.Fatal(e)
	}
	var out posts.Post
	out.Content = good.URL
	if e = decodeBlog(&out, b.([]byte)); e != nil || out.Meta.Embeds[0].Title != "Synthetic title" {
		t.Fatal(out, e)
	}
}

func TestBlogMetadataRequiresPersistedProjectionAndEffectiveType(t *testing.T) {
	p := posts.Post{Type: posts.Blog, Content: "https://blog.example/article"}
	if e := decodeBlog(&p, nil); !errors.Is(e, blogs.ErrUnavailable) {
		t.Fatal("missing blog metadata admitted", e)
	}
	r := &Repository{}
	p.Type = posts.Article
	if e := r.applyBlog(context.Background(), nil, "", &p, nil, true); !errors.Is(e, posts.ErrUnsupported) {
		t.Fatal("non-blog consumed embeds", e)
	}
}
