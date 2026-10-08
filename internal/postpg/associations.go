// SPDX-License-Identifier: AGPL-3.0-only
package postpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/sphere/postmedia"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/taxonomy"
	"strings"
)

// AssociationStore joins the lifecycle's transaction. Apply is called only after
// canonical actor, publisher editor and post locks; failure rolls back the post,
// links and both audit ledgers. Load must never modify database state.
type AssociationStore interface {
	Apply(context.Context, *sql.Tx, string, *posts.Post, posts.Patch, bool) error
	Load(context.Context, *sql.Tx, string, *posts.Post) error
}

func NewWithAssociations(db *sql.DB, a AssociationStore) *Repository {
	r := New(db)
	return r.WithAssociations(a)
}

// WithAssociations composes the same store with either guarded or direct repositories.
func (r *Repository) WithAssociations(a AssociationStore) *Repository {
	r.associations = a
	return r
}
func (r *Repository) applyAssociations(ctx context.Context, tx *sql.Tx, actor string, p *posts.Post, v posts.Patch, create bool) error {
	// Existing taxonomy/interaction-only adapters must not silently consume
	// newly recognized relation inputs. The relation adapter opts in explicitly.
	if v.RepliedPostID != nil || v.ForwardedPostID != nil || v.ChainedPostID != nil {
		if _, ok := r.associations.(interface{ SupportsPostRelations() }); !ok {
			return posts.ErrUnsupported
		}
	}
	if v.Attachments != nil && len(*v.Attachments) > 0 {
		supported, ok := r.associations.(interface{ SupportsPostAttachments() bool })
		if !ok || !supported.SupportsPostAttachments() {
			return posts.ErrUnsupported
		}
	}
	if r.associations != nil {
		if e := r.associations.Apply(ctx, tx, actor, p, v, create); e != nil {
			return e
		}
		if e := r.associations.Load(ctx, tx, actor, p); e != nil {
			return e
		}
		return requireContent(p)
	}
	for _, s := range []*[]string{v.Tags, v.Categories, v.CollectionIDs} {
		if s != nil && len(*s) > 0 {
			return posts.ErrUnsupported
		}
	}
	EmptyAssociations(p)
	return requireContent(p)
}
func requireContent(p *posts.Post) error {
	if p.DeletedAt == nil && strings.TrimSpace(p.Content) == "" && len(p.Attachments) == 0 {
		return posts.ErrInvalid
	}
	return nil
}
func EmptyAssociations(p *posts.Post) {
	p.Attachments = []postmedia.Attachment{}
	p.ChainedPosts = []posts.Post{}
	p.Tags = []taxonomy.TagProjection{}
	p.Categories = []taxonomy.CategoryProjection{}
	p.PublisherCollections = []posts.Collection{}
	p.ReactionsCount = map[string]int64{}
	p.ReactionsMade = map[string]bool{}
}

// Fixed SQL fragments and scanner for composed repository queries. They retain
// the canonical active publisher/account filters and use $1 for actor identity.
// Callers must validate identity and keep count and projection in one snapshot.
const ProjectionColumns = columns
const ActivePostFrom = base
const EditorPredicate = editor
const ListedPredicate = `u.type=0 AND p.drafted_at IS NULL AND p.published_at<=CURRENT_TIMESTAMP AND (p.visibility=0 OR ` + editor + `)`

func ScanProjection(s interface{ Scan(...any) error }) (posts.Post, error) {
	p, e := scan(s)
	EmptyAssociations(&p)
	return p, e
}

// ReadInTransaction enforces the same detail privacy predicate as Get. The
// caller owns a snapshot; writes must lock publisher then post before calling.
func ReadInTransaction(ctx context.Context, tx *sql.Tx, actor, id string) (posts.Post, error) {
	return ReadWithVisibility(ctx, tx, actor, id, nil)
}
