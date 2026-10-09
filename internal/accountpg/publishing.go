// SPDX-License-Identifier: AGPL-3.0-only
package accountpg

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"

	"solsynth.local/go-migration/ports/passport/accounts"
)

// PublishingFederation observes an already enabled, registered local actor.
// It cannot enable federation, provision keys or create delivery authority.
type PublishingFederation interface {
	PublishingActorEnabled(context.Context, *sql.Tx, string) (bool, error)
}

func (r *Repository) WithPublishingFederation(f PublishingFederation) *Repository {
	r.publishingFederation = f
	return r
}

const publishingColumns = `id::text,account_id::text,default_publisher_id::text,default_reply_publisher_id::text,default_fediverse_publisher_id::text,created_at,updated_at`

func readPublishing(ctx context.Context, tx *sql.Tx, actor string) (out accounts.PublishingSettings, e error) {
	e = tx.QueryRowContext(ctx, `SELECT `+publishingColumns+` FROM owned_publishing_settings WHERE account_id=$1`, actor).Scan(&out.ID, &out.AccountID, &out.DefaultPostingPublisherID, &out.DefaultReplyPublisherID, &out.DefaultFediversePublisherID, &out.CreatedAt, &out.UpdatedAt)
	return
}

// Cleanup preserves the row identity and every unrelated choice. A publisher
// deletion clears only matching choices; member removal clears only that member.
const clearPublishingPublisher = `UPDATE owned_publishing_settings SET default_publisher_id=CASE WHEN default_publisher_id=$1 THEN NULL ELSE default_publisher_id END,default_reply_publisher_id=CASE WHEN default_reply_publisher_id=$1 THEN NULL ELSE default_reply_publisher_id END,default_fediverse_publisher_id=CASE WHEN default_fediverse_publisher_id=$1 THEN NULL ELSE default_fediverse_publisher_id END,updated_at=$3 WHERE ($2::uuid IS NULL OR account_id=$2) AND(default_publisher_id=$1 OR default_reply_publisher_id=$1 OR default_fediverse_publisher_id=$1)`

// These helpers join an already-authorized canonical deletion transaction.
// They preserve settings identity and all choices outside the deleted scope.
func ClearPublishingPublisher(ctx context.Context, tx *sql.Tx, publisher string, at time.Time) error {
	_, e := tx.ExecContext(ctx, clearPublishingPublisher, publisher, nil, at)
	return e
}
func ClearPublishingRealm(ctx context.Context, tx *sql.Tx, realm string, at time.Time) error {
	_, e := tx.ExecContext(ctx, `WITH removed AS(SELECT id FROM owned_publishers WHERE realm_id=$1) UPDATE owned_publishing_settings SET default_publisher_id=CASE WHEN default_publisher_id IN(SELECT id FROM removed) THEN NULL ELSE default_publisher_id END,default_reply_publisher_id=CASE WHEN default_reply_publisher_id IN(SELECT id FROM removed) THEN NULL ELSE default_reply_publisher_id END,default_fediverse_publisher_id=CASE WHEN default_fediverse_publisher_id IN(SELECT id FROM removed) THEN NULL ELSE default_fediverse_publisher_id END,updated_at=$2 WHERE default_publisher_id IN(SELECT id FROM removed) OR default_reply_publisher_id IN(SELECT id FROM removed) OR default_fediverse_publisher_id IN(SELECT id FROM removed)`, realm, at)
	return e
}

