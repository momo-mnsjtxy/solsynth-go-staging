// SPDX-License-Identifier: AGPL-3.0-only
package postpg

import (
	"bytes"
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere/posts"
	"strings"
	"time"
)

//go:embed scheduling_schema.sql
var SchedulingSchema string

// WithScheduling enables only durable intent created by the original authority
// proof. Constructors never install schema or run due work.
func (r *Repository) WithScheduling() *Repository { r.scheduling = true; return r }
func (r *Repository) SchedulingReady(ctx context.Context) error {
	if r == nil || !r.scheduling || r.db == nil || r.authority == nil || r.visibility == nil {
		return posts.ErrUnavailable
	}
	if _, ok := r.associations.(interface{ SupportsPostRelations() }); !ok {
		return posts.ErrUnavailable
	}
	_, e := r.db.ExecContext(ctx, `SELECT id,post_id,session_id,revision,policy_stamp FROM owned_post_schedules LIMIT 0;SELECT schedule_id,kind FROM owned_post_schedule_events LIMIT 0;SELECT post_id,file_id,owner_authorized FROM owned_post_media LIMIT 0;SELECT post_id,replied_post_id FROM owned_post_relations LIMIT 0`)
	return e
}
func (r *Repository) prepareSchedule(ctx context.Context, tx *sql.Tx, actor string, p *posts.Post, v posts.Patch, now time.Time) error {
	if !r.scheduling || v.PublishedAt == nil || !v.PublishedAt.After(now) {
		return nil
	}
	if r.authority == nil {
		return posts.ErrUnavailable
	}
	proof, e := r.authority.Authorize(ctx, r.db, tx, actor, "posts.create")
	if e != nil {
		return e
	}
	if proof.Session.Type != 0 || proof.Session.ClientID != "" {
		return posts.ErrUnsupported
	}
	p.DraftedAt = &now
	p.PublishedAt = nil
	return nil
}
func scheduleAudit(ctx context.Context, tx *sql.Tx, id, revision int64, post, actor, kind string, at time.Time) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_post_schedule_events(schedule_id,revision,post_id,actor_id,kind,occurred_at)VALUES($1,$2,$3,$4,$5,$6)`, id, revision, post, actor, kind, at)
	return e
}
func postAudit(ctx context.Context, tx *sql.Tx, id string) (n int64, e error) {
	e = tx.QueryRowContext(ctx, `SELECT COALESCE(max(id),0) FROM owned_text_post_audit WHERE post_id=$1`, id).Scan(&n)
	return
}
func (r *Repository) syncSchedule(ctx context.Context, tx *sql.Tx, actor string, p *posts.Post, v posts.Patch, action string, now time.Time) error {
	if !r.scheduling {
		return nil
	}
	var priorID, revision int64
	var state string
	e := tx.QueryRowContext(ctx, `SELECT id,revision,state FROM owned_post_schedules WHERE post_id=$1 AND state IN('pending','blocked') ORDER BY id DESC LIMIT 1 FOR UPDATE`, p.ID).Scan(&priorID, &revision, &state)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	future := v.PublishedAt != nil && v.PublishedAt.After(now)
	explicit := future || v.PublishedAt != nil || v.DraftedAt != nil || action == "publish" || action == "delete"
	if priorID != 0 && explicit {
		_, e = tx.ExecContext(ctx, `UPDATE owned_post_schedules SET state='cancelled',failure=NULL,revision=revision+1,updated_at=clock_timestamp(),finished_at=clock_timestamp() WHERE id=$1`, priorID)
		if e != nil {
			return e
		}
		if e = scheduleAudit(ctx, tx, priorID, revision+1, p.ID, actor, "cancelled", now); e != nil {
			return e
		}
	}
	auditID, e := postAudit(ctx, tx, p.ID)
	if e != nil {
		return e
	}
	if future {
		proof, e := r.authority.Authorize(ctx, r.db, tx, actor, "posts.create")
		if e != nil {
			return e
		}
		if proof.Session.Type != 0 || proof.Session.ClientID != "" {
			return posts.ErrUnsupported
		}
		stamp, e := schedulePolicyStamp(ctx, tx, actor, p.PublisherID, p.ID)
		if e != nil {
			return e
		}
		var id int64
		e = tx.QueryRowContext(ctx, `INSERT INTO owned_post_schedules(post_id,actor_id,publisher_id,session_id,account_version,authorization_epoch,due_at,state,post_audit_id,policy_stamp)VALUES($1,$2,$3,$4,$5,$6,$7,'pending',$8,$9::jsonb)RETURNING id`, p.ID, actor, p.PublisherID, proof.Session.ID, proof.Account.Version, proof.Session.AuthorizationEpoch, v.PublishedAt.UTC(), auditID, stamp).Scan(&id)
		if e != nil {
			return e
		}
		if e = scheduleAudit(ctx, tx, id, 1, p.ID, actor, "scheduled", now); e != nil {
			return e
		}
	} else if priorID != 0 && !explicit && state == "pending" {
		_, e = tx.ExecContext(ctx, `UPDATE owned_post_schedules SET revision=revision+1,post_audit_id=$2,updated_at=clock_timestamp() WHERE id=$1`, priorID, auditID)
		if e != nil {
			return e
		}
		if e = scheduleAudit(ctx, tx, priorID, revision+1, p.ID, actor, "edited", now); e != nil {
			return e
		}
	}
	return r.loadSchedule(ctx, tx, actor, p)
}
func (r *Repository) loadSchedule(ctx context.Context, tx *sql.Tx, actor string, p *posts.Post) error {
	if !r.scheduling || actor == "" {
		p.Schedule = nil
		return nil
	}
	var editable bool
	if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1`+base+` AND p.id=$2 AND `+EditorPredicateWithVisibility(r.visibility)+`)`, actor, p.ID).Scan(&editable); e != nil {
		return e
	}
	if !editable {
		p.Schedule = nil
		return nil
	}
	var s posts.Schedule
	e := tx.QueryRowContext(ctx, `SELECT id,due_at,state,failure FROM owned_post_schedules WHERE post_id=$1 ORDER BY id DESC LIMIT 1`, p.ID).Scan(&s.ID, &s.DueAt, &s.State, &s.Failure)
	if errors.Is(e, sql.ErrNoRows) {
		p.Schedule = nil
		return nil
	}
	if e == nil {
		p.Schedule = &s
	}
	return e
}

