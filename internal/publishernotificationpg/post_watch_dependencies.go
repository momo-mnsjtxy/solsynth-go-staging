// SPDX-License-Identifier: AGPL-3.0-only
// Observe ancestry before locking; source/recipient locks follow the same
// account -> realm -> publisher -> post order as canonical post mutations.
// Dependency locks never wait: a publication can already own its author's
// account before its recipient foreign key requests KEY SHARE in reverse order.
package publishernotificationpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	"solsynth.local/go-migration/ports/notifications"
)

// ErrWatchRetry means another transaction currently owns a required dependency.
// The caller must roll back and leave the durable item available for retry; it
// must not record suppression, commit a delivery lease, or contact a provider.
var ErrWatchRetry = errors.New("publisher watcher dependency is busy; retry the durable item")

func watchLockError(err error) error {
	var failure *pgconn.PgError
	if errors.As(err, &failure) && failure.Code == "55P03" {
		return errors.Join(ErrWatchRetry, err)
	}
	return err
}

type watchDependencies struct {
	raw        string
	Posts      []string
	Publishers []struct{ ID, Owner string }
	Realms     []struct{ ID, Owner string }
}

const watchDependenciesSQL = `WITH target AS(
 SELECT q.targets,q.witnesses,i.post_id FROM owned_post_watch_notification_intents q JOIN owned_publisher_content_notification_intents i ON i.id=q.publication_id WHERE q.id=$1
), post_ids AS(
 SELECT post_id id FROM target UNION SELECT unnest(targets) FROM target
 UNION SELECT (witness->>'id')::uuid FROM target CROSS JOIN LATERAL jsonb_array_elements(witnesses) witness WHERE witness->>'source'='reply'
), pp AS(SELECT p.id,p.publisher_id,p.realm_id FROM owned_text_posts p JOIN post_ids id ON id.id=p.id),
 pubs AS(SELECT u.id,u.account_id,u.realm_id FROM owned_publishers u WHERE u.id IN(SELECT publisher_id FROM pp)),
 realms AS(SELECT r.id,r.account_id FROM owned_realms r WHERE r.id IN(SELECT realm_id FROM pp UNION SELECT realm_id FROM pubs))
 SELECT jsonb_build_object(
 'Posts',COALESCE((SELECT jsonb_agg(id ORDER BY id) FROM pp),'[]'::jsonb),
 'Publishers',COALESCE((SELECT jsonb_agg(jsonb_build_object('ID',id,'Owner',COALESCE(account_id::text,'')) ORDER BY id) FROM pubs),'[]'::jsonb),
 'Realms',COALESCE((SELECT jsonb_agg(jsonb_build_object('ID',id,'Owner',account_id) ORDER BY id) FROM realms),'[]'::jsonb),
 'PostScopes',COALESCE((SELECT jsonb_agg(to_jsonb(pp) ORDER BY id) FROM pp),'[]'::jsonb),
 'PublisherScopes',COALESCE((SELECT jsonb_agg(to_jsonb(pubs) ORDER BY id) FROM pubs),'[]'::jsonb),
 'Relations',COALESCE((SELECT jsonb_agg(to_jsonb(l) ORDER BY l.post_id) FROM owned_post_relations l JOIN pp ON pp.id=l.post_id),'[]'::jsonb)
 )::text`

func observeWatchDependencies(ctx context.Context, tx *sql.Tx, id string) (*watchDependencies, error) {
	var p watchDependencies
	if e := tx.QueryRowContext(ctx, watchDependenciesSQL, id).Scan(&p.raw); e != nil {
		return nil, e
	}
	if e := json.Unmarshal([]byte(p.raw), &p); e != nil {
		return nil, e
	}
	return &p, nil
}
func (p *watchDependencies) verify(ctx context.Context, tx *sql.Tx, id string) error {
	current, e := observeWatchDependencies(ctx, tx, id)
	if e != nil {
		return e
	}
	if p.raw != current.raw {
		return notifications.ErrConflict
	}
	return nil
}
func (p *watchDependencies) lock(ctx context.Context, tx *sql.Tx, recipient string) error {
	accounts := []string{recipient}
	for _, publisher := range p.Publishers {
		accounts = append(accounts, publisher.Owner)
	}
	for _, realm := range p.Realms {
		accounts = append(accounts, realm.Owner)
	}
	for _, id := range uniqueSorted(accounts...) {
		mode := ` FOR SHARE`
		if id == recipient {
			mode = ` FOR UPDATE`
		}
		var held string
		if e := tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1`+mode+` NOWAIT`, id).Scan(&held); e != nil {
			return watchLockError(e)
		}
	}
	for _, realm := range p.Realms {
		var held string
		if e := tx.QueryRowContext(ctx, `SELECT id::text FROM owned_realms WHERE id=$1 FOR SHARE NOWAIT`, realm.ID).Scan(&held); e != nil {
			return watchLockError(e)
		}
	}
	for _, publisher := range p.Publishers {
		var held string
		if e := tx.QueryRowContext(ctx, `SELECT id::text FROM owned_publishers WHERE id=$1 FOR SHARE NOWAIT`, publisher.ID).Scan(&held); e != nil {
			return watchLockError(e)
		}
	}
	for _, post := range p.Posts {
		var held string
		if e := tx.QueryRowContext(ctx, `SELECT id::text FROM owned_text_posts WHERE id=$1 FOR SHARE NOWAIT`, post).Scan(&held); e != nil {
			return watchLockError(e)
		}
	}
	return nil
}
