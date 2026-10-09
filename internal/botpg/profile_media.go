// SPDX-License-Identifier: AGPL-3.0-only
package botpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/develop/apps"
	"solsynth.local/go-migration/ports/develop/bots"
	"time"
)

func botMediaError(e error) error {
	switch {
	case errors.Is(e, apps.ErrInvalid):
		return bots.ErrInvalid
	case errors.Is(e, apps.ErrForbidden):
		return bots.ErrForbidden
	case errors.Is(e, apps.ErrNotFound):
		return bots.ErrNotFound
	}
	return e
}
func (r *Repository) loadBotMedia(ctx context.Context, tx *sql.Tx, actor string, b *bots.Bot) error {
	if r.profileMedia == nil {
		return nil
	}
	var e error
	b.Account.Profile.Picture, b.Account.Profile.Background, e = r.profileMedia.Load(ctx, tx, actor, "bot", b.ID)
	return botMediaError(e)
}

// Bot image audience needs current statements. Other public modules retain
// their existing repeatable-read snapshot and package behavior unchanged.
func (r *Repository) publicBotCurrent(ctx context.Context, fn func(*sql.Tx, time.Time) error) error {
	if r == nil || r.db == nil {
		return bots.ErrUnavailable
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted, ReadOnly: true})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var now time.Time
	if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
		return e
	}
	if e = fn(tx, now.UTC()); e != nil {
		return mapped(e)
	}
	return tx.Commit()
}