// Bind relevant canonical policy records and membership incarnations, never
// bearer bytes. Clock-based grant expiry is independently evaluated at execution.
// Replacing a relevant grant or realm policy invalidates old intent even if its
// effective bits are later restored; explicit rescheduling records new intent.
func schedulePolicyStamp(ctx context.Context, tx *sql.Tx, actor, publisher, post string) ([]byte, error) {
	var b []byte
	e := tx.QueryRowContext(ctx, `WITH realm_ids AS (
 SELECT p.realm_id id FROM owned_text_posts p WHERE p.id=$3
 UNION SELECT u.realm_id FROM owned_publishers u WHERE u.id=$2
 UNION SELECT target.realm_id FROM owned_post_relations l JOIN owned_text_posts target ON target.id IN(l.replied_post_id,l.forwarded_post_id,l.chained_post_id) WHERE l.post_id=$3
 UNION SELECT u.realm_id FROM owned_post_relations l JOIN owned_text_posts target ON target.id IN(l.replied_post_id,l.forwarded_post_id,l.chained_post_id) JOIN owned_publishers u ON u.id=target.publisher_id WHERE l.post_id=$3
 ) SELECT jsonb_build_object(
 'actor',jsonb_build_object('version',a.version,'disabled',a.disabled,'superuser',a.is_superuser),
 'grants',(SELECT jsonb_agg(to_jsonb(g) ORDER BY g.id) FROM owned_authority_grants g WHERE g.account_id=a.id AND g.pattern IN('*','posts.*','posts.create')),
 'groups',(SELECT jsonb_agg(to_jsonb(g) ORDER BY g.id) FROM owned_authority_group_grants g JOIN owned_authority_group_members m ON m.group_id=g.group_id WHERE m.account_id=a.id AND g.pattern IN('*','posts.*','posts.create')),
 'punishments',(SELECT jsonb_agg(to_jsonb(p) ORDER BY p.id) FROM owned_authority_punishments p WHERE p.account_id=a.id AND(p.kind='disable_account' OR p.kind='permission_modification' AND p.pattern IN('*','posts.*','posts.create'))),
 'publisher',jsonb_build_object('id',u.id,'owner',u.account_id,'realm',u.realm_id,'deleted',u.deleted_at,'shadowban',u.shadowbanned_at),
 'owner',(SELECT jsonb_build_object('version',o.version,'disabled',o.disabled) FROM owned_authority_accounts o WHERE o.id=u.account_id),
 'membership',(SELECT to_jsonb(m) FROM owned_publisher_members m WHERE m.publisher_id=u.id AND m.account_id=a.id),
 'realms',(SELECT jsonb_agg(jsonb_build_object('id',r.id,'owner',r.account_id,'owner_version',oa.version,'owner_disabled',oa.disabled,'deleted',r.deleted_at,'member',to_jsonb(m),'role',to_jsonb(rp),'override',to_jsonb(up)) ORDER BY r.id) FROM realm_ids ids JOIN owned_realms r ON r.id=ids.id JOIN owned_authority_accounts oa ON oa.id=r.account_id LEFT JOIN owned_realm_members m ON m.realm_id=r.id AND m.account_id=a.id LEFT JOIN owned_realm_role_permissions rp ON rp.realm_id=r.id AND rp.role_level=m.role LEFT JOIN owned_realm_user_permissions up ON up.realm_id=r.id AND up.account_id=a.id),
 'post_moderation',(SELECT COALESCE(max(ma.id),0) FROM owned_content_moderation_audit ma WHERE ma.kind='post' AND ma.target_id=$3),
 'publisher_moderation',(SELECT COALESCE(max(ma.id),0) FROM owned_content_moderation_audit ma WHERE ma.target_id=u.id)
 ) FROM owned_authority_accounts a CROSS JOIN owned_publishers u WHERE a.id=$1 AND u.id=$2`, actor, publisher, post).Scan(&b)
	return b, e
}

