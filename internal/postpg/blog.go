// SPDX-License-Identifier: AGPL-3.0-only
package postpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/blogs"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/reader"
	"strings"
	"time"
	"unicode/utf8"
)

type BlogProvider interface {
	PolicyStamp(context.Context, *sql.Tx, string, accounts.Publisher) (string, error)
	Preview(context.Context, string) (reader.Link, error)
	RequireDomain(context.Context, *sql.Tx, accounts.Publisher, string) error
	DomainStamp(context.Context, *sql.Tx, accounts.Publisher, string) (string, error)
}

func (r *Repository) WithBlogs(provider BlogProvider) *Repository { r.blogs = provider; return r }

type blogPreparation struct {
	publisher, url, domainStamp, policyStamp string
	link                                     reader.Link
}

// Initial permission/domain checks precede outbound preview. All SQL locks are
// released first; the final write repeats them and binds the preview to the
// effective publisher and URL, so asynchronous work cannot authorize a write.
func (r *Repository) prepareBlog(ctx context.Context, actor, selector, id, action string, v posts.Patch) (*blogPreparation, error) {
	if action == "create" && (v.Type == nil || *v.Type != posts.Blog) {
		if v.BlogEmbeds {
			return nil, posts.ErrUnsupported
		}
		return nil, nil
	}
	if action == "delete" {
		return nil, nil
	}
	// No opt-in leaves the pre-existing unsupported contract intact.
	if r == nil || r.blogs == nil {
		if v.Type != nil && *v.Type == posts.Blog {
			return nil, posts.ErrUnsupported
		}
		return nil, nil
	}
	var prep *blogPreparation
	permission := "posts.update"
	if action == "create" || action == "publish" {
		permission = "posts.create"
	}
	e := r.write(ctx, actor, permission, func(tx *sql.Tx, now time.Time) error {
		if action != "publish" {
			if e := r.validatePatch(v, action == "create", now); e != nil {
				return e
			}
		}
		var p posts.Post
		var e error
		if action == "create" {
			p.Publisher, e = r.createPublisher(ctx, tx, actor, selector, v)
			p.PublisherID = p.Publisher.ID
		} else {
			p, e = r.locked(ctx, tx, actor, id, v, false, action == "publish")
		}
		if e != nil {
			return e
		}
		if action == "publish" && p.DraftedAt == nil {
			return posts.ErrNotDraft
		}
		apply(&p, v)
		if p.Type != posts.Blog {
			if v.BlogEmbeds {
				return posts.ErrUnsupported
			}
			return nil
		}
		canonical, _, e := blogs.NormalizeURL(p.Content)
		if e != nil {
			return e
		}
		if e = r.authorizeBlog(ctx, tx, actor, p); e != nil {
			return e
		}
		stamp, e := r.blogs.DomainStamp(ctx, tx, p.Publisher, canonical)
		if e != nil {
			return e
		}
		policy, e := r.blogs.PolicyStamp(ctx, tx, actor, p.Publisher)
		if e != nil {
			return e
		}
		prep = &blogPreparation{publisher: p.PublisherID, url: canonical, domainStamp: stamp, policyStamp: policy}
		return nil
	})
	if e != nil {
		return nil, e
	}
	if prep == nil {
		return nil, nil
	}
	link, e := r.blogs.Preview(ctx, prep.url)
	if e != nil {
		if errors.Is(e, blogs.ErrUnavailable) || errors.Is(e, reader.ErrUnavailable) {
			return nil, blogs.ErrUnavailable
		}
		return nil, blogs.ErrPreview
	}
	if e = validateBlogPreview(prep.url, link); e != nil {
		return nil, e
	}
	prep.link = link
	return prep, nil
}
func validateBlogPreview(source string, p reader.Link) error {
	_, host, e := blogs.NormalizeURL(source)
	if e != nil {
		return e
	}
	canonical, target, e := blogs.NormalizeURL(p.URL)
	// Cross-domain canonical URLs/redirects must not claim the verified origin.
	if e != nil || host != target || canonical != p.URL || strings.TrimSpace(p.Title) == "" {
		return blogs.ErrPreview
	}
	for _, f := range []struct {
		s     *string
		limit int
	}{{&p.Title, 1024}, {p.Description, 4096}, {p.SiteName, 1024}, {p.ContentType, 512}, {p.Author, 1024}} {
		if f.s != nil && (!utf8.ValidString(*f.s) || strings.ContainsRune(*f.s, 0) || utf8.RuneCountInString(*f.s) > f.limit) {
			return blogs.ErrPreview
		}
	}
	for _, v := range []*string{p.ImageURL, p.FaviconURL} {
		if v != nil {
			canonical, _, e := blogs.NormalizeURL(*v)
			if e != nil || canonical != *v {
				return blogs.ErrPreview
			}
		}
	}
	return nil
}
func (r *Repository) authorizeBlog(ctx context.Context, tx *sql.Tx, actor string, p posts.Post) error {
	if r.blogs == nil || r.authority == nil {
		return blogs.ErrUnavailable
	}
	if _, e := r.authority.Authorize(ctx, r.db, tx, actor, "posts.create.blog"); e != nil {
		return e
	}
	return r.blogs.RequireDomain(ctx, tx, p.Publisher, p.Content)
}
func (r *Repository) applyBlog(ctx context.Context, tx *sql.Tx, actor string, p *posts.Post, prep *blogPreparation, embeds bool) error {
	if p.Type != posts.Blog {
		if embeds {
			return posts.ErrUnsupported
		}
		p.Meta = nil
		return nil
	}
	if p.DeletedAt != nil {
		return nil
	}
	if r.blogs == nil {
		return posts.ErrUnsupported
	}
	canonical, _, e := blogs.NormalizeURL(p.Content)
	if e != nil {
		return e
	}
	if prep == nil || prep.publisher != p.PublisherID || prep.url != canonical {
		return posts.ErrConflict
	}
	if e = r.authorizeBlog(ctx, tx, actor, *p); e != nil {
		return e
	}
	if e = r.matchBlogPreparation(ctx, tx, actor, *p, prep); e != nil {
		return e
	}
	p.Content = canonical
	if p.Title == nil || strings.TrimSpace(*p.Title) == "" {
		p.Title = optional(&prep.link.Title)
	}
	if p.Description == nil || strings.TrimSpace(*p.Description) == "" {
		p.Description = optional(prep.link.Description)
	}
	p.Meta = posts.BlogMetadata(prep.link)
	return nil
}
func encodeBlog(p posts.Post) (any, error) {
	if p.Meta == nil {
		return nil, nil
	}
	return json.Marshal(p.Meta)
}
func decodeBlog(p *posts.Post, b []byte) error {
	if len(b) == 0 {
		if p.Type == posts.Blog {
			return blogs.ErrUnavailable
		}
		return nil
	}
	if json.Unmarshal(b, &p.Meta) != nil || p.Meta == nil || len(p.Meta.Embeds) != 1 || p.Meta.Embeds[0].Type != "link" {
		return blogs.ErrUnavailable
	}
	return validateBlogPreview(p.Content, p.Meta.Embeds[0].Link)
}
func (r *Repository) CheckBlogPermission(ctx context.Context, actor, selector, raw string) (blogs.Permission, error) {
	var out blogs.Permission
	canonical, domain, e := blogs.NormalizeURL(raw)
	if e != nil {
		return out, e
	}
	if r == nil || r.authority == nil || r.blogs == nil {
		return out, blogs.ErrUnavailable
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	if _, e = r.authority.Authenticate(ctx, r.db, tx, actor); e != nil {
		return out, e
	}
	p, e := r.publishers.ResolveForPost(ctx, tx, actor, selector)
	if e != nil {
		return out, e
	}
	out.Domain, out.PublisherID, out.PublisherName = domain, p.ID, p.Name
	_, e = r.authority.Authorize(ctx, r.db, tx, actor, "posts.create.blog")
	if e != nil && !errors.Is(e, authority.ErrForbidden) {
		return out, e
	}
	out.PermissionGranted = e == nil
	// The node diagnostic records active explicit account/group grants, without
	// superuser fallback. Actual permission also requires scopes and punishments.
	e = tx.QueryRowContext(ctx, `WITH rules AS(SELECT pattern,deny,starts_at,expires_at FROM owned_authority_grants WHERE account_id=$1 UNION ALL SELECT g.pattern,g.deny,g.starts_at,g.expires_at FROM owned_authority_group_grants g JOIN owned_authority_group_members m ON m.group_id=g.group_id WHERE m.account_id=$1) SELECT COALESCE(bool_or(NOT deny),false) AND NOT COALESCE(bool_or(deny),false) FROM rules WHERE(pattern='*' OR pattern='posts.create.blog' OR(pattern LIKE '%.*' AND starts_with('posts.create.blog',left(pattern,length(pattern)-1))))AND(starts_at IS NULL OR starts_at<=clock_timestamp())AND(expires_at IS NULL OR expires_at>clock_timestamp())`, actor).Scan(&out.PermissionNodeGranted)
	if e != nil {
		return out, e
	}
	e = r.blogs.RequireDomain(ctx, tx, p, canonical)
	if e != nil && !errors.Is(e, blogs.ErrUnverified) {
		return out, e
	}
	out.DomainVerified = e == nil
	out.CanCreate = out.PermissionGranted && out.DomainVerified
	if _, e = r.authority.Authenticate(ctx, r.db, tx, actor); e != nil {
		return out, e
	}
	return out, tx.Commit()
}

func (r *Repository) blogScheduleStamp(ctx context.Context, tx *sql.Tx, actor string, p *posts.Post) ([]byte, error) {
	b, e := schedulePolicyStamp(ctx, tx, actor, p.PublisherID, p.ID)
	if e != nil || p.Type != posts.Blog {
		return b, e
	}
	if r.blogs == nil {
		return nil, blogs.ErrUnavailable
	}
	stamp, e := r.blogs.DomainStamp(ctx, tx, p.Publisher, p.Content)
	if e != nil {
		return nil, e
	}
	policy, e := r.blogs.PolicyStamp(ctx, tx, actor, p.Publisher)
	if e != nil {
		return nil, e
	}
	// Let PostgreSQL produce the same canonical jsonb representation retained
	// by owned_post_schedules; Go JSON whitespace/key order must not block an
	// otherwise unchanged schedule. Bind blog-specific policy incarnations too.
	var out []byte
	e = tx.QueryRowContext(ctx, `SELECT $1::jsonb || jsonb_build_object(
 'blog_domain',$2::text,'blog_policy',$4::text,
 'blog_grants',(SELECT jsonb_agg(to_jsonb(g) ORDER BY g.id) FROM owned_authority_grants g WHERE g.account_id=$3 AND g.pattern IN('*','posts.*','posts.create.*','posts.create.blog')),
 'blog_groups',(SELECT jsonb_agg(to_jsonb(g) ORDER BY g.id) FROM owned_authority_group_grants g JOIN owned_authority_group_members m ON m.group_id=g.group_id WHERE m.account_id=$3 AND g.pattern IN('*','posts.*','posts.create.*','posts.create.blog')),
 'blog_punishments',(SELECT jsonb_agg(to_jsonb(p) ORDER BY p.id) FROM owned_authority_punishments p WHERE p.account_id=$3 AND (p.kind='disable_account' OR p.kind='permission_modification' AND p.pattern IN('*','posts.*','posts.create.*','posts.create.blog'))))`, b, stamp, actor, policy).Scan(&out)
	return out, e
}

// Recheck once more after association/schedule waits, immediately before the
// canonical write's final original-proof check and commit.
func (r *Repository) matchBlogPreparation(ctx context.Context, tx *sql.Tx, actor string, p posts.Post, prep *blogPreparation) error {
	if e := r.authorizeBlog(ctx, tx, actor, p); e != nil {
		return e
	}
	if prep == nil || prep.publisher != p.PublisherID {
		return posts.ErrConflict
	}
	stamp, e := r.blogs.DomainStamp(ctx, tx, p.Publisher, p.Content)
	if e != nil {
		return e
	}
	policy, e := r.blogs.PolicyStamp(ctx, tx, actor, p.Publisher)
	if e != nil {
		return e
	}
	if stamp != prep.domainStamp || policy != prep.policyStamp {
		return posts.ErrConflict
	}
	return nil
}
