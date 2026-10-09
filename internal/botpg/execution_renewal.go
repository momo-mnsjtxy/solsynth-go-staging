// SPDX-License-Identifier: AGPL-3.0-only
package botpg

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/apps"
	"solsynth.local/go-migration/ports/develop/bots"
)

const maxRenewalConsent = 7 * 24 * time.Hour

type renewalRow struct {
	bots.RenewalPolicy
	generation                                         int64
	issuer, session, owner, adapter, binding           string
	version, epoch, ownerVersion, botVersion, appEpoch int64
	mlsBinding                                         string
	issuerJoined, issuerRoomJoined, botJoined, created time.Time
}

const renewalColumns = `bot_id::text,room_id::text,extend_by_seconds,renew_before_seconds,consent_expires_at,active,revoked_at,reason,updated_at,enrollment_generation,issuer_id::text,session_id::text,owner_id::text,adapter,adapter_binding,account_version,session_epoch,owner_version,bot_version,issuer_joined_at,issuer_room_joined_at,bot_joined_at,created_at,app_epoch,mls_binding`

func scanRenewal(row interface{ Scan(...any) error }) (v renewalRow, e error) {
	e = row.Scan(&v.BotID, &v.RoomID, &v.ExtendBySeconds, &v.RenewBeforeSeconds, &v.ConsentExpiresAt, &v.Active, &v.RevokedAt, &v.Reason, &v.UpdatedAt, &v.generation, &v.issuer, &v.session, &v.owner, &v.adapter, &v.binding, &v.version, &v.epoch, &v.ownerVersion, &v.botVersion, &v.issuerJoined, &v.issuerRoomJoined, &v.botJoined, &v.created, &v.appEpoch, &v.mlsBinding)
	return
}

func validRenewalRequest(p *bots.RenewalPolicyRequest) bool {
	if p == nil || p.ExtendBySeconds < 300 || p.ExtendBySeconds > 86400 || p.RenewBeforeSeconds < 60 || p.RenewBeforeSeconds > 3600 || p.RenewBeforeSeconds >= p.ExtendBySeconds || p.ConsentExpiresAt.IsZero() {
		return false
	}
	p.ConsentExpiresAt = p.ConsentExpiresAt.UTC().Truncate(time.Microsecond)
	return true
}

