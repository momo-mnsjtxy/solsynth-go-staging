// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"solsynth.local/go-migration/internal/chatpg"
	"solsynth.local/go-migration/internal/surveypg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/sphere/locations"
	"solsynth.local/go-migration/ports/sphere/surveys"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
)

type chatMessageComposer struct {
	funds   *walletpg.Repository
	surveys *surveypg.Repository
}

func NewChatMessageComposer(funds *walletpg.Repository, surveys *surveypg.Repository) chatpg.MessageComposer {
	return chatMessageComposer{funds: funds, surveys: surveys}
}

type chatCanonicalReference struct {
	kind string
	id   string
}

func chatCanonicalReferences(p rooms.TextRequest) ([]chatCanonicalReference, error) {
	refs := []chatCanonicalReference{}
	add := func(kind string, id *string) {
		if id != nil {
			refs = append(refs, chatCanonicalReference{kind: kind, id: *id})
		}
	}
	add("fund", p.FundID)
	add("survey", p.SurveyID)
	add("meet", p.MeetID)
	add("notable_day", p.NotableDayID)
	add("calendar_event", p.CalendarEventID)
	if p.Embeds != nil {
		for _, raw := range *p.Embeds {
			kind, _ := raw["type"].(string)
			switch kind {
			case "fund", "survey", "meet", "notable_day", "calendar_event":
				id, ok := raw["id"].(string)
				// Canonical locators contain no client-authored projection fields.
				if !ok || !rooms.ValidID(id) || len(raw) != 2 {
					return nil, rooms.ErrInvalid
				}
				refs = append(refs, chatCanonicalReference{kind: kind, id: id})
			}
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].kind == refs[j].kind {
			return refs[i].id < refs[j].id
		}
		return refs[i].kind < refs[j].kind
	})
	unique := refs[:0]
	for _, ref := range refs {
		if len(unique) == 0 || unique[len(unique)-1] != ref {
			unique = append(unique, ref)
		}
	}
	return unique, nil
}

func validateOwnedChatReference(ctx context.Context, tx *sql.Tx, actor string, ref chatCanonicalReference, lock bool) error {
	suffix := ""
	if lock {
		suffix = " FOR SHARE OF src,acct"
	}
	var query string
	switch ref.kind {
	case "meet":
		query = `SELECT src.id::text FROM owned_meets src JOIN owned_authority_accounts acct ON acct.id=src.host_id WHERE src.id=$1 AND src.host_id=$2 AND src.deleted_at IS NULL AND src.status=0 AND src.expires_at>clock_timestamp() AND NOT acct.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments punishment WHERE punishment.account_id=acct.id AND punishment.kind='disable_account' AND(punishment.starts_at IS NULL OR punishment.starts_at<=clock_timestamp())AND(punishment.expires_at IS NULL OR punishment.expires_at>clock_timestamp()))` + suffix
	case "calendar_event":
		query = `SELECT src.id::text FROM owned_calendar_events src JOIN owned_authority_accounts acct ON acct.id=src.account_id WHERE src.id=$1 AND src.account_id=$2 AND src.deleted_at IS NULL AND NOT acct.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments punishment WHERE punishment.account_id=acct.id AND punishment.kind='disable_account' AND(punishment.starts_at IS NULL OR punishment.starts_at<=clock_timestamp())AND(punishment.expires_at IS NULL OR punishment.expires_at>clock_timestamp()))` + suffix
	case "notable_day":
		query = `SELECT src.id::text FROM owned_notable_days src WHERE src.id=$1 AND src.deleted_at IS NULL`
		if lock {
			query += ` FOR SHARE OF src`
		}
	default:
		return rooms.ErrInvalid
	}
	var id string
	var err error
	if ref.kind == "notable_day" {
		err = tx.QueryRowContext(ctx, query, ref.id).Scan(&id)
	} else {
		err = tx.QueryRowContext(ctx, query, ref.id, actor).Scan(&id)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return rooms.ErrInvalid
	}
	return err
}