type dueSchedule struct {
	id, revision, version, epoch, audit int64
	post, actor, publisher, session     string
	due                                 time.Time
	stamp                               []byte
}

func scanSchedule(row interface{ Scan(...any) error }) (s dueSchedule, e error) {
	e = row.Scan(&s.id, &s.revision, &s.post, &s.actor, &s.publisher, &s.session, &s.version, &s.epoch, &s.audit, &s.due, &s.stamp)
	return
}

const scheduleColumns = `id,revision,post_id::text,actor_id::text,publisher_id::text,session_id::text,account_version,authorization_epoch,post_audit_id,due_at,policy_stamp`

// ProcessScheduled advances previously authorized durable intents only. It is
// bounded and starts no goroutine, network call, notification or provider task.
func (r *Repository) ProcessScheduled(ctx context.Context, limit int) (int, error) {
	if r == nil || !r.scheduling || r.authority == nil || r.db == nil {
		return 0, posts.ErrUnavailable
	}
	if limit < 1 || limit > 1000 {
		return 0, posts.ErrInvalid
	}
	rows, e := r.db.QueryContext(ctx, `SELECT `+scheduleColumns+` FROM owned_post_schedules WHERE state='pending' AND due_at<=clock_timestamp() ORDER BY due_at,id LIMIT $1`, limit)
	if e != nil {
		return 0, e
	}
	candidates := []dueSchedule{}
	for rows.Next() {
		s, x := scanSchedule(rows)
		if x != nil {
			rows.Close()
			return 0, x
		}
		candidates = append(candidates, s)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	n := 0
	for _, s := range candidates {
		done, e := r.processSchedule(ctx, s)
		if e != nil {
			return n, e
		}
		if done {
			n++
		}
	}
	return n, nil
}
func publicationDenial(e error) bool {
	var p *posts.Failure
	var a *authority.Failure
	var account *accounts.Failure
	return errors.As(e, &p) && p.Status < 500 || errors.As(e, &a) && a.Status < 500 || errors.As(e, &account) && account.Status < 500
}
func (r *Repository) processSchedule(ctx context.Context, s dueSchedule) (bool, error) {
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	var held string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, s.actor).Scan(&held); e != nil {
		return false, e
	}
	reason := ""
	eligible, e := r.authority.StoredSessionEligible(ctx, r.db, tx, s.actor, s.session, s.version, s.epoch, "posts.create")
	if e != nil {
		return false, e
	}
	if !eligible {
		reason = "authority_revoked"
	}
	var p posts.Post
	if reason == "" {
		p, e = r.locked(ctx, tx, s.actor, s.post, posts.Patch{}, false, true)
		if e != nil {
			if !publicationDenial(e) {
				return false, e
			}
			reason = "publication_denied"
		}
	}
	if reason == "" {
		if e = r.lockPublicationMedia(ctx, tx, p.ID); e != nil {
			return false, e
		}
	}
	current, e := scanSchedule(tx.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM owned_post_schedules WHERE id=$1 AND state='pending' FOR UPDATE`, s.id))
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if current.revision != s.revision {
		return false, nil
	}
	var now time.Time
	if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
		return false, e
	}
	if s.due.After(now) {
		return false, nil
	}
	if reason == "" {
		reason, e = r.checkScheduledPost(ctx, tx, s, &p)
		if e != nil {
			return false, e
		}
	}
	if reason == "" {
		eligible, e = r.authority.StoredSessionEligible(ctx, r.db, tx, s.actor, s.session, s.version, s.epoch, "posts.create")
		if e != nil {
			return false, e
		}
		if !eligible {
			reason = "authority_revoked"
		}
	}
	state := "blocked"
	if reason == "" {
		p.DraftedAt = nil
		p.PublishedAt = &s.due
		p.UpdatedAt = now
		p.Schedule = nil
		if e = save(ctx, tx, p); e != nil {
			return false, e
		}
		if e = audit(ctx, tx, s.actor, p.ID, "publish", now); e != nil {
			return false, e
		}
		state = "published"
	}
	var failure any
	if reason != "" {
		failure = reason
	}
	_, e = tx.ExecContext(ctx, `UPDATE owned_post_schedules SET state=$2,failure=$3,finished_at=$4,updated_at=$4 WHERE id=$1 AND state='pending'`, s.id, state, failure, now)
	if e != nil {
		return false, e
	}
	if e = scheduleAudit(ctx, tx, s.id, s.revision, s.post, s.actor, state, now); e != nil {
		return false, e
	}
	return true, tx.Commit()
}

func (r *Repository) checkScheduledPost(ctx context.Context, tx *sql.Tx, s dueSchedule, p *posts.Post) (string, error) {
	if reason, e := r.preparePublication(ctx, tx, s.actor, p); reason != "" || e != nil {
		return reason, e
	}
	if p.PublisherID != s.publisher || p.DraftedAt == nil || p.PublishedAt != nil || p.DeletedAt != nil {
		return "post_changed", nil
	}
	currentAudit, e := postAudit(ctx, tx, p.ID)
	if e != nil {
		return "", e
	}
	if currentAudit != s.audit {
		return "post_changed", nil
	}
	stamp, e := schedulePolicyStamp(ctx, tx, s.actor, s.publisher, s.post)
	if e != nil {
		return "", e
	}
	if !bytes.Equal(stamp, s.stamp) {
		return "policy_changed", nil
	}
	return r.checkPublication(ctx, tx, s.actor, p)
}

// The source and relation publisher/post locks are already held. Finish every
// retained-file wait before observing current owners, policy and moderation.
func (r *Repository) lockPublicationMedia(ctx context.Context, tx *sql.Tx, post string) error {
	rows, e := tx.QueryContext(ctx, `SELECT f.id::text FROM owned_post_media l JOIN owned_media_files f ON f.id=l.file_id WHERE l.post_id=$1 ORDER BY f.id FOR SHARE OF f`, post)
	if e != nil {
		return e
	}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
	}
	e = rows.Err()
	rows.Close()
	return e
}

// Reacquire only the same planned locks, then check publisher/realm permissions
// against the fresh snapshot after the file and pending-intent waits.
func (r *Repository) preparePublication(ctx context.Context, tx *sql.Tx, actor string, p *posts.Post) (string, error) {
	if e := r.lockPublicationMedia(ctx, tx, p.ID); e != nil {
		return "", e
	}
	current, e := r.locked(ctx, tx, actor, p.ID, posts.Patch{}, false, true)
	if e != nil {
		if publicationDenial(e) {
			return "publication_denied", nil
		}
		return "", e
	}
	*p = current
	return "", nil
}

// checkPublication is shared with an editor's fresh explicit publish request.
// It never restores an old schedule's authority; recorded intent additionally
// passes its own session, revision and policy-stamp checks above.
func (r *Repository) checkPublication(ctx context.Context, tx *sql.Tx, actor string, p *posts.Post) (string, error) {
	var blocked bool
	e := tx.QueryRowContext(ctx, `SELECT p.locked_at IS NOT NULL OR p.shadowbanned_at IS NOT NULL OR u.shadowbanned_at IS NOT NULL OR EXISTS(SELECT 1 FROM owned_authority_punishments x WHERE x.account_id=u.account_id AND x.kind='disable_account' AND(x.starts_at IS NULL OR x.starts_at<=clock_timestamp())AND(x.expires_at IS NULL OR x.expires_at>clock_timestamp())) FROM owned_text_posts p JOIN owned_publishers u ON u.id=p.publisher_id WHERE p.id=$1`, p.ID).Scan(&blocked)
	if e != nil {
		return "", e
	}
	if blocked {
		return "moderated", nil
	}
	rows, e := tx.QueryContext(ctx, `SELECT target::text FROM owned_post_relations l CROSS JOIN LATERAL unnest(ARRAY[l.replied_post_id,l.forwarded_post_id,l.chained_post_id])target WHERE l.post_id=$1 AND target IS NOT NULL ORDER BY target`, p.ID)
	if e != nil {
		return "", e
	}
	refs := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return "", e
		}
		refs = append(refs, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return "", e
	}
	var now time.Time
	if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
		return "", e
	}
	for _, id := range refs {
		var hidden bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_text_posts p JOIN owned_publishers u ON u.id=p.publisher_id WHERE p.id=$1 AND(p.shadowbanned_at IS NOT NULL OR u.shadowbanned_at IS NOT NULL))`, id).Scan(&hidden); e != nil {
			return "", e
		}
		if hidden {
			return "parent_unavailable", nil
		}
		target, e := ReadWithVisibility(ctx, tx, actor, id, r.visibility)
		if e != nil {
			if publicationDenial(e) {
				return "parent_unavailable", nil
			}
			return "", e
		}
		if target.DraftedAt != nil || target.PublishedAt == nil || target.PublishedAt.After(now) || target.LockedAt != nil {
			return "parent_unavailable", nil
		}
	}
	rows, e = tx.QueryContext(ctx, `SELECT l.file_id::text,l.owner_authorized FROM owned_post_media l WHERE l.post_id=$1 ORDER BY l.file_id`, p.ID)
	if e != nil {
		return "", e
	}
	type source struct {
		id    string
		grant bool
	}
	files := []source{}
	for rows.Next() {
		var f source
		if e = rows.Scan(&f.id, &f.grant); e != nil {
			rows.Close()
			return "", e
		}
		files = append(files, f)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return "", e
	}
	if strings.TrimSpace(p.Content) == "" && len(files) == 0 {
		return "content_unavailable", nil
	}
	for _, f := range files {
		var owner, state string
		var public bool
		if e = tx.QueryRowContext(ctx, `SELECT account_id::text,state,is_public FROM owned_media_files WHERE id=$1 FOR SHARE`, f.id).Scan(&owner, &state, &public); e != nil {
			if errors.Is(e, sql.ErrNoRows) {
				return "media_unavailable", nil
			}
			return "", e
		}
		var active bool
		e = tx.QueryRowContext(ctx, `SELECT NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments x WHERE x.account_id=a.id AND x.kind='disable_account' AND(x.starts_at IS NULL OR x.starts_at<=clock_timestamp())AND(x.expires_at IS NULL OR x.expires_at>clock_timestamp())) FROM owned_authority_accounts a WHERE a.id=$1`, owner).Scan(&active)
		if e != nil {
			return "", e
		}
		if state != "ready" || !active || !public && owner != actor && !f.grant {
			return "media_unavailable", nil
		}
	}
	if len(files) > 0 {
		realmIDs := RealmIDs(p.RealmID, p.Publisher.RealmID)
		for _, id := range refs {
			target, e := ReadWithVisibility(ctx, tx, actor, id, r.visibility)
			if e != nil {
				return "", e
			}
			realmIDs = append(realmIDs, RealmIDs(target.RealmID, target.Publisher.RealmID)...)
		}
		for _, id := range realmIDs {
			if e = RequireRealmAction(ctx, tx, r.visibility, actor, id, social.CanUploadMedia); e != nil {
				if publicationDenial(e) {
					return "media_permission_revoked", nil
				}
				return "", e
			}
		}
	}
	return "", nil
}
