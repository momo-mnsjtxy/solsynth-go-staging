// SPDX-License-Identifier: AGPL-3.0-only
package fortunepg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/passport/fortunes"
	"time"
)

type candidate struct{ id, actor, revision string }
type claim struct {
	candidate
	token    string
	request  fortunes.Request
	provider fortunes.Provider
	attempt  int
}

func active(ctx context.Context, tx *sql.Tx, actor string, now time.Time) (bool, error) {
	var enabled bool
	e := tx.QueryRowContext(ctx, `SELECT NOT disabled FROM owned_authority_accounts WHERE id=$1::uuid FOR UPDATE`, actor).Scan(&enabled)
	if e != nil || !enabled {
		return false, e
	}
	var punished bool
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_authority_punishments WHERE account_id=$1::uuid AND kind='disable_account' AND(starts_at IS NULL OR starts_at<=$2::timestamptz)AND(expires_at IS NULL OR expires_at>$2::timestamptz))`, actor, now).Scan(&punished)
	return !punished, e
}
func (r *Repository) claim(ctx context.Context, item candidate) (out claim, ok bool, e error) {
	out.candidate = item
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return out, false, e
	}
	defer tx.Rollback()
	now := r.cfg.Now().UTC()
	enabled, e := active(ctx, tx, item.actor, now)
	if e != nil {
		return out, false, e
	}
	var raw []byte
	var revision sql.NullString
	e = tx.QueryRowContext(ctx, `SELECT request_data,provider_revision,attempts FROM owned_fortune_requests WHERE checkin_id=$1::uuid AND((state='pending' AND not_before<=$2::timestamptz)OR(state='processing' AND lease_until<=$2::timestamptz)) FOR UPDATE SKIP LOCKED`, item.id, now).Scan(&raw, &revision, &out.attempt)
	if errors.Is(e, sql.ErrNoRows) {
		return out, false, nil
	}
	if e != nil {
		return out, false, e
	}
	if !enabled {
		_, e = tx.ExecContext(ctx, `UPDATE owned_fortune_requests SET state='cancelled',lease_id=NULL,lease_until=NULL,error_code='account_unavailable',updated_at=$2::timestamptz WHERE checkin_id=$1::uuid`, item.id, now)
		if e != nil {
			return out, false, e
		}
		return out, false, tx.Commit()
	}
	if out.attempt >= r.cfg.MaxAttempts {
		_, e = tx.ExecContext(ctx, `UPDATE owned_fortune_requests SET state='failed',lease_id=NULL,lease_until=NULL,error_code='provider_failed',updated_at=$2::timestamptz WHERE checkin_id=$1::uuid`, item.id, now)
		if e != nil {
			return out, false, e
		}
		return out, false, tx.Commit()
	}
	if e = fortunes.Decode(raw, &out.request); e != nil {
		return out, false, e
	}
	if out.request.AccountID != item.actor || out.request.CheckInID != item.id {
		return out, false, fortunes.ErrInvalid
	}
	if e = fortunes.ValidateRequest(out.request); e != nil {
		return out, false, e
	}
	// Resolve immutable configuration before leasing. The provider is called only
	// after commit, never while account/domain locks are held.
	out.provider, e = r.provider(ctx, tx, revision.String)
	if e != nil {
		if revision.Valid && errors.Is(e, fortunes.ErrUnavailable) {
			_, e = tx.ExecContext(ctx, `UPDATE owned_fortune_requests SET state='failed',lease_id=NULL,lease_until=NULL,error_code='provider_changed',updated_at=$2::timestamptz WHERE checkin_id=$1::uuid`, item.id, now)
			if e != nil {
				return out, false, e
			}
			return out, false, tx.Commit()
		}
		return out, false, e
	}
	out.revision = out.provider.Revision()
	out.token, e = newID()
	if e != nil {
		return out, false, e
	}
	out.attempt++
	_, e = tx.ExecContext(ctx, `UPDATE owned_fortune_requests SET state='processing',provider_revision=$2::text,attempts=$3::integer,lease_id=$4::uuid,lease_until=$5::timestamptz,updated_at=$6::timestamptz WHERE checkin_id=$1::uuid`, item.id, out.revision, out.attempt, out.token, now.Add(r.cfg.Timeout+30*time.Second), now)
	if e != nil {
		return out, false, e
	}
	e = tx.Commit()
	return out, e == nil, e
}
func (r *Repository) finish(ctx context.Context, c claim, g fortunes.Generation, generationErr error) error {
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	now := r.cfg.Now().UTC()
	enabled, e := active(ctx, tx, c.actor, now)
	if e != nil {
		return e
	}
	var current string
	e = tx.QueryRowContext(ctx, `SELECT COALESCE(lease_id::text,'') FROM owned_fortune_requests WHERE checkin_id=$1::uuid AND state='processing' FOR UPDATE`, c.id).Scan(&current)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	if current != c.token {
		return nil
	}
	state, code := "pending", "provider_failed"
	var data any
	var completed any
	if !enabled {
		state, code = "cancelled", "account_unavailable"
	} else if generationErr == nil {
		if e = fortunes.ValidateGeneration(&g); e != nil {
			generationErr = e
			code = "invalid_response"
		} else {
			b, e := json.Marshal(g)
			if e != nil {
				return e
			}
			data = string(b)
			completed = now
			state, code = "ready", ""
		}
	}
	if state == "pending" && c.attempt >= r.cfg.MaxAttempts {
		state = "failed"
	}
	var errorCode any
	if code != "" {
		errorCode = code
	}
	delay := time.Duration(min(3600, 30*(1<<min(c.attempt-1, 7)))) * time.Second
	_, e = tx.ExecContext(ctx, `UPDATE owned_fortune_requests SET state=$2::text,generation=$3::jsonb,completed_at=$4::timestamptz,error_code=$5::text,lease_id=NULL,lease_until=NULL,not_before=$6::timestamptz,updated_at=$7::timestamptz WHERE checkin_id=$1::uuid AND lease_id=$8::uuid`, c.id, state, data, completed, errorCode, now.Add(delay), now, c.token)
	if e != nil {
		return e
	}
	return tx.Commit()
}

// ProcessOne is a bounded host worker step. It neither starts a goroutine nor
// contacts a provider unless explicitly invoked with configured dependencies.
func (r *Repository) ProcessOne(ctx context.Context) (bool, error) {
	now := r.cfg.Now().UTC()
	rows, e := r.db.QueryContext(ctx, `SELECT f.checkin_id::text,c.account_id::text FROM owned_fortune_requests f JOIN owned_progression_checkins c ON c.id=f.checkin_id WHERE(f.state='pending' AND f.not_before<=$1::timestamptz)OR(f.state='processing' AND f.lease_until<=$1::timestamptz)ORDER BY f.not_before,f.created_at,f.checkin_id LIMIT 32`, now)
	if e != nil {
		return false, e
	}
	list := []candidate{}
	for rows.Next() {
		var v candidate
		if e = rows.Scan(&v.id, &v.actor); e != nil {
			rows.Close()
			return false, e
		}
		list = append(list, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return false, e
	}
	for _, item := range list {
		c, ok, e := r.claim(ctx, item)
		if e != nil {
			return false, e
		}
		if !ok {
			continue
		}
		providerCtx, cancel := context.WithTimeout(ctx, r.cfg.Timeout)
		g, providerErr := c.provider.Generate(providerCtx, c.request)
		cancel()
		finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		e = r.finish(finishCtx, c, g, providerErr)
		finishCancel()
		return true, e
	}
	return false, nil
}
