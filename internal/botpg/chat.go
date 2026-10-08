// SPDX-License-Identifier: AGPL-3.0-only
package botpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop"
	"solsynth.local/go-migration/ports/develop/apps"
	"solsynth.local/go-migration/ports/develop/bots"
	"time"
)

func chat(ctx context.Context, tx *sql.Tx, id string, created time.Time) (v bots.ChatConfig, e error) {
	v = bots.DefaultChat(id, created)
	var commands, events []byte
	e = tx.QueryRowContext(ctx, `SELECT commands,auto_approve_dm,auto_approve_group_chat,support_chat,subscribed_events,created_at,updated_at FROM owned_bot_chat WHERE bot_id=$1`, id).Scan(&commands, &v.AutoApproveDM, &v.AutoApproveGroupChat, &v.SupportChat, &events, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(e, sql.ErrNoRows) {
		return v, nil
	}
	if e != nil {
		return v, e
	}
	if e = json.Unmarshal(commands, &v.Commands); e != nil {
		return v, e
	}
	e = json.Unmarshal(events, &v.SubscribedEvents)
	return
}
func saveChat(ctx context.Context, tx *sql.Tx, s apps.Scope, v bots.ChatConfig, now time.Time) error {
	if e := bots.ValidateCommands(v.Commands); e != nil {
		return e
	}
	if e := bots.ValidateManifestPatch(bots.ManifestPatch{SubscribedEvents: &v.SubscribedEvents}); e != nil {
		return e
	}
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_bot_chat(bot_id,commands,auto_approve_dm,auto_approve_group_chat,support_chat,subscribed_events,created_at,updated_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8)ON CONFLICT(bot_id)DO UPDATE SET commands=EXCLUDED.commands,auto_approve_dm=EXCLUDED.auto_approve_dm,auto_approve_group_chat=EXCLUDED.auto_approve_group_chat,support_chat=EXCLUDED.support_chat,subscribed_events=EXCLUDED.subscribed_events,updated_at=EXCLUDED.updated_at`, v.ID, encoded(v.Commands), v.AutoApproveDM, v.AutoApproveGroupChat, v.SupportChat, encoded(v.SubscribedEvents), v.CreatedAt, now)
	if e != nil {
		return e
	}
	return audit(ctx, tx, s, "bot.chat.update", v.ID, now)
}
func (r *Repository) Chat(ctx context.Context, s apps.Scope, id string, p *bots.ManifestPatch, replace bool) (out bots.ChatConfig, e error) {
	if !authority.ValidID(id) {
		return out, bots.ErrInvalid
	}
	permission := ""
	role := 0
	if p != nil {
		permission = "developers.bots.chat.manage"
		role = 50
		if e = bots.ValidateManifestPatch(*p); e != nil {
			return out, e
		}
	}
	e = r.within(ctx, s, id, permission, role, func(tx *sql.Tx, _ appScope, now time.Time) error {
		row, e := loadBot(ctx, tx, s.App, id)
		if e != nil {
			return e
		}
		if row.bot.DeletedAt != nil {
			return bots.ErrNotFound
		}
		if p != nil {
			if e = mutableBot(ctx, tx, row, now); e != nil {
				return e
			}
		}
		out, e = chat(ctx, tx, id, row.bot.CreatedAt)
		if e != nil {
			return e
		}
		if p == nil {
			return nil
		}
		if replace {
			created := out.CreatedAt
			out = bots.DefaultChat(id, created)
		}
		if p.Commands != nil {
			out.Commands = bots.CloneCommands(*p.Commands)
		}
		if p.AutoApproveDM != nil {
			out.AutoApproveDM = *p.AutoApproveDM
		}
		if p.AutoApproveGroupChat != nil {
			out.AutoApproveGroupChat = *p.AutoApproveGroupChat
		}
		if p.SupportChat != nil {
			out.SupportChat = *p.SupportChat
		}
		if p.SubscribedEvents != nil {
			out.SubscribedEvents = append([]string{}, (*p.SubscribedEvents)...)
		}
		out.UpdatedAt = now
		return saveChat(ctx, tx, s, out, now)
	})
	return
}
func (r *Repository) PublicChat(ctx context.Context, id string) (out bots.ChatConfig, e error) {
	e = r.public(ctx, func(tx *sql.Tx, _ time.Time) error {
		app, e := publicBotApp(ctx, tx, id)
		if e != nil {
			return e
		}
		row, e := loadBot(ctx, tx, app, id)
		if e != nil {
			return e
		}
		out, e = chat(ctx, tx, id, row.bot.CreatedAt)
		return e
	})
	return
}
func (r *Repository) Command(ctx context.Context, s apps.Scope, id, name string, cmd *develop.BotCommand, action string) (out []develop.BotCommand, e error) {
	if !authority.ValidID(id) || len(name) > 64 {
		return nil, bots.ErrInvalid
	}
	permission := ""
	role := 0
	switch action {
	case "read":
	case "create", "update", "delete":
		permission = "developers.bots.chat.manage"
		role = 50
	default:
		return nil, bots.ErrInvalid
	}
	if action == "create" || action == "update" {
		if cmd == nil {
			return nil, bots.ErrInvalid
		}
		if e = bots.ValidateCommands([]develop.BotCommand{*cmd}); e != nil {
			return nil, e
		}
		if action == "update" && cmd.Name != name {
			return nil, bots.ErrInvalid
		}
	}
	e = r.within(ctx, s, id, permission, role, func(tx *sql.Tx, _ appScope, now time.Time) error {
		row, e := loadBot(ctx, tx, s.App, id)
		if e != nil {
			return e
		}
		if row.bot.DeletedAt != nil {
			return bots.ErrNotFound
		}
		if action != "read" {
			if e = mutableBot(ctx, tx, row, now); e != nil {
				return e
			}
		}
		v, e := chat(ctx, tx, id, row.bot.CreatedAt)
		if e != nil {
			return e
		}
		index := -1
		for i, c := range v.Commands {
			if c.Name == name {
				index = i
			}
		}
		switch action {
		case "read":
			if name != "" {
				if index < 0 {
					return bots.ErrNotFound
				}
				out = bots.CloneCommands(v.Commands[index : index+1])
			} else {
				out = bots.CloneCommands(v.Commands)
			}
			return nil
		case "create":
			for _, c := range v.Commands {
				if c.Name == cmd.Name {
					return bots.ErrConflict
				}
			}
			v.Commands = append(v.Commands, *cmd)
		case "update":
			if index < 0 {
				return bots.ErrNotFound
			}
			v.Commands[index] = *cmd
		case "delete":
			if index < 0 {
				return bots.ErrNotFound
			}
			v.Commands = append(v.Commands[:index], v.Commands[index+1:]...)
		}
		out = bots.CloneCommands(v.Commands)
		return saveChat(ctx, tx, s, v, now)
	})
	return
}