// RenewalPolicy records an explicit, bounded consent. The worker can preserve
// only this enrollment and only while the exact recorded session, authority,
// publisher, room, membership, adapter and bot identity remain current.
func (r *Repository) RenewalPolicy(ctx context.Context, s apps.Scope, bot, room string, p *bots.RenewalPolicyRequest, action string) (out bots.RenewalPolicy, e error) {
	if r == nil || r.execution == nil {
		return out, bots.ErrUnsupported
	}
	if !authority.ValidID(bot) || !authority.ValidID(room) {
		return out, bots.ErrInvalid
	}
	permission, role := "", 0
	switch action {
	case "read":
	case "set", "delete":
		permission, role = bots.ExecutionPermission, 50
	default:
		return out, bots.ErrInvalid
	}
	if action == "set" && !validRenewalRequest(p) || action != "set" && p != nil {
		return out, bots.ErrInvalid
	}
	e = r.within(ctx, s, bot, permission, role, func(tx *sql.Tx, scope appScope, now time.Time) error {
		row, x := loadBot(ctx, tx, s.App, bot)
		if x != nil {
			return x
		}
		if row.bot.DeletedAt != nil {
			return bots.ErrNotFound
		}
		if x = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); x != nil {
			return x
		}
		if action == "delete" {
			policy, x := scanRenewal(tx.QueryRowContext(ctx, `SELECT `+renewalColumns+` FROM owned_bot_enrollment_renewals WHERE bot_id=$1 AND room_id=$2 FOR UPDATE`, bot, room))
			if x != nil {
				return mapped(x)
			}
			out = policy.RenewalPolicy
			if policy.Active {
				reason := "explicit_revocation"
				if _, x = tx.ExecContext(ctx, `UPDATE owned_bot_enrollment_renewals SET active=false,revoked_at=$3,reason=$4,updated_at=$3 WHERE bot_id=$1 AND room_id=$2 AND active`, bot, room, now, reason); x != nil {
					return x
				}
				out.Active, out.RevokedAt, out.Reason, out.UpdatedAt = false, &now, &reason, now
			}
			return audit(ctx, tx, s, "bot.execution.renewal.revoke", bot, now)
		}
		roomState, x := r.executionRoom(ctx, tx, room, s.Actor, row.bot.Account.ID, true)
		if x != nil {
			return x
		}
		if roomState.owner != s.Actor {
			return bots.ErrForbidden
		}
		current, x := enrollment(ctx, tx, bot, room)
		if x != nil {
			return mapped(x)
		}
		if action == "read" {
			if _, x = executionMember(ctx, tx, room, s.Actor); x != nil {
				return x
			}
			policy, x := scanRenewal(tx.QueryRowContext(ctx, `SELECT `+renewalColumns+` FROM owned_bot_enrollment_renewals WHERE bot_id=$1 AND room_id=$2`, bot, room))
			if x != nil {
				return mapped(x)
			}
			out = policy.RenewalPolicy
			return nil
		}
		if !current.Active || !current.ExpiresAt.After(now) || current.issuer != s.Actor || !row.active || scope.status != 1 {
			return bots.ErrForbidden
		}
		adapter := r.execution.adapters[current.Adapter]
		if adapter == nil || adapter.BotID() != bot || adapter.BindingID() != current.binding {
			return bots.ErrUnsupported
		}
		if x = activeAccount(ctx, tx, row.bot.Account.ID, now); x != nil {
			return x
		}
		cfg, x := chat(ctx, tx, bot, row.bot.CreatedAt)
		if x != nil {
			return x
		}
		if !cfg.SupportChat || roomState.kind == 1 && !cfg.AutoApproveDM || roomState.guarded && roomState.kind == 0 && !cfg.AutoApproveGroupChat {
			return bots.ErrForbidden
		}
		issuerRoomJoined, x := executionMember(ctx, tx, room, s.Actor)
		if x != nil {
			return x
		}
		botJoined, x := executionMember(ctx, tx, room, row.bot.Account.ID)
		if x != nil {
			return x
		}
		principal, x := r.guard.Authorize(ctx, r.db, tx, s.Actor, bots.ExecutionPermission, "chat.invites.manage")
		if x != nil {
			return x
		}
		if !p.ConsentExpiresAt.After(now) || p.ConsentExpiresAt.After(now.Add(maxRenewalConsent)) || p.ConsentExpiresAt.After(principal.Session.ExpiresAt) {
			return bots.ErrInvalid
		}
		var pubOwner string
		var ownerVersion int64
		var issuerJoined time.Time
		if x = tx.QueryRowContext(ctx, `SELECT pub.account_id::text,account.version,member.joined_at FROM owned_publishers pub JOIN owned_authority_accounts account ON account.id=pub.account_id JOIN owned_publisher_members member ON member.publisher_id=pub.id AND member.account_id=$2 WHERE pub.id=$1 AND member.joined_at IS NOT NULL AND member.revoked_at IS NULL AND member.role>=50`, scope.publisher, s.Actor).Scan(&pubOwner, &ownerVersion, &issuerJoined); x != nil {
			return mapped(x)
		}
		if current.owner != pubOwner || current.ownerVersion != ownerVersion || !current.issuerJoined.Equal(issuerJoined) || !current.issuerRoomJoined.Equal(issuerRoomJoined) || current.botVersion != row.version || !current.joined.Equal(botJoined) {
			return bots.ErrConflict
		}
		mlsBinding, x := r.mlsDeviceBinding(ctx, tx, room, row.bot.Account.ID, roomState.encryption == r.execution.config.MLSEncryptionMode)
		if x != nil || current.appEpoch != scope.epoch || current.mlsBinding != mlsBinding {
			if x != nil {
				return x
			}
			return bots.ErrConflict
		}
		created := now
		var existingCreated time.Time
		if x = tx.QueryRowContext(ctx, `SELECT created_at FROM owned_bot_enrollment_renewals WHERE bot_id=$1 AND room_id=$2`, bot, room).Scan(&existingCreated); x == nil {
			created = existingCreated
		} else if !errors.Is(x, sql.ErrNoRows) {
			return x
		}
		_, x = tx.ExecContext(ctx, `INSERT INTO owned_bot_enrollment_renewals(bot_id,room_id,enrollment_generation,extend_by_seconds,renew_before_seconds,consent_expires_at,active,issuer_id,session_id,account_version,session_epoch,owner_id,owner_version,issuer_joined_at,issuer_room_joined_at,bot_version,bot_joined_at,adapter,adapter_binding,created_at,updated_at,app_epoch,mls_binding)VALUES($1,$2,$3,$4,$5,$6,true,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22) ON CONFLICT(bot_id,room_id)DO UPDATE SET enrollment_generation=EXCLUDED.enrollment_generation,extend_by_seconds=EXCLUDED.extend_by_seconds,renew_before_seconds=EXCLUDED.renew_before_seconds,consent_expires_at=EXCLUDED.consent_expires_at,active=true,issuer_id=EXCLUDED.issuer_id,session_id=EXCLUDED.session_id,account_version=EXCLUDED.account_version,session_epoch=EXCLUDED.session_epoch,owner_id=EXCLUDED.owner_id,owner_version=EXCLUDED.owner_version,issuer_joined_at=EXCLUDED.issuer_joined_at,issuer_room_joined_at=EXCLUDED.issuer_room_joined_at,bot_version=EXCLUDED.bot_version,bot_joined_at=EXCLUDED.bot_joined_at,adapter=EXCLUDED.adapter,adapter_binding=EXCLUDED.adapter_binding,created_at=EXCLUDED.created_at,updated_at=EXCLUDED.updated_at,revoked_at=NULL,reason=NULL,app_epoch=EXCLUDED.app_epoch,mls_binding=EXCLUDED.mls_binding`, bot, room, current.Generation, p.ExtendBySeconds, p.RenewBeforeSeconds, p.ConsentExpiresAt, s.Actor, principal.Session.ID, principal.Account.Version, principal.Session.AuthorizationEpoch, pubOwner, ownerVersion, issuerJoined, issuerRoomJoined, row.version, botJoined, current.Adapter, current.binding, created, now, scope.epoch, mlsBinding)
		if x != nil {
			return x
		}
		out = bots.RenewalPolicy{BotID: bot, RoomID: room, ExtendBySeconds: p.ExtendBySeconds, RenewBeforeSeconds: p.RenewBeforeSeconds, ConsentExpiresAt: p.ConsentExpiresAt, Active: true, UpdatedAt: now}
		return audit(ctx, tx, s, "bot.execution.renewal.enable", bot, now)
	})
	return out, e
}

