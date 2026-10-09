// SPDX-License-Identifier: AGPL-3.0-only
package rooms

import (
	"bytes"
	"encoding/json"
	"solsynth.local/go-migration/ports/messager"
	"strings"
	"unicode/utf8"
)

func ValidID(id string) bool {
	return id != "00000000-0000-0000-0000-000000000000" && id == strings.ToLower(id) && messager.ValidateSyncID(id) == nil
}
func validText(s string, max int) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > max {
		return false
	}
	for _, r := range s {
		if r == 0 || r < 32 && r != '\n' && r != '\t' && r != '\r' || r == 127 {
			return false
		}
	}
	return true
}
func ValidateRoom(p RoomPatch, create bool, policy Policy) error {
	if _, e := NormalizeSlug(p.Slug); e != nil {
		return e
	}
	if p.PictureID != nil && !ValidID(*p.PictureID) || p.BackgroundID != nil && !ValidID(*p.BackgroundID) || p.RemovePicture && p.PictureID != nil || p.RemoveBackground && p.BackgroundID != nil || create && (p.RemovePicture || p.RemoveBackground) {
		return ErrInvalid
	}
	if p.RealmID != nil && !ValidID(*p.RealmID) {
		return ErrInvalid
	}
	if create && p.Name == nil || p.Name != nil && (!validText(*p.Name, 1024) || strings.TrimSpace(*p.Name) == "") || p.Description != nil && !validText(*p.Description, 4096) {
		return ErrInvalid
	}
	if p.EncryptionMode != nil && *p.EncryptionMode != policy.NoneEncryptionMode {
		return ErrUnsupported
	}
	return nil
}
func ValidateTimeout(p *TimeoutRequest) error {
	if p != nil && (p.TimeoutUntil.IsZero() || p.Reason != nil && !validText(*p.Reason, 4096)) {
		return ErrInvalid
	}
	return nil
}
func ValidateText(p TextRequest, edit bool) (TextRequest, error) {
	if p.Type != nil && *p.Type != "text" {
		return p, ErrUnsupported
	}
	if p.SurveyID != nil && !ValidID(*p.SurveyID) || p.FundID != nil && !ValidID(*p.FundID) || p.MeetID != nil && !ValidID(*p.MeetID) || p.NotableDayID != nil && !ValidID(*p.NotableDayID) || p.CalendarEventID != nil && !ValidID(*p.CalendarEventID) {
		return p, ErrInvalid
	}
	if p.LocationName != nil && !validText(*p.LocationName, 256) || p.LocationAddress != nil && !validText(*p.LocationAddress, 1024) || p.LocationWKT != nil && !validText(*p.LocationWKT, 256) {
		return p, ErrInvalid
	}
	if p.EncryptionMeta != nil {
		if e := ValidateEncryption(p.EncryptionMeta); e != nil {
			return p, e
		}
		if p.ContentSet || p.Content != "" || p.Embeds != nil || p.SurveyID != nil || p.FundID != nil || p.MeetID != nil || p.NotableDayID != nil || p.CalendarEventID != nil || p.LocationName != nil || p.LocationAddress != nil || p.LocationWKT != nil {
			return p, ErrInvalid
		}
		if e := ValidateMLSReferenceMeta(p.Meta); e != nil {
			return p, e
		}
	}
	if !validText(p.Content, 4096) {
		return p, ErrInvalid
	}
	if p.AttachmentsID != nil {
		if len(*p.AttachmentsID) > 32 {
			return p, ErrInvalid
		}
		seen := map[string]bool{}
		for _, id := range *p.AttachmentsID {
			if !ValidID(id) || seen[id] {
				return p, ErrInvalid
			}
			seen[id] = true
		}
	}
	if len(p.Meta) > 0 {
		var value map[string]any
		if len(p.Meta) > 16384 || json.Unmarshal(p.Meta, &value) != nil || value == nil {
			return p, ErrInvalid
		}
		if _, reserved := value["redirect"]; reserved {
			return p, ErrInvalid
		}
		// Canonical embeds must use the top-level embeds field so the message
		// composer can authorize and lock their source rows. Unknown legacy
		// metadata remains opaque, but cannot impersonate a canonical locator.
		if raw, exists := value["embeds"]; exists {
			list, ok := raw.([]any)
			if !ok {
				return p, ErrInvalid
			}
			for _, item := range list {
				embed, ok := item.(map[string]any)
				if !ok {
					return p, ErrInvalid
				}
				kind, _ := embed["type"].(string)
				switch kind {
				case "survey", "fund", "meet", "notable_day", "calendar_event":
					return p, ErrInvalid
				}
			}
		}
		p.Meta, _ = json.Marshal(value)
	}
	if p.Embeds != nil {
		if len(*p.Embeds) > 16 {
			return p, ErrInvalid
		}
		for _, v := range *p.Embeds {
			if v == nil {
				return p, ErrInvalid
			}
		}
		raw, e := json.Marshal(*p.Embeds)
		if e != nil || len(raw) > 16384 {
			return p, ErrInvalid
		}
	}
	if !edit && p.ForwardedMessageID == nil && p.EncryptionMeta == nil && p.SurveyID == nil && p.FundID == nil && p.MeetID == nil && p.NotableDayID == nil && p.CalendarEventID == nil && p.LocationName == nil && p.LocationAddress == nil && p.LocationWKT == nil && strings.TrimSpace(p.Content) == "" && (p.AttachmentsID == nil || len(*p.AttachmentsID) == 0) && (p.Embeds == nil || len(*p.Embeds) == 0) {
		return p, ErrInvalid
	}
	if p.Nonce != nil && (!validText(*p.Nonce, 36) || strings.TrimSpace(*p.Nonce) == "") {
		return p, ErrInvalid
	}
	if p.ClientMessageID != nil && (!validText(*p.ClientMessageID, 128) || strings.TrimSpace(*p.ClientMessageID) == "") {
		return p, ErrInvalid
	}
	if p.ClientMessageID != nil && p.Nonce != nil && *p.ClientMessageID != *p.Nonce {
		return p, ErrInvalid
	}
	if p.ClientMessageID == nil {
		p.ClientMessageID = p.Nonce
	}
	p.Nonce = nil
	if p.RepliedMessageID != nil && !ValidID(*p.RepliedMessageID) {
		return p, ErrInvalid
	}
	if p.ThreadID != nil && !ValidID(*p.ThreadID) || p.ForwardedMessageID != nil && !ValidID(*p.ForwardedMessageID) {
		return p, ErrInvalid
	}
	if p.EncryptionMeta != nil && (p.ThreadID != nil || p.ForwardedMessageID != nil || p.IsThreadRoot) {
		return p, ErrUnsupported
	}
	if p.IsThreadRoot && (p.ThreadID != nil || p.RepliedMessageID != nil) {
		return p, ErrInvalid
	}
	return p, nil
}

// ContentSet distinguishes an omitted edit field from an explicit empty value.
func (p *TextRequest) UnmarshalJSON(data []byte) error {
	type plain TextRequest
	var v plain
	if e := json.Unmarshal(data, &v); e != nil {
		return e
	}
	var fields map[string]json.RawMessage
	if e := json.Unmarshal(data, &fields); e != nil {
		return e
	}
	if raw, ok := fields["content"]; ok {
		v.ContentSet = !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
	}
	_, v.SurveySet = fields["survey_id"]
	_, v.FundSet = fields["fund_id"]
	_, v.MeetSet = fields["meet_id"]
	_, v.NotableDaySet = fields["notable_day_id"]
	_, v.CalendarEventSet = fields["calendar_event_id"]
	_, nameSet := fields["location_name"]
	_, addressSet := fields["location_address"]
	_, wktSet := fields["location_wkt"]
	v.LocationSet = nameSet || addressSet || wktSet
	if bytes.Equal(bytes.TrimSpace(v.Meta), []byte("null")) {
		v.Meta = nil
	}

	*p = TextRequest(v)
	return nil
}
