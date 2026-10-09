// SPDX-License-Identifier: AGPL-3.0-only
package passkitpg

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/passport/passkit"
)

//go:embed schema.sql
var Schema string

type Config struct {
	Generator    *passkit.Generator
	Media        media.ObjectStore
	ProgramGroup string
}
type Repository struct {
	db    *sql.DB
	guard *authoritypg.TransactionGuard
	cfg   Config
}

func New(db *sql.DB, guard *authoritypg.TransactionGuard, c Config) (*Repository, error) {
	if db == nil || guard == nil || c.Generator == nil || len(c.ProgramGroup) > 256 {
		return nil, passkit.ErrUnavailable
	}
	return &Repository{db, guard, c}, nil
}
func (r *Repository) Ready(ctx context.Context) error {
	_, e := r.db.ExecContext(ctx, `SELECT account_id,pass_type,serial_number,authentication_token,fingerprint,update_tag,updated_at FROM owned_apple_passes LIMIT 0; SELECT device_id,push_token FROM owned_apple_pass_registrations LIMIT 0; SELECT last_value FROM owned_apple_pass_updates`)
	return e
}

type record struct {
	actor, token, fingerprint string
	tag                       int64
	updated                   time.Time
}

func active(ctx context.Context, tx *sql.Tx, actor string) error {
	var ok bool
	e := tx.QueryRowContext(ctx, `SELECT NOT disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=clock_timestamp())AND(p.expires_at IS NULL OR p.expires_at>clock_timestamp())) FROM owned_authority_accounts a WHERE a.id=$1::uuid`, actor).Scan(&ok)
	if errors.Is(e, sql.ErrNoRows) || e == nil && !ok {
		return passkit.ErrUnauthorized
	}
	return e
}
func (r *Repository) callback(ctx context.Context, typ, serial, token string) (*sql.Tx, record, error) {
	var out record
	if typ != r.cfg.Generator.PassType() || !authority.ValidID(serial) || len(token) != 48 {
		return nil, out, passkit.ErrUnauthorized
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return nil, out, e
	}
	fail := func(e error) (*sql.Tx, record, error) { tx.Rollback(); return nil, record{}, e }
	// Canonical identity always precedes pass/registration/file locks. Serial is
	// the account UUID by schema invariant, not caller-granted account authority.
	var id string
	e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1::uuid FOR UPDATE`, serial).Scan(&id)
	if errors.Is(e, sql.ErrNoRows) {
		return fail(passkit.ErrUnauthorized)
	}
	if e != nil {
		return fail(e)
	}
	if e = active(ctx, tx, serial); e != nil {
		return fail(e)
	}
	e = tx.QueryRowContext(ctx, `SELECT account_id::text,authentication_token,fingerprint,update_tag,updated_at FROM owned_apple_passes WHERE pass_type=$1::text AND serial_number=$2::uuid FOR UPDATE`, typ, serial).Scan(&out.actor, &out.token, &out.fingerprint, &out.tag, &out.updated)
	if errors.Is(e, sql.ErrNoRows) {
		return fail(passkit.ErrUnauthorized)
	}
	if e != nil {
		return fail(e)
	}
	if subtle.ConstantTimeCompare([]byte(out.token), []byte(token)) != 1 {
		return fail(passkit.ErrUnauthorized)
	}
	return tx, out, nil
}
func (r *Repository) snapshot(ctx context.Context, tx *sql.Tx, actor string) (passkit.Member, error) {
	var m passkit.Member
	e := tx.QueryRowContext(ctx, `SELECT a.id::text,a.name,a.nick,a.created_at,COALESCE(p.first_name,''),COALESCE(p.middle_name,''),COALESCE(p.last_name,'') FROM owned_authority_accounts a LEFT JOIN owned_accounts_profiles p ON p.account_id=a.id WHERE a.id=$1::uuid`, actor).Scan(&m.ID, &m.Name, &m.Nick, &m.CreatedAt, &m.FirstName, &m.MiddleName, &m.LastName)
	if e != nil {
		return m, e
	}

	if r.cfg.Media != nil {
		var id, hash string
		var size int64
		e = tx.QueryRowContext(ctx, `SELECT f.id::text,f.sha256,f.size FROM owned_account_profile_media p JOIN owned_media_files f ON f.id=p.file_id WHERE p.account_id=$1::uuid AND p.slot='picture' AND f.account_id=$1::uuid AND f.state='ready' AND f.mime_type IN('image/png','image/jpeg','image/gif') FOR SHARE OF f`, actor).Scan(&id, &hash, &size)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return m, e
		}
		if e == nil && size > 0 && size <= 4<<20 {
			body, x := r.cfg.Media.Open(ctx, id)
			if x == nil {
				b, x := io.ReadAll(io.LimitReader(body, size+1))
				body.Close()
				h := sha256.Sum256(b)
				if x == nil && int64(len(b)) == size && hex.EncodeToString(h[:]) == hash {
					m.Picture = b
				}
			}
		}
	}
	if r.cfg.ProgramGroup != "" {
		// Only first-party membership definitions are supported here. Canonical
		// app-product eligibility acquires additional ancestry/product/consent
		// locks and cannot be appended after these pass/media locks. Exclude that
		// lane rather than presenting a label after its consent is withdrawn.
		// Internal eligibility follows the canonical status/testing/time policy;
		// the definition owns group identity, and current cycles use begun_at/id.
		e = tx.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(s.document->>'display_name',''),s.identifier) FROM commerce_v1.subscriptions s JOIN commerce_v1.definitions d ON d.identifier=s.identifier WHERE s.account_id=$1::uuid AND s.status=1 AND s.begun_at<=clock_timestamp() AND s.ended_at>clock_timestamp() AND COALESCE(NULLIF(d.document->>'group_identifier',''),d.identifier)=$2::text AND s.document->>'is_testing' IS DISTINCT FROM 'true' AND s.document->>'app_identifier' IS NULL AND s.identifier NOT LIKE 'app-product:%' AND d.document->>'app_identifier' IS NULL AND NULLIF(d.document->>'product_id','') IS NULL ORDER BY s.begun_at DESC,s.id DESC LIMIT 1`, actor, r.cfg.ProgramGroup).Scan(&m.Program)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return m, e
		}
	}
	return m, nil
}
func (r *Repository) refresh(ctx context.Context, tx *sql.Tx, rec record) (record, map[string][]byte, error) {
	m, e := r.snapshot(ctx, tx, rec.actor)
	if e != nil {
		return rec, nil, e
	}
	files, fingerprint, e := r.cfg.Generator.Files(m, rec.token)
	if e != nil {
		return rec, nil, e
	}
	if fingerprint != rec.fingerprint {
		e = tx.QueryRowContext(ctx, `INSERT INTO owned_apple_passes(account_id,pass_type,serial_number,authentication_token,fingerprint,update_tag,updated_at) VALUES($1::uuid,$2::text,$1::uuid,$3::text,$4::text,nextval('owned_apple_pass_updates'),clock_timestamp()) ON CONFLICT(pass_type,serial_number)DO UPDATE SET fingerprint=EXCLUDED.fingerprint,update_tag=EXCLUDED.update_tag,updated_at=EXCLUDED.updated_at RETURNING update_tag,updated_at`, rec.actor, r.cfg.Generator.PassType(), rec.token, fingerprint).Scan(&rec.tag, &rec.updated)
		if e != nil {
			return rec, nil, e
		}
		rec.fingerprint = fingerprint
	}
	return rec, files, nil
}
func (r *Repository) unchanged(ctx context.Context, tx *sql.Tx, rec record) error {
	m, e := r.snapshot(ctx, tx, rec.actor)
	if e != nil {
		return e
	}
	_, fingerprint, e := r.cfg.Generator.Files(m, rec.token)
	if e != nil {
		return e
	}
	if fingerprint != rec.fingerprint {
		return passkit.ErrUnavailable
	}
	return nil
}