func (r *Repository) revokeRenewal(ctx context.Context, tx *sql.Tx, p renewalRow, app, reason string, now time.Time) error {
	result, e := tx.ExecContext(ctx, `UPDATE owned_bot_enrollment_renewals SET active=false,revoked_at=$4,reason=$5,updated_at=$4 WHERE bot_id=$1 AND room_id=$2 AND enrollment_generation=$3 AND active`, p.BotID, p.RoomID, p.generation, now, reason)
	if e != nil {
		return e
	}
	if n, e := result.RowsAffected(); e != nil || n != 1 {
		if e != nil {
			return e
		}
		return bots.ErrConflict
	}
	return audit(ctx, tx, apps.Scope{Actor: p.issuer, App: app}, "bot.execution.renewal.stop", p.BotID, now)
}

// processEnrollmentRenewal performs one durable reconciliation. Invalid or
// expired recorded authority permanently revokes the consent; a later account,
// session, membership or MLS-device restoration cannot revive it.
func (r *Repository) processEnrollmentRenewal(ctx context.Context, bot, room string) (bool, error) {
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	policy, e := scanRenewal(tx.QueryRowContext(ctx, `SELECT `+renewalColumns+` FROM owned_bot_enrollment_renewals WHERE bot_id=$1 AND room_id=$2`, bot, room))
	if e != nil || !policy.Active {
		return false, e
	}
	current, e := enrollment(ctx, tx, bot, room)
	if e != nil {
		return false, e
	}
	observed, e := observeExecution(ctx, tx, bot)
	if e != nil {
		return false, e
	}
	accountIDs := []string{policy.issuer, policy.owner, observed.account}
	if observed.owner != "" {
		accountIDs = append(accountIDs, observed.owner)
	}
	if e = lockExecutionAccounts(ctx, tx, true, accountIDs...); e != nil {
		return false, e
	}
	var locked string
	for _, item := range []struct {
		query        string
		args         []any
		allowMissing bool
	}{
		{`SELECT id::text FROM owned_publishers WHERE id=$1 FOR UPDATE NOWAIT`, []any{observed.publisher}, false},
		{`SELECT id::text FROM owned_publisher_members WHERE publisher_id=$1 AND account_id=$2 FOR UPDATE NOWAIT`, []any{observed.publisher, policy.issuer}, true},
		{`SELECT id::text FROM owned_dev_projects WHERE id=$1 FOR UPDATE NOWAIT`, []any{observed.project}, false},
		{`SELECT id::text FROM owned_custom_apps WHERE id=$1 FOR UPDATE NOWAIT`, []any{observed.app}, false},
		{`SELECT id::text FROM owned_bots WHERE id=$1 FOR UPDATE NOWAIT`, []any{bot}, false},
		{`SELECT id::text FROM chat_rooms WHERE id=$1 FOR UPDATE NOWAIT`, []any{room}, false},
		{`SELECT bot_id::text FROM owned_bot_enrollments WHERE bot_id=$1 AND room_id=$2 FOR UPDATE NOWAIT`, []any{bot, room}, false},
		{`SELECT bot_id::text FROM owned_bot_enrollment_renewals WHERE bot_id=$1 AND room_id=$2 FOR UPDATE NOWAIT`, []any{bot, room}, false},
	} {
		if e = tx.QueryRowContext(ctx, item.query, item.args...).Scan(&locked); e != nil {
			if item.allowMissing && errors.Is(e, sql.ErrNoRows) {
				continue
			}
			return false, e
		}
	}
	policy, e = scanRenewal(tx.QueryRowContext(ctx, `SELECT `+renewalColumns+` FROM owned_bot_enrollment_renewals WHERE bot_id=$1 AND room_id=$2`, bot, room))
	if e != nil || !policy.Active {
		return false, e
	}
	current, e = enrollment(ctx, tx, bot, room)
	if e != nil {
		return false, e
	}
	fresh, e := observeExecution(ctx, tx, bot)
	if e != nil {
		return false, e
	}
	var now time.Time
	if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
		return false, e
	}
	stop := func(reason string) (bool, error) {
		if e := r.revokeRenewal(ctx, tx, policy, fresh.app, reason, now); e != nil {
			return false, e
		}
		return true, tx.Commit()
	}
	if observed != fresh || !current.Active || current.Generation != policy.generation || policy.appEpoch != fresh.appEpoch || current.appEpoch != fresh.appEpoch || policy.mlsBinding != current.mlsBinding {
		return stop("enrollment_or_ancestry_changed")
	}
	if !current.ExpiresAt.After(now) || !policy.ConsentExpiresAt.After(now) {
		return stop("enrollment_or_consent_expired")
	}
	// Candidate discovery is intentionally unlocked. Recheck the due boundary
	// after all canonical locks so concurrent workers cannot extend the same
	// consent twice from slightly different database timestamps.
	if current.ExpiresAt.After(now.Add(time.Duration(policy.RenewBeforeSeconds) * time.Second)) {
		return false, nil
	}
	if current.issuer != policy.issuer || current.owner != policy.owner || current.Adapter != policy.adapter || current.binding != policy.binding || fresh.owner != policy.owner || policy.ownerVersion != current.ownerVersion || policy.botVersion != current.botVersion {
		return stop("enrollment_or_ancestry_changed")
	}
	adapter := r.execution.adapters[policy.adapter]
	if adapter == nil || adapter.BotID() != bot || adapter.BindingID() != policy.binding {
		return stop("adapter_binding_changed")
	}
	row, e := loadBot(ctx, tx, fresh.app, bot)
	if errors.Is(e, bots.ErrNotFound) || e == nil && (!row.active || row.bot.DeletedAt != nil || row.version != policy.botVersion) {
		return stop("bot_revoked")
	}
	if e != nil {
		return false, e
	}
	var status int
	var appEpoch int64
	var pubOwner string
	var ownerVersion int64
	var issuerJoined time.Time
	e = tx.QueryRowContext(ctx, `SELECT a.status,a.oauth_epoch,pub.account_id::text,owner.version,member.joined_at`+ancestry+` JOIN owned_authority_accounts owner ON owner.id=pub.account_id JOIN owned_publisher_members member ON member.publisher_id=pub.id AND member.account_id=$2 WHERE a.id=$1 AND `+liveAncestry+` AND member.joined_at IS NOT NULL AND member.revoked_at IS NULL AND member.role>=50`, fresh.app, policy.issuer).Scan(&status, &appEpoch, &pubOwner, &ownerVersion, &issuerJoined)
	if errors.Is(e, sql.ErrNoRows) || e == nil && (status != 1 || appEpoch != policy.appEpoch || pubOwner != policy.owner || ownerVersion != policy.ownerVersion || !issuerJoined.Equal(policy.issuerJoined)) {
		return stop("publisher_authority_changed")
	}
	if e != nil {
		return false, e
	}
	for _, id := range []string{policy.issuer, policy.owner, fresh.account} {
		if e = activeAccount(ctx, tx, id, now); errors.Is(e, bots.ErrForbidden) || errors.Is(e, bots.ErrNotFound) {
			return stop("account_revoked")
		} else if e != nil {
			return false, e
		}
	}
	roomState, e := r.executionRoom(ctx, tx, room, policy.issuer, fresh.account, false)
	if errors.Is(e, bots.ErrForbidden) || errors.Is(e, bots.ErrNotFound) || errors.Is(e, bots.ErrUnsupported) || e == nil && roomState.owner != policy.issuer {
		return stop("room_or_mls_membership_changed")
	}
	if e != nil {
		return false, e
	}
	mlsBinding, e := r.mlsDeviceBinding(ctx, tx, room, fresh.account, roomState.encryption == r.execution.config.MLSEncryptionMode)
	if errors.Is(e, bots.ErrForbidden) || e == nil && mlsBinding != policy.mlsBinding {
		return stop("room_or_mls_membership_changed")
	}
	if e != nil {
		return false, e
	}
	cfg, e := chat(ctx, tx, bot, row.bot.CreatedAt)
	if e != nil {
		return false, e
	}
	if !cfg.SupportChat || roomState.kind == 1 && !cfg.AutoApproveDM || roomState.guarded && roomState.kind == 0 && !cfg.AutoApproveGroupChat {
		return stop("chat_policy_changed")
	}
	issuerRoomJoined, e := executionMember(ctx, tx, room, policy.issuer)
	if errors.Is(e, bots.ErrForbidden) || e == nil && !issuerRoomJoined.Equal(policy.issuerRoomJoined) {
		return stop("issuer_room_membership_changed")
	}
	if e != nil {
		return false, e
	}
	botJoined, e := executionMember(ctx, tx, room, fresh.account)
	if errors.Is(e, bots.ErrForbidden) || e == nil && !botJoined.Equal(policy.botJoined) {
		return stop("bot_room_membership_changed")
	}
	if e != nil {
		return false, e
	}
	alive, e := r.execution.recorded.StoredSessionEligible(ctx, r.db, tx, policy.issuer, policy.session, policy.version, policy.epoch, bots.ExecutionPermission, "chat.invites.manage")
	if e != nil {
		return false, e
	}
	if !alive {
		return stop("issuer_authority_revoked")
	}
	var sessionExpiry time.Time
	if e = tx.QueryRowContext(ctx, `SELECT expires_at FROM owned_authority_sessions WHERE id=$1 AND account_id=$2`, policy.session, policy.issuer).Scan(&sessionExpiry); e != nil {
		return false, e
	}
	if policy.ConsentExpiresAt.After(sessionExpiry) {
		return stop("session_expiry_changed")
	}
	// Reobserve time-based punishments and authority at the mutation boundary.
	if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
		return false, e
	}
	if !policy.ConsentExpiresAt.After(now) || !current.ExpiresAt.After(now) {
		return stop("enrollment_or_consent_expired")
	}
	for _, id := range []string{policy.issuer, policy.owner, fresh.account} {
		if e = activeAccount(ctx, tx, id, now); errors.Is(e, bots.ErrForbidden) || errors.Is(e, bots.ErrNotFound) {
			return stop("account_revoked")
		} else if e != nil {
			return false, e
		}
	}
	alive, e = r.execution.recorded.StoredSessionEligible(ctx, r.db, tx, policy.issuer, policy.session, policy.version, policy.epoch, bots.ExecutionPermission, "chat.invites.manage")
	if e != nil {
		return false, e
	}
	if !alive {
		return stop("issuer_authority_revoked")
	}
	roomState, e = r.executionRoom(ctx, tx, room, policy.issuer, fresh.account, false)
	if errors.Is(e, bots.ErrForbidden) || errors.Is(e, bots.ErrNotFound) || errors.Is(e, bots.ErrUnsupported) || e == nil && roomState.owner != policy.issuer {
		return stop("room_or_mls_membership_changed")
	}
	if e != nil {
		return false, e
	}
	mlsBinding, e = r.mlsDeviceBinding(ctx, tx, room, fresh.account, roomState.encryption == r.execution.config.MLSEncryptionMode)
	if errors.Is(e, bots.ErrForbidden) || e == nil && mlsBinding != policy.mlsBinding {
		return stop("room_or_mls_membership_changed")
	}
	if e != nil {
		return false, e
	}
	issuerRoomJoined, e = executionMember(ctx, tx, room, policy.issuer)
	if errors.Is(e, bots.ErrForbidden) || e == nil && !issuerRoomJoined.Equal(policy.issuerRoomJoined) {
		return stop("issuer_room_membership_changed")
	}
	if e != nil {
		return false, e
	}
	botJoined, e = executionMember(ctx, tx, room, fresh.account)
	if errors.Is(e, bots.ErrForbidden) || e == nil && !botJoined.Equal(policy.botJoined) {
		return stop("bot_room_membership_changed")
	}
	if e != nil {
		return false, e
	}
	target := now.Add(time.Duration(policy.ExtendBySeconds) * time.Second)
	if policy.ConsentExpiresAt.Before(target) {
		target = policy.ConsentExpiresAt
	}
	if sessionExpiry.Before(target) {
		target = sessionExpiry
	}
	if !target.After(current.ExpiresAt) {
		return false, nil
	}
	result, e := tx.ExecContext(ctx, `UPDATE owned_bot_enrollments SET expires_at=$4,updated_at=$5,issuer_id=$6,session_id=$7,account_version=$8,session_epoch=$9,owner_id=$10,owner_version=$11,issuer_joined_at=$12,issuer_room_joined_at=$13,bot_version=$14,joined_at=$15 WHERE bot_id=$1 AND room_id=$2 AND generation=$3 AND active AND expires_at>$5`, bot, room, current.Generation, target, now, policy.issuer, policy.session, policy.version, policy.epoch, policy.owner, policy.ownerVersion, policy.issuerJoined, policy.issuerRoomJoined, policy.botVersion, policy.botJoined)
	if e != nil {
		return false, e
	}
	if n, e := result.RowsAffected(); e != nil || n != 1 {
		if e != nil {
			return false, e
		}
		return false, bots.ErrConflict
	}
	if e = audit(ctx, tx, apps.Scope{Actor: policy.issuer, App: fresh.app}, "bot.execution.renewal.apply", bot, now); e != nil {
		return false, e
	}
	return true, tx.Commit()
}

