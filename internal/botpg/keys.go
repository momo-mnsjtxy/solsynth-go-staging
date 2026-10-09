// SPDX-License-Identifier: AGPL-3.0-only
package botpg

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/apps"
	"solsynth.local/go-migration/ports/develop/bots"
)

const BotKeyPermission = "developers.bots.keys.manage"

func botKeyDigest(bot, token string) []byte {
	h := sha256.Sum256([]byte("solsynth.owned-bot-key.v1\x00" + bot + "\x00" + token))
	return h[:]
}

func scanKey(row interface{ Scan(...any) error }) (bots.Key, error) {
	var out bots.Key
	err := row.Scan(&out.ID, &out.BotID, &out.Label, &out.CreatedAt, &out.UpdatedAt, &out.RevokedAt)
	return out, mapped(err)
}

const keyColumns = `id::text,bot_id::text,label,created_at,updated_at,revoked_at`

func validKeyLabel(label string) bool {
	return strings.TrimSpace(label) == label && label != "" && len(label) <= 1024 && !strings.ContainsRune(label, '\x00')
}

// Keys exposes metadata for reads and reveals a credential exactly once after a
// committed create or rotation. Delete is a durable revocation, not hard delete.
func (r *Repository) Keys(ctx context.Context, s apps.Scope, bot, ref string, req *bots.KeyRequest, action string) (out []bots.Key, err error) {
	out = []bots.Key{}
	if !authority.ValidID(bot) || ref != "" && !authority.ValidID(ref) {
		return out, bots.ErrInvalid
	}
	permission, role := "", 0
	switch action {
	case "read":
	case "create", "rotate", "delete":
		permission, role = BotKeyPermission, 50
	default:
		return out, bots.ErrInvalid
	}
	if action == "create" && ref != "" || (action == "rotate" || action == "delete") && ref == "" {
		return out, bots.ErrInvalid
	}
	if action == "create" && (req == nil || !validKeyLabel(req.Label)) || action == "rotate" && req != nil && req.Label != "" && !validKeyLabel(req.Label) {
		return out, bots.ErrInvalid
	}
	err = r.within(ctx, s, bot, permission, role, func(tx *sql.Tx, _ appScope, now time.Time) error {
		row, e := loadBot(ctx, tx, s.App, bot)
		if e != nil {
			return e
		}
		if row.bot.DeletedAt != nil {
			return bots.ErrNotFound
		}
		if action != "read" {
			if !row.active || s.Actor == row.bot.Account.ID {
				return bots.ErrForbidden
			}
			if e = mutableBot(ctx, tx, row, now); e != nil {
				return e
			}
		}
		if action == "read" {
			if ref != "" {
				key, e := scanKey(tx.QueryRowContext(ctx, `SELECT `+keyColumns+` FROM owned_bot_keys WHERE id=$1 AND bot_id=$2 AND revoked_at IS NULL`, ref, bot))
				if e != nil {
					return e
				}
				out = append(out, key)
				return nil
			}
			rows, e := tx.QueryContext(ctx, `SELECT `+keyColumns+` FROM owned_bot_keys WHERE bot_id=$1 AND revoked_at IS NULL ORDER BY created_at DESC,id`, bot)
			if e != nil {
				return e
			}
			defer rows.Close()
			for rows.Next() {
				key, e := scanKey(rows)
				if e != nil {
					return e
				}
				out = append(out, key)
			}
			return rows.Err()
		}

		var key bots.Key
		generation := int64(1)
		if action == "create" {
			key.ID, e = newID()
			if e != nil {
				return e
			}
			key.BotID, key.Label, key.CreatedAt = bot, req.Label, now
		} else {
			var currentGeneration int64
			key, e = scanKey(tx.QueryRowContext(ctx, `SELECT `+keyColumns+` FROM owned_bot_keys WHERE id=$1 AND bot_id=$2 AND revoked_at IS NULL FOR UPDATE`, ref, bot))
			if e != nil {
				return e
			}
			if action == "delete" {
				result, e := tx.ExecContext(ctx, `UPDATE owned_bot_keys SET revoked_at=$3,updated_at=$3 WHERE id=$1 AND bot_id=$2 AND revoked_at IS NULL`, ref, bot, now)
				if e != nil {
					return e
				}
				if n, e := result.RowsAffected(); e != nil || n != 1 {
					if e != nil {
						return e
					}
					return bots.ErrNotFound
				}
				key.RevokedAt, key.UpdatedAt = &now, now
				out = append(out, key)
				return audit(ctx, tx, s, "bot.key.revoke", key.ID, now)
			}
			if e = tx.QueryRowContext(ctx, `SELECT generation FROM owned_bot_keys WHERE id=$1`, ref).Scan(&currentGeneration); e != nil {
				return e
			}
			generation = currentGeneration + 1
			if req != nil && req.Label != "" {
				key.Label = req.Label
			}
		}
		secretBytes := make([]byte, 32)
		if _, e = rand.Read(secretBytes); e != nil {
			return e
		}
		token := "bk1." + key.ID + "." + base64.RawURLEncoding.EncodeToString(secretBytes)
		key.Secret, key.UpdatedAt = &token, now
		if action == "create" {
			_, e = tx.ExecContext(ctx, `INSERT INTO owned_bot_keys(id,bot_id,secret_hash,label,generation,created_at,updated_at)VALUES($1,$2,$3,$4,$5,$6,$6)`, key.ID, bot, botKeyDigest(bot, token), key.Label, generation, now)
		} else {
			_, e = tx.ExecContext(ctx, `UPDATE owned_bot_keys SET secret_hash=$3,label=$4,generation=$5,updated_at=$6 WHERE id=$1 AND bot_id=$2 AND revoked_at IS NULL`, key.ID, bot, botKeyDigest(bot, token), key.Label, generation, now)
		}
		if e != nil {
			return e
		}
		if e = audit(ctx, tx, s, "bot.key."+action, key.ID, now); e != nil {
			return e
		}
		out = append(out, key)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// AuthenticateBotKey resolves only the bot identity bound into the credential.
// It grants no developer identity and accepts neither bearer sessions nor labels.
func (r *Repository) AuthenticateBotKey(ctx context.Context, bot, token string) (account string, err error) {
	if r == nil || r.db == nil || !authority.ValidID(bot) || len(token) > 256 {
		return "", bots.ErrInvalid
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "bk1" || !authority.ValidID(parts[1]) {
		return "", bots.ErrForbidden
	}
	raw, e := base64.RawURLEncoding.DecodeString(parts[2])
	if e != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != parts[2] {
		return "", bots.ErrForbidden
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted, ReadOnly: true})
	if e != nil {
		return "", e
	}
	defer tx.Rollback()
	account, e = r.authenticateBotKeyInTransaction(ctx, tx, bot, token, parts[1], false)
	if e != nil {
		return "", e
	}
	if e = tx.Commit(); e != nil {
		return "", e
	}
	return account, nil
}

func (r *Repository) AuthenticateBotKeyInTransaction(ctx context.Context, db *sql.DB, tx *sql.Tx, bot, token string) (string, error) {
	if r == nil || r.db != db || tx == nil || !authority.ValidID(bot) || len(token) > 256 {
		return "", bots.ErrInvalid
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "bk1" || !authority.ValidID(parts[1]) {
		return "", bots.ErrForbidden
	}
	raw, e := base64.RawURLEncoding.DecodeString(parts[2])
	if e != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != parts[2] {
		return "", bots.ErrForbidden
	}
	return r.authenticateBotKeyInTransaction(ctx, tx, bot, token, parts[1], true)
}

func (r *Repository) authenticateBotKeyInTransaction(ctx context.Context, tx *sql.Tx, bot, token, key string, lock bool) (account string, err error) {
	var stored []byte
	var active bool
	suffix := ""
	if lock {
		// Bot-reply lock planning performs only nonlocking proof inspection. The
		// canonical chat write acquires its complete sorted account set before this
		// key/bot lock, matching key mutation's account-before-key order. A
		// successful final check is therefore linearized with rotation/revocation
		// until the chat transaction commits.
		suffix = " FOR SHARE OF k,b,account"
	}
	err = tx.QueryRowContext(ctx, `SELECT k.secret_hash,b.account_id::text,b.is_active AND b.deleted_at IS NULL AND NOT account.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=account.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=clock_timestamp())AND(p.expires_at IS NULL OR p.expires_at>clock_timestamp())) FROM owned_bot_keys k JOIN owned_bots b ON b.id=k.bot_id JOIN owned_authority_accounts account ON account.id=b.account_id WHERE k.id=$1 AND k.bot_id=$2 AND k.revoked_at IS NULL`+suffix, key, bot).Scan(&stored, &account, &active)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (!active || subtle.ConstantTimeCompare(stored, botKeyDigest(bot, token)) != 1) {
		return "", bots.ErrForbidden
	}
	return account, err
}
