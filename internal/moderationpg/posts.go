// SPDX-License-Identifier: AGPL-3.0-only
package moderationpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/internal/postpg"
	c "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere/posts"
	"strings"
	"unicode/utf8"
)

type postScope struct {
	ID, Publisher                string
	Realm, PublisherRealm, Owner *string
}

func observePost(ctx context.Context, tx *sql.Tx, id string) (postScope, error) {
	var v postScope
	e := tx.QueryRowContext(ctx, `SELECT p.id::text,p.publisher_id::text,p.realm_id::text,u.realm_id::text,u.account_id::text FROM owned_text_posts p JOIN owned_publishers u ON u.id=p.publisher_id WHERE p.id=$1 AND p.deleted_at IS NULL AND u.deleted_at IS NULL`, id).Scan(&v.ID, &v.Publisher, &v.Realm, &v.PublisherRealm, &v.Owner)
	return v, mapped(e)
}
func same(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
func ptr(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
func lockIDs(ctx context.Context, tx *sql.Tx, table string, ids []string) error {
	for _, id := range sorted(ids) {
		var held string
		if e := tx.QueryRowContext(ctx, `SELECT id::text FROM `+table+` WHERE id=$1 FOR UPDATE`, id).Scan(&held); e != nil {
			return mapped(e)
		}
	}
	return nil
}
func lockPosts(ctx context.Context, tx *sql.Tx, scopes []postScope) error {
	realms, pubs, ids := []string{}, []string{}, []string{}
	for _, v := range scopes {
		realms = append(realms, ptr(v.Realm), ptr(v.PublisherRealm))
		pubs = append(pubs, v.Publisher)
		ids = append(ids, v.ID)
	}
	if e := lockIDs(ctx, tx, "owned_realms", realms); e != nil {
		return e
	}
	if e := lockIDs(ctx, tx, "owned_publishers", pubs); e != nil {
		return e
	}
	if e := lockIDs(ctx, tx, "owned_text_posts", ids); e != nil {
		return e
	}
	for _, old := range scopes {
		v, e := observePost(ctx, tx, old.ID)
		if e != nil {
			return e
		}
		if old.Publisher != v.Publisher || !same(old.Realm, v.Realm) || !same(old.PublisherRealm, v.PublisherRealm) || !same(old.Owner, v.Owner) {
			return c.ErrConflict
		}
	}
	return nil
}
func validReason(reason *string) bool {
	return reason == nil || (utf8.ValidString(*reason) && len(*reason) <= 4096 && strings.IndexByte(*reason, 0) < 0)
}
func validatePost(cmd c.Command) error {
	switch cmd.Action {
	case "lock", "unlock", "unshadowban", "delete":
		return nil
	case "shadowban":
		if cmd.Reason != nil && c.ValidReason(*cmd.Reason, false) {
			return nil
		}
	case "visibility":
		if cmd.Visibility != nil && *cmd.Visibility >= 0 && *cmd.Visibility <= 4 {
			return nil
		}
	case "remove_realm":
		if validReason(cmd.Reason) {
			return nil
		}
	}
	return c.ErrInvalid
}
func (r *Repository) mutatePosts(ctx context.Context, actor, ref string, cmd c.Command, global bool) (any, error) {
	if e := validatePost(cmd); e != nil {
		return nil, e
	}
	ids := cmd.IDs
	if ids == nil {
		ids = []string{ref}
	}
	if len(ids) < 1 || len(ids) > 100 || len(sorted(ids)) != len(ids) {
		return nil, c.ErrInvalid
	}
	for _, id := range ids {
		if !authority.ValidID(id) {
			return nil, c.ErrInvalid
		}
	}
	ids = sorted(ids)
	scopes := []postScope{}
	permission := c.PostKind.Permission(cmd.Action)
	tx, e := r.begin(ctx, actor, permission, global, func(tx *sql.Tx) ([]string, error) {
		owners := []string{}
		for _, id := range ids {
			v, e := observePost(ctx, tx, id)
			if e != nil {
				return nil, e
			}
			scopes = append(scopes, v)
			owners = append(owners, ptr(v.Owner))
		}
		return owners, nil
	})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if e = lockPosts(ctx, tx, scopes); e != nil {
		return nil, e
	}
	if !global {
		if len(scopes) != 1 || scopes[0].Realm == nil {
			return nil, c.ErrNotInRealm
		}
		if e = r.social.RequireRealmPermissionInTransaction(ctx, tx, actor, *scopes[0].Realm, social.CanModeratePosts); e != nil {
			if errors.Is(e, social.ErrForbidden) || errors.Is(e, social.ErrNotFound) {
				return nil, c.ErrForbidden
			}
			return nil, e
		}

		if _, e = postpg.ReadWithVisibility(ctx, tx, actor, ref, r.social); e != nil {
			return nil, e
		}
	}
	if e = r.authorize(ctx, tx, actor, permission, global); e != nil {
		return nil, e
	}
	now := r.config.Now().UTC()
	out := []c.Post{}
	for _, id := range ids {
		p, e := r.post(ctx, tx, id)
		if e != nil {
			return nil, e
		}
		old := map[string]any{"visibility": p.Visibility, "realm_id": p.RealmID, "locked": p.LockedAt != nil, "shadowban_reason": p.ShadowbanReason}
		switch cmd.Action {
		case "lock":
			if p.LockedAt != nil {
				return nil, c.ErrAlreadyLocked
			}
			p.LockedAt = &now
		case "unlock":
			if p.LockedAt == nil {
				return nil, c.ErrNotLocked
			}
			p.LockedAt = nil
		case "visibility":
			p.Visibility = *cmd.Visibility
		case "shadowban":
			p.ShadowbanReason = *cmd.Reason
			p.ShadowbannedAt = &now
		case "unshadowban":
			p.ShadowbanReason = "none"
			p.ShadowbannedAt = nil
		case "delete":
			p.DeletedAt = &now
		case "remove_realm":
			if p.RealmID == nil {
				return nil, c.ErrNotInRealm
			}
			_, e = tx.ExecContext(ctx, `INSERT INTO owned_realm_post_moderation(realm_id,post_id,moderator_id,reason,created_at)VALUES($1,$2,$3,$4,$5)ON CONFLICT(realm_id,post_id)DO NOTHING`, *p.RealmID, p.ID, actor, cmd.Reason, now)
			if e != nil {
				return nil, e
			}
			if p.Publisher.AccountID != nil {
				if _, e = tx.ExecContext(ctx, `INSERT INTO owned_content_moderation_notifications(realm_id,post_id,account_id,reason,created_at)VALUES($1,$2,$3,$4,$5)ON CONFLICT(realm_id,post_id)DO NOTHING`, *p.RealmID, p.ID, *p.Publisher.AccountID, cmd.Reason, now); e != nil {
					return nil, e
				}
			}
			p.Visibility = posts.Private
			p.RealmID = nil
		}
		p.UpdatedAt = now
		_, e = tx.ExecContext(ctx, `UPDATE owned_text_posts SET visibility=$2,realm_id=$3,locked_at=$4,shadowban_reason=NULLIF($5::text,'none'),shadowbanned_at=$6,deleted_at=$7,updated_at=$8 WHERE id=$1`, p.ID, p.Visibility, p.RealmID, p.LockedAt, p.ShadowbanReason, p.ShadowbannedAt, p.DeletedAt, now)
		if e != nil {
			return nil, e
		}
		if p.DeletedAt != nil && r.config.PostAssociations != nil {
			if e = r.config.PostAssociations.Apply(ctx, tx, actor, &p.Post, posts.Patch{}, false); e != nil {
				return nil, e
			}
		}
		action := "update"
		if cmd.Action == "delete" {
			action = "delete"
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_text_post_audit(actor_id,post_id,action,occurred_at)VALUES($1,$2,$3,$4)`, actor, p.ID, action, now); e != nil {
			return nil, e
		}
		if e = r.audit(ctx, tx, actor, c.PostKind, p.ID, cmd.Action, map[string]any{"before": old, "visibility": p.Visibility, "realm_id": p.RealmID, "locked": p.LockedAt != nil, "shadowban_reason": p.ShadowbanReason, "reason": cmd.Reason, "scoped": !global}); e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	if cmd.IDs != nil {
		return map[string]any{"success": true, "count": len(out)}, nil
	}
	return out[0], nil
}
func (r *Repository) ModerateRealmPost(ctx context.Context, actor, id string, reason *string) (c.Post, error) {
	v, e := r.mutatePosts(ctx, actor, id, c.Command{Action: "remove_realm", Reason: reason}, false)
	if e != nil {
		return c.Post{}, e
	}
	return v.(c.Post), nil
}
func (r *Repository) Mutate(ctx context.Context, actor string, kind c.Kind, ref string, cmd c.Command) (any, error) {
	switch kind {
	case c.PostKind:
		return r.mutatePosts(ctx, actor, ref, cmd, true)
	case c.PublisherKind:
		return r.mutatePublisher(ctx, actor, ref, cmd)
	case c.RealmKind:
		return r.mutateRealm(ctx, actor, ref, cmd)
	}
	return nil, c.ErrInvalid
}
