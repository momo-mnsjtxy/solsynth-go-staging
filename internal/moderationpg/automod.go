// SPDX-License-Identifier: AGPL-3.0-only
package moderationpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/internal/postpg"
	c "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/posts"
	"strings"
)

// reviewAutomod is an optional owned administration flow. It leaves canonical
// visibility, drafts, manual locks and manual shadowbans unchanged.
func (r *Repository) reviewAutomod(ctx context.Context, actor, id string, request *c.AutomodReview) (any, error) {
	if !r.config.AutomodReview {
		return nil, c.ErrUnsupported
	}
	if request == nil || !authority.ValidID(id) || request.ExpectedRevision < 0 || (request.Action != "dismiss" && request.Action != "recheck") || strings.TrimSpace(request.Reason) == "" || !validReason(&request.Reason) {
		return nil, c.ErrInvalid
	}
	var scope postScope
	tx, e := r.begin(ctx, actor, "posts.moderate", true, func(tx *sql.Tx) ([]string, error) {
		var e error
		scope, e = observePost(ctx, tx, id)
		return []string{ptr(scope.Owner)}, e
	})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if e = lockPosts(ctx, tx, []postScope{scope}); e != nil {
		return nil, e
	}
	if e = r.authorizeAutomodReview(ctx, tx, actor); e != nil {
		return nil, e
	}
	p, e := r.post(ctx, tx, id)
	if e != nil {
		return nil, e
	}
	prior := p.Automod
	revision := int64(0)
	if prior != nil {
		revision = prior.Revision
	}
	s, e := postpg.ReadAutomodSnapshot(ctx, tx, true)
	if e != nil {
		return nil, e
	}
	if e = r.authorizeAutomodReview(ctx, tx, actor); e != nil {
		return nil, e
	}
	d, e := postpg.ComputeAutomod(ctx, s, p.Post, prior, true)
	if e != nil {
		return nil, e
	}
	// Only an exact repeat of a still-current dismissal is idempotent.
	if request.Action == "dismiss" && prior != nil && prior.Review != nil && postpg.SameAutomodInput(prior, d) && prior.Review.BaseRevision == request.ExpectedRevision && prior.Review.ActorID == actor && prior.Review.Reason == request.Reason {
		if e = r.authorizeAutomodReview(ctx, tx, actor); e != nil {
			return nil, e
		}
		if e = tx.Commit(); e != nil {
			return nil, e
		}
		return p, nil
	}
	if revision != request.ExpectedRevision {
		return nil, c.ErrConflict
	}
	if request.Action == "dismiss" {
		if prior == nil || !postpg.SameAutomodInput(prior, d) || len(d.Matches) == 0 {
			return nil, c.ErrConflict
		}
		d.Outcome = "dismissed"
		d.Hidden = false
		d.Penalty = 0
		d.Flagged = false
		d.Review = &posts.AutomodReviewRecord{BaseRevision: revision, ActorID: actor, Reason: request.Reason, At: s.At}
	}
	if e = postpg.SaveAutomodInTransaction(ctx, tx, actor, &p.Post, d, "review_"+request.Action); e != nil {
		return nil, e
	}
	if e = r.audit(ctx, tx, actor, c.PostKind, id, "automod_review", map[string]any{"action": request.Action, "before_revision": revision, "revision": d.Revision, "reason": request.Reason}); e != nil {
		return nil, e
	}
	if e = r.authorizeAutomodReview(ctx, tx, actor); e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return p, nil
}

func (r *Repository) authorizeAutomodReview(ctx context.Context, tx *sql.Tx, actor string) error {
	return r.authorize(ctx, tx, actor, "posts.moderate", true, "automod.rules.manage")
}
