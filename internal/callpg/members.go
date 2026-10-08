// SPDX-License-Identifier: AGPL-3.0-only
package callpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/messager/calls"
	"strings"
	"time"
	"unicode/utf8"
)

func (r *Repository) Join(ctx context.Context, actor, rid, tool string) (calls.JoinResponse, error) {
	var out calls.JoinResponse
	if !calls.ValidTool(tool) {
		return out, calls.ErrInvalid
	}
	if _, e := r.Start(ctx, actor, rid); e != nil {
		return out, e
	}
	tx, p, v, m, e := r.locked(ctx, actor, rid, "")
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	c, e := active(ctx, tx, rid)
	if e != nil {
		return out, e
	}
	now, e := dbnow(ctx, tx)
	if e != nil {
		return out, e
	}
	if e = timeout(ctx, tx, m.ID); e != nil {
		return out, e
	}
	var identity, state, session string
	var epoch, version int64
	e = tx.QueryRowContext(ctx, `SELECT identity::text,state,session_id::text,session_epoch,account_version FROM call_v1.members WHERE call_id=$1 AND member_id=$2 AND tool=$3`, c.ID, m.ID, tool).Scan(&identity, &state, &session, &epoch, &version)
	fresh := errors.Is(e, sql.ErrNoRows) || state == "left"
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return out, e
	}
	if state == "removing" || state == "admitted" && (session != p.Session.ID || epoch != p.Session.Epoch || version != p.Account.Version) {
		return out, calls.ErrConflict
	}
	if fresh && state != "left" {
		var total, own int
		e = tx.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER(WHERE account_id=$2) FROM call_v1.members WHERE call_id=$1`, c.ID, actor).Scan(&total, &own)
		if e != nil {
			return out, e
		}
		if total >= 100 || own >= 8 {
			return out, calls.ErrConflict
		}
	}
	if fresh {
		identity, e = uuid()
		if e != nil {
			return out, e
		}
	}
	expiry := now.Add(r.cfg.TokenTTL)
	if c.ExpiresAt.Before(expiry) {
		expiry = c.ExpiresAt
	}
	if p.Session.ExpiresAt.Before(expiry) {
		expiry = p.Session.ExpiresAt
	}
	if !expiry.After(now.Add(time.Second)) {
		return out, calls.ErrConflict
	}
	participants, e := r.participants(ctx, tx, c)
	if e != nil {
		return out, e
	}
	token, e := r.provider.Token(ctx, calls.Grant{Room: c.SessionID, Identity: identity, IssuedAt: now, ExpiresAt: expiry})
	if e != nil || token == "" {
		return out, calls.ErrProvider
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO call_v1.members(call_id,member_id,account_id,tool,identity,session_id,session_epoch,account_version,state,admitted_at,token_expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'admitted',$9,$10) ON CONFLICT(call_id,member_id,tool) DO UPDATE SET identity=EXCLUDED.identity,session_id=EXCLUDED.session_id,session_epoch=EXCLUDED.session_epoch,account_version=EXCLUDED.account_version,state='admitted',admitted_at=CASE WHEN call_v1.members.state='left' THEN EXCLUDED.admitted_at ELSE call_v1.members.admitted_at END,token_expires_at=EXCLUDED.token_expires_at,revoked_at=NULL`, c.ID, m.ID, actor, tool, identity, p.Session.ID, p.Session.Epoch, p.Account.Version, now, expiry)
	if e != nil {
		return out, e
	}
	if fresh {
		if e = event(ctx, tx, c, "owned.call.admitted", "", actor); e != nil {
			return out, e
		}
	}
	if _, e = r.auth(ctx, tx, actor, ""); e != nil {
		return out, e
	}
	if e = tx.Commit(); e != nil {
		return out, e
	}
	return calls.JoinResponse{Provider: r.provider.Name(), Endpoint: r.provider.Endpoint(), Token: token, CallID: c.ID, RoomName: c.SessionID, RoomTitle: v.Name, IsAdmin: v.AccountID == actor || v.Type == 1, Participants: participants, ExpiresAt: expiry}, nil
}
func (r *Repository) participants(ctx context.Context, tx *sql.Tx, c calls.Call) ([]calls.Participant, error) {
	if e := r.checkProvider(c); e != nil {
		return nil, e
	}
	op, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	items, e := r.provider.Participants(op, c.SessionID)
	if e != nil {
		return nil, calls.ErrProvider
	}
	out := make([]calls.Participant, 0, len(items))
	for _, p := range items {
		var account, state string
		var valid bool
		e = tx.QueryRowContext(ctx, `SELECT cm.account_id::text,cm.state,NOT (`+invalidAdmission+`) FROM call_v1.members cm WHERE cm.call_id=$1 AND cm.identity::text=$2`, c.ID, p.Identity).Scan(&account, &state, &valid)
		if errors.Is(e, sql.ErrNoRows) {
			p.State = "unrecognized"
			out = append(out, p)
			continue
		}
		if e != nil {
			return nil, e
		}
		p.AccountID = account
		p.State = state
		if !valid || state == "removing" || state == "left" {
			p.State = "revocation_pending"
		} else {
			m, e := member(ctx, tx, c.RoomID, account)
			if e != nil {
				return nil, e
			}
			p.Profile = &m
		}
		out = append(out, p)
	}
	return out, nil
}
func (r *Repository) Participants(ctx context.Context, actor, rid string) ([]calls.Participant, error) {
	if _, e := r.Get(ctx, actor, rid); e != nil {
		return nil, e
	}
	tx, _, _, _, e := r.locked(ctx, actor, rid, "")
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	c, e := active(ctx, tx, rid)
	if e != nil {
		return nil, e
	}
	out, e := r.participants(ctx, tx, c)
	if e != nil {
		return nil, e
	}
	return out, tx.Commit()
}
func (r *Repository) Leave(ctx context.Context, actor, rid, tool string) error {
	if e := r.configured(); e != nil {
		return e
	}
	if !calls.ValidTool(tool) {
		return calls.ErrInvalid
	}
	tx, _, _, m, e := r.locked(ctx, actor, rid, "")
	if e != nil {
		return e
	}
	defer tx.Rollback()
	c, e := ongoing(ctx, tx, rid)
	if errors.Is(e, calls.ErrNotFound) {
		return tx.Commit()
	}
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE call_v1.members SET state='removing' WHERE call_id=$1 AND member_id=$2 AND tool=$3 AND state='admitted'`, c.ID, m.ID, tool)
	if e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	return r.finishRemovals(ctx, c.ID, actor, tool)
}
func (r *Repository) Kick(ctx context.Context, actor, rid, target string, k calls.KickRequest) error {
	if e := r.configured(); e != nil {
		return e
	}
	if valid(target) != nil || k.BanDurationMinutes < 0 || k.BanDurationMinutes > 10080 || len(k.Reason) > 1024 || !utf8.ValidString(k.Reason) || strings.ContainsRune(k.Reason, 0) {
		return calls.ErrInvalid
	}
	tx, _, v, _, e := r.locked(ctx, actor, rid, "chat.call.kick")
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if (actor != v.AccountID && v.Type != 1) || target == actor || target == v.AccountID {
		return calls.ErrForbidden
	}
	targetMember, e := member(ctx, tx, rid, target)
	if e != nil {
		return e
	}
	c, e := active(ctx, tx, rid)
	if e != nil {
		return e
	}
	if k.BanDurationMinutes > 0 {
		_, e = tx.ExecContext(ctx, `INSERT INTO call_v1.timeouts(member_id,timeout_until,actor_id,reason) VALUES($1,clock_timestamp()+$2::integer*interval '1 minute',$3,$4) ON CONFLICT(member_id) DO UPDATE SET timeout_until=EXCLUDED.timeout_until,actor_id=EXCLUDED.actor_id,reason=EXCLUDED.reason WHERE call_v1.timeouts.timeout_until<=clock_timestamp()`, targetMember.ID, k.BanDurationMinutes, actor, k.Reason)
		if e != nil {
			return e
		}
	}
	result, e := tx.ExecContext(ctx, `UPDATE call_v1.members SET state=CASE WHEN state='left' THEN state ELSE 'removing' END,ban_until=(SELECT timeout_until FROM call_v1.timeouts WHERE member_id=$3) WHERE call_id=$1 AND account_id=$2`, c.ID, target, targetMember.ID)
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n == 0 {
		return calls.ErrNotFound
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	return r.finishRemovals(ctx, c.ID, target, "*")
}

// A pending row was durably committed before this runs. Failed provider effects
// leave it pending for retry. No completed event or 204 precedes remote success.
func (r *Repository) finishRemovals(ctx context.Context, cid, target, tool string) error {
	tx, e := r.begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var rid string
	e = tx.QueryRowContext(ctx, `SELECT room_id::text FROM call_v1.calls WHERE id=$1`, cid).Scan(&rid)
	if e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `SELECT 1 FROM chat_rooms WHERE id=$1 FOR UPDATE`, rid); e != nil {
		return e
	}
	c, e := scan(tx.QueryRowContext(ctx, `SELECT `+fields+` FROM call_v1.calls WHERE id=$1`, cid))
	if e != nil {
		return e
	}
	if e = r.checkProvider(c); e != nil {
		return e
	}
	rows, e := tx.QueryContext(ctx, `SELECT identity::text,account_id::text FROM call_v1.members WHERE call_id=$1 AND state='removing' AND ($2='' OR account_id::text=$2) AND ($3='*' OR tool=$3) ORDER BY identity`, cid, target, tool)
	if e != nil {
		return e
	}
	type removal struct{ identity, account string }
	list := []removal{}
	for rows.Next() {
		var item removal
		if e = rows.Scan(&item.identity, &item.account); e != nil {
			rows.Close()
			return e
		}
		list = append(list, item)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, item := range list {
		now, e := dbnow(ctx, tx)
		if e != nil {
			return e
		}
		// One second ahead covers tokens minted in the current whole JWT second.
		op, cancel := context.WithTimeout(ctx, 5*time.Second)
		e = r.provider.Remove(op, c.SessionID, item.identity, now.Add(time.Second))
		cancel()
		if e != nil {
			return calls.ErrProvider
		}
		_, e = tx.ExecContext(ctx, `UPDATE call_v1.members SET state='left',revoked_at=clock_timestamp() WHERE identity=$1`, item.identity)
		if e != nil {
			return e
		}
		if e = event(ctx, tx, c, "owned.call.member.left", "", item.account); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (r *Repository) Mute(ctx context.Context, actor, rid, target string, muted bool) error {
	if e := r.configured(); e != nil {
		return e
	}
	if valid(target) != nil {
		return calls.ErrInvalid
	}
	tx, _, v, _, e := r.locked(ctx, actor, rid, "chat.call.mute")
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if actor != v.AccountID && v.Type != 1 {
		return calls.ErrForbidden
	}
	if _, e = member(ctx, tx, rid, target); e != nil {
		return e
	}
	c, e := active(ctx, tx, rid)
	if e != nil {
		return e
	}
	items, e := r.participants(ctx, tx, c)
	if e != nil {
		return e
	}
	found := false
	for _, p := range items {
		if p.AccountID == target && p.TrackSID != "" {
			found = true
			op, cancel := context.WithTimeout(ctx, 5*time.Second)
			e = r.provider.Mute(op, c.SessionID, p.Identity, p.TrackSID, muted)
			cancel()
			if e != nil {
				return calls.ErrProvider
			}
		}
	}
	if !found {
		return calls.ErrNotFound
	}
	if e = event(ctx, tx, c, "owned.call.mute.changed", "", target); e != nil {
		return e
	}
	return tx.Commit()
}
func (r *Repository) Invite(ctx context.Context, actor, rid, target string) error {
	if e := valid(target); e != nil {
		return e
	}
	pre, _, _, _, pe := r.locked(ctx, actor, rid, "chat.call.invite")
	if pe != nil {
		return pe
	}
	if pe = pre.Commit(); pe != nil {
		return pe
	}
	if _, e := r.Start(ctx, actor, rid); e != nil {
		return e
	}
	tx, _, _, _, e := r.locked(ctx, actor, rid, "chat.call.invite")
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = member(ctx, tx, rid, target); e != nil {
		return e
	}
	c, e := active(ctx, tx, rid)
	if e != nil {
		return e
	}
	result, e := tx.ExecContext(ctx, `INSERT INTO call_v1.invites(call_id,actor_id,target_id,sent_at) VALUES($1,$2,$3,clock_timestamp()) ON CONFLICT(call_id,actor_id,target_id) DO UPDATE SET sent_at=clock_timestamp() WHERE call_v1.invites.sent_at<clock_timestamp()-interval '1 minute'`, c.ID, actor, target)
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n > 0 {
		if e = event(ctx, tx, c, "owned.call.invited", target, actor); e != nil {
			return e
		}
	}
	return tx.Commit()
}
