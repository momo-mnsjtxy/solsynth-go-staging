// SPDX-License-Identifier: AGPL-3.0-only
package blogpg

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/blogs"
	"time"
)

func (r *Repository) VerificationEnabled() bool { return r != nil && r.config.Verifier != nil }
func (r *Repository) captureIntent(ctx context.Context, tx *sql.Tx, actor string, v blogs.Domain) error {
	p, e := r.guard.Authorize(ctx, r.db, tx, actor, "publishers.domains.manage")
	if e != nil {
		return e
	}
	if p.Session.Type != 0 || p.Session.ClientID != "" {
		return authority.ErrForbidden
	}
	stamp, e := verificationPolicyStamp(ctx, tx, actor, v.PublisherID, p.Session.ID)
	if e != nil {
		return e
	}
	// Capture the original session ancestry's deadline. Normal refresh rotates
	// credentials and can extend a session, but cannot extend this admission.
	var expires time.Time
	e = tx.QueryRowContext(ctx, `WITH RECURSIVE chain AS(SELECT id,parent_id,expires_at,ARRAY[id] path FROM owned_authority_sessions WHERE id=$1::uuid AND account_id=$2::uuid UNION ALL SELECT s.id,s.parent_id,s.expires_at,c.path||s.id FROM chain c JOIN owned_authority_sessions s ON s.id=c.parent_id AND s.account_id=$2::uuid WHERE NOT s.id=ANY(c.path) AND cardinality(c.path)<64) SELECT min(expires_at) FROM chain`, p.Session.ID, actor).Scan(&expires)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_publisher_domain_intents(domain_id,actor_id,session_id,account_version,authorization_epoch,revision,expires_at,policy_stamp)VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb)ON CONFLICT(domain_id)DO UPDATE SET actor_id=EXCLUDED.actor_id,session_id=EXCLUDED.session_id,account_version=EXCLUDED.account_version,authorization_epoch=EXCLUDED.authorization_epoch,revision=EXCLUDED.revision,expires_at=EXCLUDED.expires_at,policy_stamp=EXCLUDED.policy_stamp`, v.ID, actor, p.Session.ID, p.Account.Version, p.Session.AuthorizationEpoch, v.Revision, expires, stamp)
	return e
}

type pending struct {
	id, actor, publisher, session string
	version, epoch, revision      int64
	expires                       time.Time
	stamp                         []byte
}

func (r *Repository) pendingEligible(ctx context.Context, tx *sql.Tx, j pending) (bool, error) {
	var live bool
	if e := tx.QueryRowContext(ctx, `SELECT $1::timestamptz>clock_timestamp()`, j.expires).Scan(&live); e != nil || !live {
		return false, e
	}
	ok, e := r.guard.StoredSessionEligible(ctx, r.db, tx, j.actor, j.session, j.version, j.epoch, "publishers.domains.manage")
	if e != nil || !ok {
		return false, e
	}
	stamp, e := verificationPolicyStamp(ctx, tx, j.actor, j.publisher, j.session)
	return e == nil && bytes.Equal(stamp, j.stamp), e
}
func denied(e error) bool {
	var a *accounts.Failure
	var p *authority.Failure
	return errors.As(e, &a) && a.Status < 500 || errors.As(e, &p) && p.Status < 500
}

// pendingBegin only resumes an already admitted revision. Canonical actor,
// publisher and domain locks are acquired in the same order as user mutations.
func (r *Repository) pendingBegin(ctx context.Context, j pending) (*sql.Tx, accounts.Publisher, blogs.Domain, bool, error) {
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return nil, accounts.Publisher{}, blogs.Domain{}, false, e
	}
	abort := func(e error) (*sql.Tx, accounts.Publisher, blogs.Domain, bool, error) {
		tx.Rollback()
		return nil, accounts.Publisher{}, blogs.Domain{}, false, e
	}
	var id string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, j.actor).Scan(&id); e != nil {
		return abort(e)
	}
	ok, e := r.pendingEligible(ctx, tx, j)
	if e != nil {
		return abort(e)
	}
	var p accounts.Publisher
	if ok {
		p, e = r.accounts.RequirePostEditor(ctx, tx, j.actor, j.publisher)
		if e != nil {
			if !denied(e) {
				return abort(e)
			}
			ok = false
		}
	}
	if ok {
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_publisher_members WHERE publisher_id=$1 AND account_id=$2 AND role>=$3 AND joined_at IS NOT NULL AND revoked_at IS NULL)`, j.publisher, j.actor, accounts.Manager).Scan(&ok); e != nil {
			return abort(e)
		}
	}
	v, e := scan(tx.QueryRowContext(ctx, `SELECT `+columns+` FROM owned_publisher_domains WHERE id=$1 AND publisher_id=$2 AND revision=$3 AND status=0 AND deleted_at IS NULL FOR UPDATE`, j.id, j.publisher, j.revision))
	if e != nil {
		return abort(e)
	}
	var same bool
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_publisher_domain_intents WHERE domain_id=$1 AND actor_id=$2 AND session_id=$3 AND revision=$4 AND account_version=$5 AND authorization_epoch=$6)`, j.id, j.actor, j.session, j.revision, j.version, j.epoch).Scan(&same)
	if e != nil {
		return abort(e)
	}
	if !same {
		return abort(blogs.ErrConflict)
	}
	if ok {
		ok, e = r.pendingEligible(ctx, tx, j)
		if e != nil {
			return abort(e)
		}
	}
	return tx, p, v, ok, nil
}
func (r *Repository) revokePending(ctx context.Context, tx *sql.Tx, j pending, v blogs.Domain) error {
	now := r.config.Now().UTC()
	if _, e := tx.ExecContext(ctx, `UPDATE owned_publisher_domains SET status=3,last_error='Verification authority expired',updated_at=$2 WHERE id=$1 AND status=0 AND revision=$3`, j.id, now, j.revision); e != nil {
		return e
	}
	if e := audit(ctx, tx, j.actor, v, "revoked", now); e != nil {
		return e
	}
	return tx.Commit()
}
func (r *Repository) processPending(ctx context.Context, j pending) (bool, error) {
	tx, p, v, ok, e := r.pendingBegin(ctx, j)
	if errors.Is(e, blogs.ErrNotFound) || errors.Is(e, blogs.ErrConflict) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if !ok {
		defer tx.Rollback()
		return true, r.revokePending(ctx, tx, j, v)
	}
	if e = tx.Commit(); e != nil {
		return false, e
	}
	verified, fetchError := r.config.Verifier.VerifyDomain(ctx, v.Domain, p.Name)
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	tx, current, actual, ok, e := r.pendingBegin(ctx, j)
	if errors.Is(e, blogs.ErrNotFound) || errors.Is(e, blogs.ErrConflict) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	if !ok {
		return true, r.revokePending(ctx, tx, j, actual)
	}
	if _, e = r.applyVerification(ctx, tx, j.actor, p, current, v, verified, fetchError); e != nil {
		return false, e
	}
	ok, e = r.pendingEligible(ctx, tx, j)
	if e != nil {
		return false, e
	}
	if !ok {
		return false, authority.ErrForbidden
	}
	return true, tx.Commit()
}

// ProcessPending recovers interrupted admissions only. Failed/revoked domains
// require a fresh explicit recheck. Provider work never holds a SQL transaction.
func (r *Repository) ProcessPending(ctx context.Context, limit int) (int, error) {
	if !r.VerificationEnabled() || r.db == nil || r.guard == nil {
		return 0, blogs.ErrUnavailable
	}
	if limit < 1 || limit > 100 {
		return 0, blogs.ErrInvalid
	}
	rows, e := r.db.QueryContext(ctx, `SELECT d.id::text,i.actor_id::text,d.publisher_id::text,i.session_id::text,i.account_version,i.authorization_epoch,i.revision,i.expires_at,i.policy_stamp FROM owned_publisher_domains d JOIN owned_publisher_domain_intents i ON i.domain_id=d.id AND i.revision=d.revision WHERE d.status=0 AND d.deleted_at IS NULL ORDER BY d.updated_at,d.id LIMIT $1`, limit)
	if e != nil {
		return 0, e
	}
	jobs := []pending{}
	for rows.Next() {
		var j pending
		if e = rows.Scan(&j.id, &j.actor, &j.publisher, &j.session, &j.version, &j.epoch, &j.revision, &j.expires, &j.stamp); e != nil {
			rows.Close()
			return 0, e
		}
		jobs = append(jobs, j)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	n := 0
	for _, j := range jobs {
		done, e := r.processPending(ctx, j)
		if e != nil {
			return n, e
		}
		if done {
			n++
		}
	}
	return n, nil
}

// This stamp is evaluated under canonical actor/realm/publisher locks and kept
// as PostgreSQL jsonb bytes. Membership row versions detect revoke/rejoin ABA;
// relevant grant IDs prevent equivalent replacement policy reviving old work.
func verificationPolicyStamp(ctx context.Context, tx *sql.Tx, actor, publisher, session string) ([]byte, error) {
	var stamp []byte
	e := tx.QueryRowContext(ctx, `WITH RECURSIVE chain AS(SELECT id,parent_id,authorization_epoch,ARRAY[id] path FROM owned_authority_sessions WHERE id=$3::uuid AND account_id=$1::uuid UNION ALL SELECT s.id,s.parent_id,s.authorization_epoch,c.path||s.id FROM chain c JOIN owned_authority_sessions s ON s.id=c.parent_id AND s.account_id=$1::uuid WHERE NOT s.id=ANY(c.path) AND cardinality(c.path)<64)
 SELECT jsonb_build_object(
 'generation',COALESCE((SELECT revision FROM owned_publisher_blog_authority_epochs WHERE account_id=a.id),0),
 'sessions',(SELECT jsonb_agg(jsonb_build_object('id',id,'epoch',authorization_epoch) ORDER BY id) FROM chain),
 'actor',jsonb_build_object('version',a.version,'disabled',a.disabled,'superuser',a.is_superuser),
 'publisher',jsonb_build_object('id',p.id,'type',p.type,'name',p.name,'owner',p.account_id,'realm',p.realm_id,'deleted',p.deleted_at),
 'owner',(SELECT jsonb_build_object('version',o.version,'disabled',o.disabled,'generation',COALESCE((SELECT revision FROM owned_publisher_blog_authority_epochs WHERE account_id=o.id),0)) FROM owned_authority_accounts o WHERE o.id=p.account_id),
 'member',(SELECT to_jsonb(m)||jsonb_build_object('incarnation',m.xmin::text) FROM owned_publisher_members m WHERE m.publisher_id=p.id AND m.account_id=a.id),
 'grants',(SELECT jsonb_agg(to_jsonb(g) ORDER BY g.id) FROM owned_authority_grants g WHERE g.account_id=a.id AND g.pattern IN('*','publishers.*','publishers.domains.*','publishers.domains.manage')),
 'groups',(SELECT jsonb_agg(jsonb_build_object('grant',to_jsonb(g),'membership',m.xmin::text) ORDER BY g.id) FROM owned_authority_group_grants g JOIN owned_authority_group_members m ON m.group_id=g.group_id WHERE m.account_id=a.id AND g.pattern IN('*','publishers.*','publishers.domains.*','publishers.domains.manage')),
 'punishments',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM owned_authority_punishments x WHERE x.account_id IN(a.id,p.account_id) AND(x.kind='disable_account' OR x.kind='permission_modification' AND x.pattern IN('*','publishers.*','publishers.domains.*','publishers.domains.manage'))),
 'realm',(SELECT jsonb_build_object('id',r.id,'owner',r.account_id,'owner_version',o.version,'owner_disabled',o.disabled,'owner_generation',COALESCE((SELECT revision FROM owned_publisher_blog_authority_epochs WHERE account_id=o.id),0),'deleted',r.deleted_at,'member',to_jsonb(m),'incarnation',m.xmin::text) FROM owned_realms r JOIN owned_authority_accounts o ON o.id=r.account_id LEFT JOIN owned_realm_members m ON m.realm_id=r.id AND m.account_id=a.id WHERE r.id=p.realm_id)
 ) FROM owned_authority_accounts a CROSS JOIN owned_publishers p WHERE a.id=$1::uuid AND p.id=$2::uuid`, actor, publisher, session).Scan(&stamp)
	return stamp, e
}

func (r *Repository) currentIntent(ctx context.Context, tx *sql.Tx, actor string, v blogs.Domain) (pending, error) {
	j := pending{id: v.ID, actor: actor, publisher: v.PublisherID, revision: v.Revision}
	e := tx.QueryRowContext(ctx, `SELECT session_id::text,account_version,authorization_epoch,expires_at,policy_stamp FROM owned_publisher_domain_intents WHERE domain_id=$1::uuid AND actor_id=$2::uuid AND revision=$3`, v.ID, actor, v.Revision).Scan(&j.session, &j.version, &j.epoch, &j.expires, &j.stamp)
	return j, mapped(e)
}
