// SPDX-License-Identifier: AGPL-3.0-only
// Source-informed from licensed Develop BotAccountService/Controller and
// BOT_CHAT_API.md at 4ee2d0cebb9da8284a92483fee6dce795bf6947c.
package developpg

import (
	"context"
	"database/sql"
	"errors"

	"solsynth.local/go-migration/ports/develop"
)

type BotCommandsRepository struct {
	db        *sql.DB
	candidate bool
}

func NewBotCommandsRepository(db *sql.DB) *BotCommandsRepository {
	return &BotCommandsRepository{db: db}
}
func NewSyntheticCompatibilityBotCommandsRepository(db *sql.DB) *BotCommandsRepository {
	return &BotCommandsRepository{db: db, candidate: true}
}

var _ develop.BotCommandsReader = (*BotCommandsRepository)(nil)

// ReadBotCommands executes real reads with explicit active-row and documented
// lower-case JSON fixture policy. It does not test is_active: neither retained
// source lookup nor public controller has that predicate. The coherent single
// SQL statement is a deliberate stronger snapshot than two upstream queries.
func (r *BotCommandsRepository) ReadBotCommands(ctx context.Context, id string) ([]develop.BotCommand, error) {
	if r == nil || !r.candidate {
		return nil, develop.ErrBotCommandsPolicyUnverified
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := develop.ValidateBotCommandID(id); err != nil {
		return nil, err
	}
	if r.db == nil {
		return nil, ErrDatabaseRequired
	}
	var raw sql.NullString
	err := r.db.QueryRowContext(ctx, `SELECT c.commands::text FROM bot_accounts b
 JOIN dev_projects p ON p.id=b.project_id AND p.deleted_at IS NULL
 LEFT JOIN bot_chat_configs c ON c.id=b.id AND c.deleted_at IS NULL
 WHERE b.id=$1 AND b.deleted_at IS NULL`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, develop.ErrBotNotFound
	}
	if err != nil {
		return nil, err
	}
	if !raw.Valid {
		return []develop.BotCommand{}, nil
	}
	return develop.DecodeSyntheticBotCommands([]byte(raw.String))
}
