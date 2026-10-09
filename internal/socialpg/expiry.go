// SPDX-License-Identifier: AGPL-3.0-only
package socialpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/passport/social"
	"sort"
	"time"
)

type expiryCandidate struct {
	kind, id, owner, target string
	status                  int
	at                      time.Time
	degrade                 sql.NullInt64
	revision                int64
}

func expiryCandidates(ctx context.Context, q querier, now time.Time, a, b any, limit int) ([]expiryCandidate, error) {
	rows, e := q.QueryContext(ctx, `SELECT 'relationship',id::text,account_id::text,related_id::text,status,expired_at,degrade_to,policy_revision FROM owned_social_relationships WHERE expired_at<=$1 AND($2::uuid IS NULL OR((account_id=$2 AND related_id=$3)OR(account_id=$3 AND related_id=$2))) UNION ALL SELECT 'request',id::text,account_id::text,related_id::text,0,expired_at,NULL::integer,policy_revision FROM owned_social_requests WHERE expired_at<=$1 AND($2::uuid IS NULL OR((account_id=$2 AND related_id=$3)OR(account_id=$3 AND related_id=$2))) ORDER BY 6,2 LIMIT $4`, now, a, b, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []expiryCandidate{}
	for rows.Next() {
		var c expiryCandidate
		if e = rows.Scan(&c.kind, &c.id, &c.owner, &c.target, &c.status, &c.at, &c.degrade, &c.revision); e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func expireCandidate(ctx context.Context, tx *sql.Tx, c expiryCandidate, now time.Time) (bool, error) {
	var result sql.Result
	var e error
	if c.kind == "request" {
		result, e = tx.ExecContext(ctx, `DELETE FROM owned_social_requests WHERE id=$1 AND policy_revision=$2 AND expired_at<=$3`, c.id, c.revision, now)
	} else if c.degrade.Valid {
		result, e = tx.ExecContext(ctx, `UPDATE owned_social_relationships SET status=degrade_to,expired_at=NULL,degrade_to=NULL,updated_at=$3,policy_revision=nextval('owned_social_policy_revision') WHERE id=$1 AND policy_revision=$2 AND expired_at<=$3`, c.id, c.revision, now)
	} else {
		result, e = tx.ExecContext(ctx, `DELETE FROM owned_social_relationships WHERE id=$1 AND policy_revision=$2 AND expired_at<=$3`, c.id, c.revision, now)
	}
	if e != nil {
		return false, e
	}
	n, e := result.RowsAffected()
	if e != nil || n == 0 {
		return false, e
	}
	id, e := newID()
	if e != nil {
		return false, e
	}
	var next any
	if c.degrade.Valid {
		next = c.degrade.Int64
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_social_expiry_events(id,kind,source_id,policy_revision,account_id,related_id,previous_status,next_status,expired_at,processed_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, id, c.kind, c.id, c.revision, c.owner, c.target, c.status, next, c.at, now)
	return e == nil, e
}

// Existing relationship writers already retain both canonical account locks.
func expirePair(ctx context.Context, tx *sql.Tx, a, b string, now time.Time) error {
	rows, e := expiryCandidates(ctx, tx, now, a, b, 4)
	if e != nil {
		return e
	}
	for _, c := range rows {
		if _, e = expireCandidate(ctx, tx, c, now); e != nil {
			return e
		}
	}
	return nil
}

// ProcessExpiredRelationships is explicit, bounded maintenance. Current-time
// read policy does not depend on this worker having run. No remote calls occur.
func (r *Repository) ProcessExpiredRelationships(ctx context.Context, limit int) (int, error) {
	if r == nil || r.db == nil {
		return 0, social.ErrUnavailable
	}
	if limit < 1 || limit > 1000 {
		return 0, social.ErrInvalid
	}
	var now time.Time
	if e := r.db.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
		return 0, e
	}
	rows, e := expiryCandidates(ctx, r.db, now, nil, nil, limit)
	if e != nil {
		return 0, e
	}
	n := 0
	for _, c := range rows {
		done, e := r.expireOne(ctx, c)
		if e != nil {
			return n, e
		}
		if done {
			n++
		}
	}
	return n, nil
}
func (r *Repository) expireOne(ctx context.Context, c expiryCandidate) (bool, error) {
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	ids := []string{c.owner, c.target}
	sort.Strings(ids)
	for _, id := range ids {
		var held string
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, id).Scan(&held); e != nil {
			return false, e
		}
	}
	var now time.Time
	if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
		return false, e
	}
	done, e := expireCandidate(ctx, tx, c, now)
	if e != nil {
		return false, e
	}
	return done, tx.Commit()
}
func (r *Repository) ExpiryReady(ctx context.Context) error {
	if r == nil || r.db == nil {
		return social.ErrUnavailable
	}
	_, e := r.db.ExecContext(ctx, `SELECT expired_at,degrade_to,policy_revision FROM owned_social_relationships LIMIT 0;SELECT expired_at,policy_revision FROM owned_social_requests LIMIT 0;SELECT account_id,status FROM owned_social_effective_relationships LIMIT 0;SELECT account_id FROM owned_social_effective_requests LIMIT 0;SELECT source_id,policy_revision FROM owned_social_expiry_events LIMIT 0`)
	if e != nil {
		return errors.Join(social.ErrUnavailable, e)
	}
	return nil
}