func (r *Repository) PublishingSettings(ctx context.Context, actor string) (accounts.PublishingSettings, error) {
	return r.publishingSettings(ctx, actor, nil)
}
func (r *Repository) UpdatePublishingSettings(ctx context.Context, actor string, p accounts.PublishingSettingsPatch) (accounts.PublishingSettings, error) {
	if e := p.Validate(); e != nil {
		return accounts.PublishingSettings{}, e
	}
	return r.publishingSettings(ctx, actor, &p)
}
func (r *Repository) publishingSettings(ctx context.Context, actor string, patch *accounts.PublishingSettingsPatch) (out accounts.PublishingSettings, err error) {
	err = r.write(ctx, actor, "", func(tx *sql.Tx, now time.Time) error {
		ids := []string{}
		if patch != nil {
			for _, id := range []*string{patch.DefaultPostingPublisherID, patch.DefaultReplyPublisherID, patch.DefaultFediversePublisherID} {
				if id != nil {
					ids = append(ids, *id)
				}
			}
		}
		sort.Strings(ids)
		var ancestry map[string]string
		var e error
		if r.realms != nil && len(ids) > 0 {
			ancestry, e = r.realms.PrelockPublisherRealms(ctx, tx, ids)
			if errors.Is(e, accounts.ErrNotFound) {
				return accounts.ErrPublishingPublisher
			}
			if e != nil {
				return e
			}
		}
		for i, id := range ids {
			if i > 0 && ids[i-1] == id {
				continue
			}
			p, e := lookupPublisher(ctx, tx, id, true)
			if errors.Is(e, accounts.ErrNotFound) {
				return accounts.ErrPublishingPublisher
			}
			if e != nil {
				return e
			}
			if p.Type != 0 && r.realms == nil {
				return accounts.ErrUnsupported
			}
		}
		if e = VerifyPublisherRealms(ctx, tx, ancestry); e != nil {
			return e
		}
		check := func() error {
			for _, id := range ids {
				// Re-evaluate time-dependent owner and moderation state after every
				// possible wait; selecting a default never confers editor rights.
				owner := r.activePublishingOwner("p")
				var active bool
				if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_publishers p WHERE p.id=$1 AND p.deleted_at IS NULL AND `+owner+`)`, id).Scan(&active); e != nil {
					return e
				}
				if !active {
					return accounts.ErrPublishingPublisher
				}
				if e = r.require(ctx, tx, id, actor, accounts.Viewer); e != nil {
					if errors.Is(e, accounts.ErrForbidden) || errors.Is(e, accounts.ErrNotFound) {
						return accounts.ErrPublishingMembership
					}
					return e
				}
				if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_publishers p WHERE p.id=$1 AND `+r.publisherVisible("p", "$2")+`)`, id, actor).Scan(&active); e != nil {
					return e
				}
				if !active {
					return accounts.ErrPublishingPublisher
				}
			}
			if patch != nil && patch.DefaultFediversePublisherID != nil {
				if r.publishingFederation == nil {
					return accounts.ErrPublishingFederation
				}
				ok, e := r.publishingFederation.PublishingActorEnabled(ctx, tx, *patch.DefaultFediversePublisherID)
				if e != nil {
					return e
				}
				if !ok {
					return accounts.ErrPublishingFederation
				}
			}
			if r.requireAuthority {
				_, e = r.guard.Authenticate(ctx, r.db, tx, actor)
				return e
			}
			return nil
		}
		if e = check(); e != nil {
			return e
		}
		id, e := newID()
		if e != nil {
			return e
		}
		result, e := tx.ExecContext(ctx, `INSERT INTO owned_publishing_settings(account_id,id,created_at)VALUES($1,$2,$3)ON CONFLICT(account_id)DO NOTHING`, actor, id, now)
		if e != nil {
			return e
		}
		created, e := result.RowsAffected()
		if e != nil {
			return e
		}
		if patch != nil {
			_, e = tx.ExecContext(ctx, `UPDATE owned_publishing_settings SET default_publisher_id=COALESCE($2::uuid,default_publisher_id),default_reply_publisher_id=COALESCE($3::uuid,default_reply_publisher_id),default_fediverse_publisher_id=COALESCE($4::uuid,default_fediverse_publisher_id),updated_at=clock_timestamp() WHERE account_id=$1`, actor, patch.DefaultPostingPublisherID, patch.DefaultReplyPublisherID, patch.DefaultFediversePublisherID)
			if e != nil {
				return e
			}
		}
		out, e = readPublishing(ctx, tx, actor)
		if e != nil {
			return e
		}
		if patch != nil || created > 0 {
			action := "publishing.initialize"
			if patch != nil {
				action = "publishing.update"
			}
			if e = audit(ctx, tx, actor, action, out.ID, now); e != nil {
				return e
			}
		}
		return check()
	})
	if err != nil {
		return accounts.PublishingSettings{}, err
	}
	return
}

// The individual-only adapter cannot name realm tables that were not installed.
func (r *Repository) activePublishingOwner(alias string) string {
	if r.realms != nil {
		return ActivePublisherOwnerPredicate(alias)
	}
	return `(` + alias + `.type=0 AND EXISTS(SELECT 1 FROM owned_authority_accounts a WHERE a.id=` + alias + `.account_id AND NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments pun WHERE pun.account_id=a.id AND pun.kind='disable_account' AND(pun.starts_at IS NULL OR pun.starts_at<=clock_timestamp())AND(pun.expires_at IS NULL OR pun.expires_at>clock_timestamp()))))`
}
