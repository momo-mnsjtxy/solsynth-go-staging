// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/messager/rooms"
	"strings"
)

// Associations join the room mutation transaction; reads hydrate current state.
type MessageAssociations interface {
	Apply(context.Context, *sql.Tx, string, *messager.MessageProjection, *[]string) error
	Load(context.Context, *sql.Tx, string, *messager.MessageProjection) error
	Remove(context.Context, *sql.Tx, string) error
	Ready(context.Context) error
}

type MessageComposer interface {
	Compose(context.Context, *sql.Tx, string, rooms.TextRequest, json.RawMessage) (rooms.TextRequest, error)
}

func hasCanonicalEmbed(p rooms.TextRequest) bool {
	if p.Embeds == nil {
		return false
	}
	for _, embed := range *p.Embeds {
		kind, _ := embed["type"].(string)
		switch kind {
		case "survey", "fund", "meet", "notable_day", "calendar_event":
			return true
		}
	}
	return false
}

func (r *Repository) composeMessage(ctx context.Context, tx *sql.Tx, actor string, p rooms.TextRequest, prior json.RawMessage) (rooms.TextRequest, error) {
	if p.SurveyID == nil && p.FundID == nil && p.MeetID == nil && p.NotableDayID == nil && p.CalendarEventID == nil && p.LocationName == nil && p.LocationAddress == nil && p.LocationWKT == nil && !hasCanonicalEmbed(p) && !p.SurveySet && !p.FundSet && !p.MeetSet && !p.NotableDaySet && !p.CalendarEventSet && !p.LocationSet {
		return p, nil
	}
	if r.composer == nil {
		return p, rooms.ErrUnavailable
	}
	return r.composer.Compose(ctx, tx, actor, p, prior)
}

func (r *Repository) WithAssociations(a MessageAssociations) *Repository {
	r.associations = a
	return r
}
func (r *Repository) loadMessage(ctx context.Context, tx *sql.Tx, actor string, v *messager.MessageProjection) error {
	if r.mls {
		var raw []byte
		if e := tx.QueryRowContext(ctx, `SELECT encryption_meta FROM chat_messages WHERE id=$1`, v.ID).Scan(&raw); e != nil {
			return e
		}
		if len(raw) > 0 {
			v.EncryptionMeta = raw
		}
	}
	if e := r.Load(ctx, tx, actor, v); e != nil {
		return e
	}
	v.ReactionsMade = map[string]bool{}
	// Anonymous public readers have no account-specific reaction state.
	if actor == "" {
		return nil
	}
	rows, e := tx.QueryContext(ctx, `SELECT reaction.symbol FROM chat_reactions reaction JOIN chat_members sender ON sender.id=reaction.sender_id WHERE reaction.message_id=$1 AND sender.chat_room_id=$2 AND sender.account_id=$3 AND reaction.deleted_at IS NULL AND sender.deleted_at IS NULL`, v.ID, v.ChatRoomID, actor)
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var symbol string
		if e = rows.Scan(&symbol); e != nil {
			return e
		}
		v.ReactionsMade[symbol] = true
	}
	return rows.Err()
}
func (r *Repository) applyAttachments(ctx context.Context, tx *sql.Tx, actor string, v *messager.MessageProjection, ids *[]string) error {
	if r.associations == nil {
		if ids != nil && len(*ids) > 0 {
			return rooms.ErrUnavailable
		}
		return nil
	}
	return r.associations.Apply(ctx, tx, actor, v, ids)
}
func mergeMeta(prior json.RawMessage, p rooms.TextRequest) json.RawMessage {
	meta := map[string]any{}
	if len(prior) > 0 {
		_ = json.Unmarshal(prior, &meta)
	}
	if len(p.Meta) > 0 {
		meta = map[string]any{}
		_ = json.Unmarshal(p.Meta, &meta)
	}
	if meta == nil {
		meta = map[string]any{}
	}
	if p.Embeds != nil {
		if len(*p.Embeds) > 0 {
			meta["embeds"] = *p.Embeds
		} else {
			delete(meta, "embeds")
		}
	}
	if len(meta) == 0 {
		return json.RawMessage(`null`)
	}
	out, _ := json.Marshal(meta)
	return out
}
func messageEmpty(v messager.MessageProjection) bool {
	if v.ForwardedMessageID != nil || v.ForwardUnavailable {
		return false
	}
	if len(v.EncryptionMeta) > 0 && string(v.EncryptionMeta) != "null" {
		return false
	}
	if v.Content != nil && strings.TrimSpace(*v.Content) != "" {
		return false
	}
	var attachments []any
	_ = json.Unmarshal(v.Attachments, &attachments)
	if len(attachments) > 0 {
		return false
	}
	var meta struct {
		Embeds []map[string]any `json:"embeds"`
	}
	_ = json.Unmarshal(v.Meta, &meta)
	return len(meta.Embeds) == 0
}