func (c chatMessageComposer) validateReferences(ctx context.Context, tx *sql.Tx, actor string, refs []chatCanonicalReference, lockOwned bool) error {
	for _, ref := range refs {
		switch ref.kind {
		case "fund":
			if c.funds == nil {
				return rooms.ErrUnavailable
			}
			if e := c.funds.ValidateChatFund(ctx, tx, actor, ref.id); e != nil {
				if errors.Is(e, wallet.ErrInvalid) || errors.Is(e, wallet.ErrNotFound) || errors.Is(e, wallet.ErrForbidden) {
					return rooms.ErrInvalid
				}
				return rooms.ErrUnavailable
			}
		case "survey":
			if c.surveys == nil {
				return rooms.ErrUnavailable
			}
			if e := c.surveys.ValidateChatSurvey(ctx, tx, actor, ref.id); e != nil {
				if errors.Is(e, surveys.ErrInvalid) || errors.Is(e, surveys.ErrNotFound) || errors.Is(e, surveys.ErrForbidden) {
					return rooms.ErrInvalid
				}
				return rooms.ErrUnavailable
			}
		case "meet", "notable_day", "calendar_event":
			if e := validateOwnedChatReference(ctx, tx, actor, ref, lockOwned); e != nil {
				return e
			}
		}
	}
	return nil
}

func (c chatMessageComposer) Revalidate(ctx context.Context, tx *sql.Tx, actor string, meta json.RawMessage) error {
	var value struct {
		Embeds []map[string]any `json:"embeds"`
	}
	if json.Unmarshal(meta, &value) != nil {
		return rooms.ErrInvalid
	}
	refs, e := chatCanonicalReferences(rooms.TextRequest{Embeds: &value.Embeds})
	if e != nil {
		return e
	}
	return c.validateReferences(ctx, tx, actor, refs, false)
}

func (c chatMessageComposer) Compose(ctx context.Context, tx *sql.Tx, actor string, p rooms.TextRequest, prior json.RawMessage) (rooms.TextRequest, error) {
	refs, e := chatCanonicalReferences(p)
	if e != nil {
		return p, e
	}
	if e = c.validateReferences(ctx, tx, actor, refs, true); e != nil {
		return p, e
	}
	// Earlier checks can wait on later source locks. Re-evaluate all canonical
	// lifecycle and ownership predicates after the complete ordered lock set.
	if e = c.validateReferences(ctx, tx, actor, refs, false); e != nil {
		return p, e
	}
	// An explicit complete embeds list has authored precedence, while typed IDs
	// above still receive canonical authorization checks.
	if p.Embeds != nil {
		return p, nil
	}
	embeds := []map[string]any{}
	var meta struct {
		Embeds []map[string]any `json:"embeds"`
	}
	if len(prior) > 0 && json.Unmarshal(prior, &meta) == nil {
		embeds = append(embeds, meta.Embeds...)
	}
	filter := func(kind string) {
		kept := embeds[:0]
		for _, embed := range embeds {
			if embed["type"] != kind {
				kept = append(kept, embed)
			}
		}
		embeds = kept
	}
	if p.FundSet || p.FundID != nil {
		filter("fund")
		if p.FundID != nil {
			embeds = append(embeds, map[string]any{"type": "fund", "id": *p.FundID})
		}
	}
	if p.SurveySet || p.SurveyID != nil {
		filter("survey")
		if p.SurveyID != nil {
			embeds = append(embeds, map[string]any{"type": "survey", "id": *p.SurveyID})
		}
	}
	for _, ref := range []struct {
		set  bool
		id   *string
		kind string
	}{{p.MeetSet, p.MeetID, "meet"}, {p.NotableDaySet, p.NotableDayID, "notable_day"}, {p.CalendarEventSet, p.CalendarEventID, "calendar_event"}} {
		if ref.set || ref.id != nil {
			filter(ref.kind)
			if ref.id != nil {
				embeds = append(embeds, map[string]any{"type": ref.kind, "id": *ref.id})
			}
		}
	}
	if p.LocationSet || p.LocationName != nil || p.LocationAddress != nil || p.LocationWKT != nil {
		filter("location")
		location := map[string]any{"type": "location"}
		if p.LocationName != nil && strings.TrimSpace(*p.LocationName) != "" {
			location["name"] = *p.LocationName
		}
		if p.LocationAddress != nil && strings.TrimSpace(*p.LocationAddress) != "" {
			location["address"] = *p.LocationAddress
		}
		if p.LocationWKT != nil && strings.TrimSpace(*p.LocationWKT) != "" {
			point, e := locations.ParsePoint(*p.LocationWKT)
			if e != nil {
				return p, rooms.ErrInvalid
			}
			location["wkt"] = point.WKT()
		}
		if len(location) > 1 {
			embeds = append(embeds, location)
		}
	}
	p.Embeds = &embeds
	return p, nil
}