// ProcessEnrollmentRenewalsDue renews or permanently closes bounded consents.
// It never creates a consent and never extends its absolute deadline.
func (r *Repository) ProcessEnrollmentRenewalsDue(ctx context.Context, limit int) (int, error) {
	if r == nil || r.execution == nil {
		return 0, bots.ErrUnsupported
	}
	if limit < 1 || limit > 100 {
		return 0, bots.ErrInvalid
	}
	rows, e := r.db.QueryContext(ctx, `SELECT policy.bot_id::text,policy.room_id::text FROM owned_bot_enrollment_renewals policy JOIN owned_bot_enrollments enroll USING(bot_id,room_id) WHERE policy.active AND(policy.consent_expires_at<=clock_timestamp() OR enroll.expires_at<=clock_timestamp()+policy.renew_before_seconds*interval '1 second') ORDER BY LEAST(policy.consent_expires_at,enroll.expires_at),policy.bot_id,policy.room_id LIMIT $1`, limit)
	if e != nil {
		return 0, e
	}
	type key struct{ bot, room string }
	keys := []key{}
	for rows.Next() {
		var v key
		if e = rows.Scan(&v.bot, &v.room); e != nil {
			rows.Close()
			return 0, e
		}
		keys = append(keys, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	processed := 0
	var result error
	for _, key := range keys {
		if ctx.Err() != nil {
			return processed, errors.Join(result, ctx.Err())
		}
		ok, x := r.processEnrollmentRenewal(ctx, key.bot, key.room)
		var pg *pgconn.PgError
		if errors.As(x, &pg) && pg.Code == "55P03" {
			continue
		}
		if ok && x == nil {
			processed++
		}
		result = errors.Join(result, x)
	}
	return processed, result
}
