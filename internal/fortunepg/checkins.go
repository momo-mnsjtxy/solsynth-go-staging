// SPDX-License-Identifier: AGPL-3.0-only
package fortunepg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/fortunes"
	"solsynth.local/go-migration/ports/passport/progression"
	"strings"
	"time"
)

// EnqueueCheckIn joins the canonical progression transaction and proof. It
// verifies the persisted source row; caller-provided level/date are not trusted.
func (r *Repository) EnqueueCheckIn(ctx context.Context, tx *sql.Tx, v progression.CheckIn, now time.Time) error {
	if !authority.ValidID(v.ID) || !authority.ValidID(v.AccountID) {
		return fortunes.ErrInvalid
	}
	if _, e := r.guard.Authenticate(ctx, r.db, tx, v.AccountID); e != nil {
		return e
	}
	request := fortunes.Request{Version: 2, IdempotencyKey: "fortune:" + v.ID + ":v2", AccountID: v.AccountID, CheckInID: v.ID}
	var at time.Time
	var zone string
	e := tx.QueryRowContext(ctx, `SELECT to_char(c.day,'YYYY-MM-DD'),c.level,c.created_at,a.language,COALESCE(p.time_zone,'') FROM owned_progression_checkins c JOIN owned_authority_accounts a ON a.id=c.account_id LEFT JOIN owned_accounts_profiles p ON p.account_id=a.id WHERE c.id=$1::uuid AND c.account_id=$2::uuid AND NOT a.disabled`, v.ID, v.AccountID).Scan(&request.Day, &request.Level, &at, &request.Language, &zone)
	if e != nil {
		return mapped(e)
	}
	if len(request.Language) > 35 {
		return fortunes.ErrInvalid
	}
	request.Language = strings.ToLower(request.Language)
	loc, e := time.LoadLocation(zone)
	if e != nil || zone == "" || zone == "Local" {
		loc = time.UTC
		zone = "UTC"
	}
	request.TimeZone = zone
	request.LocalDate = at.In(loc).Format("2006-01-02")
	b, e := json.Marshal(request)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_fortune_requests(checkin_id,request_data,state,not_before,created_at,updated_at)VALUES($1::uuid,$2::jsonb,'pending',$3::timestamptz,$3::timestamptz,$3::timestamptz)ON CONFLICT(checkin_id)DO NOTHING`, v.ID, string(b), now.UTC())
	return e
}
func (r *Repository) projection(ctx context.Context, tx *sql.Tx, actor, id string) (out fortunes.Projection, e error) {
	var generation []byte
	var revision sql.NullString
	e = tx.QueryRowContext(ctx, `SELECT f.state,f.provider_revision,f.attempts,f.updated_at,f.generation FROM owned_fortune_requests f JOIN owned_progression_checkins c ON c.id=f.checkin_id JOIN owned_authority_accounts a ON a.id=c.account_id WHERE f.checkin_id=$1::uuid AND c.account_id=$2::uuid AND NOT a.disabled`, id, actor).Scan(&out.Status.State, &revision, &out.Status.Attempts, &out.Status.UpdatedAt, &generation)
	if errors.Is(e, sql.ErrNoRows) {
		out.Status.State = "unavailable"
		return out, nil
	}
	if e != nil {
		return out, e
	}
	out.Status.Provider = revision.String
	if out.Status.State == "pending" && r.cfg.Provider == nil && revision.String == "" {
		var configured bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_fortune_config WHERE id=1)`).Scan(&configured); e != nil {
			return out, e
		}
		if !configured {
			out.Status.State = "unavailable"
		}
	}
	if len(generation) > 0 {
		var v fortunes.Generation
		if e = fortunes.Decode(generation, &v); e != nil {
			return out, e
		}
		if e = fortunes.ValidateGeneration(&v); e != nil {
			return out, e
		}
		out.Generation = &v
	}
	return
}
func (r *Repository) HydrateCheckIn(ctx context.Context, tx *sql.Tx, v *progression.CheckIn) error {
	if v == nil || !authority.ValidID(v.ID) || !authority.ValidID(v.AccountID) {
		return fortunes.ErrInvalid
	}
	if _, e := r.guard.Authenticate(ctx, r.db, tx, v.AccountID); e != nil {
		return e
	}
	out, e := r.projection(ctx, tx, v.AccountID, v.ID)
	if e != nil {
		return e
	}
	v.FortuneStatus = out.Status
	if out.Generation != nil {
		v.FortuneReport = out.Generation.Report
		v.Tips = []any{}
		for _, tip := range out.Generation.Tips {
			v.Tips = append(v.Tips, tip)
		}
	}
	return nil
}
func (r *Repository) Read(ctx context.Context, actor, id string) (out fortunes.Projection, e error) {
	if !authority.ValidID(id) {
		return out, fortunes.ErrInvalid
	}
	e = r.within(ctx, actor, func(tx *sql.Tx) error {
		var exists bool
		if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_progression_checkins WHERE id=$1::uuid AND account_id=$2::uuid)`, id, actor).Scan(&exists); e != nil {
			return e
		}
		if !exists {
			return fortunes.ErrNotFound
		}
		var e error
		out, e = r.projection(ctx, tx, actor, id)
		return e
	})
	return
}

// Retry is an owner-authenticated host operation. No extra public route is
// invented. The immutable request/provider binding remains unchanged.
func (r *Repository) Retry(ctx context.Context, actor, id string) error {
	if !authority.ValidID(id) {
		return fortunes.ErrInvalid
	}
	return r.within(ctx, actor, func(tx *sql.Tx) error {
		res, e := tx.ExecContext(ctx, `UPDATE owned_fortune_requests f SET state='pending',attempts=0,not_before=$3::timestamptz,error_code=NULL,updated_at=$3::timestamptz WHERE f.checkin_id=$1::uuid AND f.state='failed' AND EXISTS(SELECT 1 FROM owned_progression_checkins c WHERE c.id=f.checkin_id AND c.account_id=$2::uuid)`, id, actor, r.cfg.Now().UTC())
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		if e == nil && n == 0 {
			return fortunes.ErrConflict
		}
		return e
	})
}