func (r *Repository) requireProvisioning(ctx context.Context, tx *sql.Tx, actor string) error {
	p, e := r.guard.Authorize(ctx, r.db, tx, actor, passkit.ProvisionPermission)
	if e != nil {
		return e
	}
	return passkit.RequireProvisioningPrincipal(p)
}

func (r *Repository) Member(ctx context.Context, actor string) (passkit.Bundle, error) {
	var out passkit.Bundle
	if !authority.ValidID(actor) {
		return out, passkit.ErrUnauthorized
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	if e = r.requireProvisioning(ctx, tx, actor); e != nil {
		return out, e
	}
	rec := record{actor: actor}
	e = tx.QueryRowContext(ctx, `SELECT authentication_token,fingerprint,update_tag,updated_at FROM owned_apple_passes WHERE account_id=$1::uuid AND pass_type=$2::text FOR UPDATE`, actor, r.cfg.Generator.PassType()).Scan(&rec.token, &rec.fingerprint, &rec.tag, &rec.updated)
	if errors.Is(e, sql.ErrNoRows) {
		var token [24]byte
		if _, e = rand.Read(token[:]); e != nil {
			return out, e
		}
		rec.token = hex.EncodeToString(token[:])
	} else if e != nil {
		return out, e
	}
	rec, files, e := r.refresh(ctx, tx, rec)
	if e != nil {
		return out, e
	}
	data, e := r.cfg.Generator.Generate(ctx, files)
	if e != nil {
		return out, e
	}
	if e = r.unchanged(ctx, tx, rec); e != nil {
		return out, e
	}
	if e = r.requireProvisioning(ctx, tx, actor); e != nil {
		return out, e
	}
	if e = active(ctx, tx, actor); e != nil {
		return out, e
	}
	if e = tx.Commit(); e != nil {
		return out, e
	}
	return passkit.Bundle{Data: data, Tag: strconv.FormatInt(rec.tag, 10), UpdatedAt: rec.updated}, nil
}
func (r *Repository) Latest(ctx context.Context, typ, serial, token string) (passkit.Bundle, error) {
	var out passkit.Bundle
	tx, rec, e := r.callback(ctx, typ, serial, token)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	rec, files, e := r.refresh(ctx, tx, rec)
	if e != nil {
		return out, e
	}
	data, e := r.cfg.Generator.Generate(ctx, files)
	if e != nil {
		return out, e
	}
	if e = r.unchanged(ctx, tx, rec); e != nil {
		return out, e
	}
	if e = active(ctx, tx, rec.actor); e != nil {
		return out, e
	}
	if e = tx.Commit(); e != nil {
		return out, e
	}
	return passkit.Bundle{Data: data, Tag: strconv.FormatInt(rec.tag, 10), UpdatedAt: rec.updated}, nil
}
func (r *Repository) Register(ctx context.Context, typ, serial, token, device, push string) (bool, error) {
	if !passkit.ValidOpaque(device, 255) || !passkit.ValidOpaque(push, 512) {
		return false, passkit.ErrInvalid
	}
	tx, rec, e := r.callback(ctx, typ, serial, token)
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	var exists bool
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_apple_pass_registrations WHERE pass_type=$1::text AND serial_number=$2::uuid AND device_id=$3::text)`, typ, serial, device).Scan(&exists)
	if e != nil {
		return false, e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_apple_pass_registrations(pass_type,serial_number,device_id,push_token,updated_at) VALUES($1::text,$2::uuid,$3::text,$4::text,clock_timestamp())ON CONFLICT(pass_type,serial_number,device_id)DO UPDATE SET push_token=EXCLUDED.push_token,updated_at=EXCLUDED.updated_at`, typ, serial, device, push)
	if e != nil {
		return false, e
	}
	if e = active(ctx, tx, rec.actor); e != nil {
		return false, e
	}
	return !exists, tx.Commit()
}
func (r *Repository) Unregister(ctx context.Context, typ, serial, token, device string) error {
	if !passkit.ValidOpaque(device, 255) {
		return passkit.ErrInvalid
	}
	tx, rec, e := r.callback(ctx, typ, serial, token)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	_, e = tx.ExecContext(ctx, `DELETE FROM owned_apple_pass_registrations WHERE pass_type=$1::text AND serial_number=$2::uuid AND device_id=$3::text`, typ, serial, device)
	if e != nil {
		return e
	}
	if e = active(ctx, tx, rec.actor); e != nil {
		return e
	}
	return tx.Commit()
}
func (r *Repository) Updates(ctx context.Context, typ, device, token, since string) (passkit.Serials, error) {
	out := passkit.Serials{SerialNumbers: []string{}, LastUpdated: since}
	if !passkit.ValidOpaque(device, 255) || typ != r.cfg.Generator.PassType() || token != "" && len(token) != 48 {
		return out, passkit.ErrUnauthorized
	}
	var previous int64
	var e error
	if since != "" {
		previous, e = strconv.ParseInt(since, 10, 64)
		if e != nil || previous < 0 {
			return out, passkit.ErrInvalid
		}
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT p.serial_number::text FROM owned_apple_passes p JOIN owned_apple_pass_registrations d ON d.pass_type=p.pass_type AND d.serial_number=p.serial_number WHERE p.pass_type=$1::text AND d.device_id=$2::text AND($3::text='' OR p.authentication_token=$3::text) ORDER BY p.serial_number LIMIT 1001`, typ, device, token)
	if e != nil {
		return out, e
	}
	var ids []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return out, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	if len(ids) > 1000 {
		return out, passkit.ErrUnavailable
	}
	if len(ids) == 0 && token != "" {
		return out, passkit.ErrUnauthorized
	}
	// Lock all participating canonical identities in UUID order before any
	// pass or media lock, then re-read registration and current account policy.
	for _, id := range ids {
		if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1::uuid FOR UPDATE`, id); e != nil {
			return out, e
		}
	}
	var latest int64
	for _, id := range ids {
		if e = active(ctx, tx, id); e != nil {
			if errors.Is(e, passkit.ErrUnauthorized) && token == "" {
				continue
			}
			return out, e
		}
		var rec record
		e = tx.QueryRowContext(ctx, `SELECT p.account_id::text,p.authentication_token,p.fingerprint,p.update_tag,p.updated_at FROM owned_apple_passes p WHERE p.pass_type=$1::text AND p.serial_number=$2::uuid AND EXISTS(SELECT 1 FROM owned_apple_pass_registrations d WHERE d.pass_type=p.pass_type AND d.serial_number=p.serial_number AND d.device_id=$3::text) FOR UPDATE`, typ, id, device).Scan(&rec.actor, &rec.token, &rec.fingerprint, &rec.tag, &rec.updated)
		if errors.Is(e, sql.ErrNoRows) {
			if token != "" {
				return out, passkit.ErrUnauthorized
			}
			continue
		}
		if e != nil {
			return out, e
		}
		if token != "" && subtle.ConstantTimeCompare([]byte(rec.token), []byte(token)) != 1 {
			return out, passkit.ErrUnauthorized
		}
		rec, _, e = r.refresh(ctx, tx, rec)
		if e != nil {
			return out, e
		}
		if e = active(ctx, tx, id); e != nil {
			return out, e
		}
		if rec.tag > latest {
			latest = rec.tag
		}
		if rec.tag > previous {
			out.SerialNumbers = append(out.SerialNumbers, id)
		}
	}
	if latest > 0 {
		out.LastUpdated = strconv.FormatInt(latest, 10)
	}
	return out, tx.Commit()
}
