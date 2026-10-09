// SPDX-License-Identifier: AGPL-3.0-only
package accountpg

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
)

func (r *Repository) PublisherQuota(ctx context.Context, actor string) (out accounts.PublisherQuota, err error) {
	if !authority.ValidID(actor) {
		return out, accounts.ErrForbidden
	}
	err = r.publisherRead(ctx, actor, func(tx *sql.Tx) error {
		if e := tx.QueryRowContext(ctx, `SELECT total,starts_at,expires_at FROM owned_publisher_quota WHERE account_id=$1 AND starts_at<=statement_timestamp() AND(expires_at IS NULL OR expires_at>statement_timestamp())`, actor).Scan(&out.Total, &out.StartsAt, &out.ExpiresAt); e != nil {
			if errors.Is(e, sql.ErrNoRows) {
				return accounts.ErrPolicy
			}
			return e
		}
		if e := tx.QueryRowContext(ctx, publisherQuotaUsed, actor).Scan(&out.Used); e != nil {
			return e
		}
		out.Remaining = out.Total - out.Used
		if out.Remaining < 0 {
			out.Remaining = 0
		}
		return nil
	})
	return
}

func (r *Repository) SearchPublishers(ctx context.Context, viewer, query string, take int) (out []accounts.Publisher, err error) {
	query = strings.TrimSpace(query)
	if query == "" || utf8.RuneCountInString(query) > 256 || take < 1 || take > 100 {
		return nil, accounts.ErrInvalid
	}
	out = []accounts.Publisher{}
	err = r.publisherRead(ctx, viewer, func(tx *sql.Tx) error {
		rows, e := tx.QueryContext(ctx, `SELECT `+publisherColumns+` FROM owned_publishers p LEFT JOIN owned_realms realm ON realm.id=p.realm_id WHERE p.deleted_at IS NULL AND strpos(lower(concat_ws(' ',p.name,p.nick,p.bio)),lower($2))>0 AND `+ActivePublisherOwnerPredicate("p")+` AND `+r.publisherVisible("p", "$1")+` AND `+PublisherRelationshipPredicate("p", "$1", true)+` ORDER BY CASE WHEN lower(p.name)=lower($2) THEN 0 ELSE 1 END,p.name,p.id LIMIT $3`, viewer, query, take)
		if e != nil {
			return e
		}
		for rows.Next() {
			p, e := scanPublisher(rows)
			if e != nil {
				return e
			}
			out = append(out, p)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for i := range out {
			if e = hydrate(ctx, tx, &out[i]); e != nil {
				return e
			}
		}
		return nil
	})
	return
}

func (r *Repository) PublisherHeatmap(ctx context.Context, viewer, ref string) (out []accounts.HeatmapDay, err error) {
	out = []accounts.HeatmapDay{}
	err = r.publisherRead(ctx, viewer, func(tx *sql.Tx) error {
		p, e := r.visiblePublisher(ctx, tx, viewer, ref)
		if e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, `SELECT to_char(date_trunc('day',published_at AT TIME ZONE 'UTC'),'YYYY-MM-DD'),count(*) FROM owned_text_posts WHERE publisher_id=$1 AND deleted_at IS NULL AND drafted_at IS NULL AND published_at<=statement_timestamp() AND shadowbanned_at IS NULL AND visibility=0 GROUP BY 1 ORDER BY 1 DESC LIMIT 366`, p.ID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v accounts.HeatmapDay
			if e = rows.Scan(&v.Day, &v.Posts); e != nil {
				return e
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return
}

func (r *Repository) PublisherStats(ctx context.Context, viewer, ref string) (out accounts.PublisherStats, err error) {
	err = r.publisherRead(ctx, viewer, func(tx *sql.Tx) error {
		p, e := r.visiblePublisher(ctx, tx, viewer, ref)
		if e != nil {
			return e
		}
		return tx.QueryRowContext(ctx, `SELECT
		 (SELECT count(*) FROM owned_text_posts q WHERE q.publisher_id=$1 AND q.deleted_at IS NULL AND q.drafted_at IS NULL AND q.published_at<=statement_timestamp() AND q.shadowbanned_at IS NULL AND q.visibility=0),
		 (SELECT count(*) FROM owned_publisher_follows f WHERE f.publisher_id=$1 AND f.state='accepted' AND f.ended_at IS NULL),
		 (SELECT count(*) FROM owned_publisher_members m WHERE m.publisher_id=$1 AND m.joined_at IS NOT NULL AND m.revoked_at IS NULL),
		 (SELECT count(*) FROM owned_post_collections c WHERE c.publisher_id=$1 AND c.deleted_at IS NULL)`, p.ID).Scan(&out.Posts, &out.Followers, &out.Members, &out.Collections)
	})
	return
}

func (r *Repository) PublisherContentStats(ctx context.Context, viewer, ref string) (out accounts.PublisherContentStats, err error) {
	err = r.publisherRead(ctx, viewer, func(tx *sql.Tx) error {
		p, e := r.visiblePublisher(ctx, tx, viewer, ref)
		if e != nil {
			return e
		}
		return tx.QueryRowContext(ctx, `SELECT
		 count(*) FILTER(WHERE p.type=0),count(*) FILTER(WHERE p.type=1),count(*) FILTER(WHERE p.type=2),
		 (SELECT count(*) FROM owned_post_reactions r JOIN owned_text_posts rp ON rp.id=r.post_id WHERE rp.publisher_id=$1 AND rp.deleted_at IS NULL AND rp.drafted_at IS NULL AND rp.published_at<=statement_timestamp() AND rp.shadowbanned_at IS NULL AND rp.visibility=0)
		 FROM owned_text_posts p WHERE p.publisher_id=$1 AND p.deleted_at IS NULL AND p.drafted_at IS NULL AND p.published_at<=statement_timestamp() AND p.shadowbanned_at IS NULL AND p.visibility=0`, p.ID).Scan(&out.Moments, &out.Articles, &out.Blogs, &out.Reactions)
	})
	return
}

func (r *Repository) PublisherFeatures(ctx context.Context, actor, ref string) (out map[string]bool, err error) {
	out = map[string]bool{"followRequiresApproval": false, "postsRequireFollow": false}
	if !authority.ValidID(actor) {
		return nil, accounts.ErrForbidden
	}
	err = r.publisherRead(ctx, actor, func(tx *sql.Tx) error {
		p, e := r.visiblePublisher(ctx, tx, actor, ref)
		if e != nil {
			return e
		}
		if e = r.require(ctx, tx, p.ID, actor, accounts.Viewer); e != nil {
			return e
		}
		var followApproval bool
		if e = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT requires_approval AND(expires_at IS NULL OR expires_at>statement_timestamp()) FROM owned_publisher_follow_policy WHERE publisher_id=$1),false)`, p.ID).Scan(&followApproval); e != nil {
			return e
		}
		out["followRequiresApproval"] = followApproval
		rows, e := tx.QueryContext(ctx, `SELECT flag FROM owned_publisher_features WHERE publisher_id=$1 AND(expired_at IS NULL OR expired_at>statement_timestamp()) ORDER BY flag`, p.ID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var flag string
			if e = rows.Scan(&flag); e != nil {
				return e
			}
			out[flag] = true
		}
		return rows.Err()
	})
	return
}

func validFeature(flag string) bool {
	if flag != strings.TrimSpace(flag) || flag == "" || utf8.RuneCountInString(flag) > 128 || strings.HasPrefix(flag, "system.") {
		return false
	}
	for _, c := range flag {
		if !(c == '.' || c == '-' || c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

func (r *Repository) SetPublisherFeature(ctx context.Context, actor, ref, flag string, expires *time.Time, enabled bool) (out accounts.PublisherFeature, err error) {
	if !validFeature(flag) {
		return out, accounts.ErrInvalid
	}
	if enabled && expires != nil && !expires.After(r.now().UTC()) {
		return out, accounts.ErrInvalid
	}
	err = r.write(ctx, actor, "publishers.features.manage", func(tx *sql.Tx, now time.Time) error {
		p, e := r.lockPublisher(ctx, tx, actor, ref)
		if e != nil {
			return e
		}
		if e = r.require(ctx, tx, p.ID, actor, accounts.Manager); e != nil {
			return e
		}
		action := "publishers.feature.remove"
		if enabled {
			_, e = tx.ExecContext(ctx, `INSERT INTO owned_publisher_features(publisher_id,flag,actor_id,created_at,updated_at,expired_at) VALUES($1,$2,$3,$4,$4,$5) ON CONFLICT(publisher_id,flag) DO UPDATE SET actor_id=EXCLUDED.actor_id,updated_at=EXCLUDED.updated_at,expired_at=EXCLUDED.expired_at`, p.ID, flag, actor, now, expires)
			action = "publishers.feature.add"
		} else {
			_, e = tx.ExecContext(ctx, `DELETE FROM owned_publisher_features WHERE publisher_id=$1 AND flag=$2`, p.ID, flag)
		}
		if e != nil {
			return e
		}
		if flag == "followRequiresApproval" {
			_, e = tx.ExecContext(ctx, `INSERT INTO owned_publisher_follow_policy(publisher_id,requires_approval,expires_at)VALUES($1,$2,$3) ON CONFLICT(publisher_id)DO UPDATE SET requires_approval=EXCLUDED.requires_approval,expires_at=EXCLUDED.expires_at`, p.ID, enabled, expires)
			if e != nil {
				return e
			}
		}
		out = accounts.PublisherFeature{PublisherID: p.ID, Flag: flag, ExpiredAt: expires}
		return audit(ctx, tx, actor, action, p.ID, now)
	})
	return
}

func (r *Repository) PublisherRewards(ctx context.Context, actor, ref string) (out accounts.PublisherRewardPreview, err error) {
	if !authority.ValidID(actor) {
		return out, accounts.ErrForbidden
	}
	err = r.publisherRead(ctx, actor, func(tx *sql.Tx) error {
		p, e := r.visiblePublisher(ctx, tx, actor, ref)
		if e != nil {
			return e
		}
		if e = r.require(ctx, tx, p.ID, actor, accounts.Viewer); e != nil {
			return e
		}
		return tx.QueryRowContext(ctx, `SELECT COALESCE(sum(CASE WHEN r.settled_at IS NOT NULL THEN a.share_amount ELSE 0 END),0)::text,COALESCE(sum(CASE WHEN r.settled_at IS NULL THEN a.share_amount ELSE 0 END),0)::text,count(*) FILTER(WHERE r.purchase_id IS NOT NULL AND r.settled_at IS NULL) FROM owned_post_award_purchases a LEFT JOIN owned_post_award_receipts r ON r.purchase_id=a.id WHERE a.publisher_id=$1 AND a.fulfilled_at IS NOT NULL`, p.ID).Scan(&out.SettledAmount, &out.PendingAmount, &out.PendingCount)
	})
	return
}

func (r *Repository) PublisherRelationships(ctx context.Context, actor, kind string) (out []accounts.PublisherRelationship, err error) {
	if !authority.ValidID(actor) {
		return nil, accounts.ErrForbidden
	}
	column := ""
	switch kind {
	case "blocked":
		column = "blocked_at"
	case "muted":
		column = "muted_at"
	default:
		return nil, accounts.ErrInvalid
	}
	out = []accounts.PublisherRelationship{}
	err = r.publisherRead(ctx, actor, func(tx *sql.Tx) error {
		rows, e := tx.QueryContext(ctx, `SELECT pr.publisher_id::text,pr.blocked_at,pr.muted_at,pr.updated_at FROM owned_publisher_relationships pr JOIN owned_publishers p ON p.id=pr.publisher_id WHERE pr.account_id=$1 AND pr.`+column+` IS NOT NULL AND p.deleted_at IS NULL ORDER BY pr.updated_at DESC,pr.publisher_id`, actor)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v accounts.PublisherRelationship
			if e = rows.Scan(&v.PublisherID, &v.BlockedAt, &v.MutedAt, &v.UpdatedAt); e != nil {
				return e
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return
}

func (r *Repository) PublisherRelationshipState(ctx context.Context, actor, ref string) (out accounts.PublisherRelationship, err error) {
	if !authority.ValidID(actor) {
		return out, accounts.ErrForbidden
	}
	err = r.publisherRead(ctx, actor, func(tx *sql.Tx) error {
		p, e := r.visiblePublisher(ctx, tx, actor, ref)
		if e != nil {
			return e
		}
		out.PublisherID = p.ID
		e = tx.QueryRowContext(ctx, `SELECT blocked_at,muted_at,updated_at FROM owned_publisher_relationships WHERE account_id=$1 AND publisher_id=$2`, actor, p.ID).Scan(&out.BlockedAt, &out.MutedAt, &out.UpdatedAt)
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		return e
	})
	return
}

func (r *Repository) SetPublisherRelationship(ctx context.Context, actor, ref, kind string, enabled bool) (out accounts.PublisherRelationship, err error) {
	column := ""
	switch kind {
	case "block":
		column = "blocked_at"
	case "mute":
		column = "muted_at"
	default:
		return out, accounts.ErrInvalid
	}
	err = r.write(ctx, actor, "publishers.subscriptions.manage", func(tx *sql.Tx, now time.Time) error {
		p, e := r.lockPublisher(ctx, tx, actor, ref)
		if e != nil {
			return e
		}
		if p.AccountID != nil && *p.AccountID == actor {
			return accounts.ErrInvalid
		}
		if enabled {
			_, e = tx.ExecContext(ctx, `INSERT INTO owned_publisher_relationships(account_id,publisher_id,`+column+`,updated_at) VALUES($1,$2,$3,$3) ON CONFLICT(account_id,publisher_id) DO UPDATE SET `+column+`=EXCLUDED.`+column+`,updated_at=EXCLUDED.updated_at`, actor, p.ID, now)
		} else {
			_, e = tx.ExecContext(ctx, `UPDATE owned_publisher_relationships SET `+column+`=NULL,updated_at=$3 WHERE account_id=$1 AND publisher_id=$2`, actor, p.ID, now)
			if e == nil {
				_, e = tx.ExecContext(ctx, `DELETE FROM owned_publisher_relationships WHERE account_id=$1 AND publisher_id=$2 AND blocked_at IS NULL AND muted_at IS NULL`, actor, p.ID)
			}
		}
		if e != nil {
			return e
		}
		out = accounts.PublisherRelationship{PublisherID: p.ID, UpdatedAt: now}
		e = tx.QueryRowContext(ctx, `SELECT blocked_at,muted_at FROM owned_publisher_relationships WHERE account_id=$1 AND publisher_id=$2`, actor, p.ID).Scan(&out.BlockedAt, &out.MutedAt)
		if e != nil && (!errors.Is(e, sql.ErrNoRows) || enabled) {
			return e
		}
		action := "publishers." + kind
		if !enabled {
			action = "publishers.un" + kind
		}
		return audit(ctx, tx, actor, action, p.ID, now)
	})
	return
}

var _ accounts.PublisherCompletionRepository = (*Repository)(nil)
