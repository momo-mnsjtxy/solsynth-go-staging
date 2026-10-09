// SPDX-License-Identifier: AGPL-3.0-only
package realmmembers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"solsynth.local/go-migration/ports/authority"
	b "solsynth.local/go-migration/ports/passport/realmboosts"
	"solsynth.local/go-migration/ports/passport/social"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (f *Failure) Error() string { return f.Code }

var (
	ErrInvalid     = &Failure{"REALM_MEMBER_INVALID", "Invalid realm member request.", 400}
	ErrForbidden   = &Failure{"REALM_MEMBER_FORBIDDEN", "Current realm role and permission are required.", 403}
	ErrNotFound    = &Failure{"PASSPORT_REALM_MEMBER_RESOURCE_NOT_FOUND", "Realm resource not found.", 404}
	ErrLabelRealm  = &Failure{"REALM_LABEL_NOT_BELONG", "Label does not belong to this realm.", 400}
	ErrQuota       = &Failure{"REALM_LABEL_LIMIT_REACHED", "Realm label limit reached for current boost level.", 400}
	ErrUnavailable = &Failure{"REALM_MEMBER_UNAVAILABLE", "Realm member features require explicit owned configuration.", 503}
	ErrConflict    = &Failure{"REALM_MEMBER_CONFLICT", "Request conflicts with current realm state.", 409}
	ErrRetry       = &Failure{"REALM_MEMBER_RETRY_REQUIRED", "Contention rolled back the transaction; retry the same request.", 409}
)

type LabelRequest struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Color       *string `json:"color"`
	Icon        *string `json:"icon"`
}
type AssignmentRequest struct {
	LabelID *string `json:"label_id"`
}
type TenureRequest struct {
	After string `json:"after"`
	Take  int    `json:"take"`
}
type TenureResult struct {
	Day      string  `json:"day"`
	Examined int     `json:"examined"`
	Awarded  int     `json:"awarded"`
	Next     *string `json:"next"`
}
type Repository interface {
	Labels(context.Context, string, string) ([]social.RealmLabel, error)
	CreateLabel(context.Context, string, string, string, LabelRequest) (social.RealmLabel, error)
	UpdateLabel(context.Context, string, string, string, LabelRequest) (social.RealmLabel, error)
	DeleteLabel(context.Context, string, string, string) error
	AssignLabel(context.Context, string, string, string, *string) (social.Member, error)
	MembersWithLabel(context.Context, string, string, string, int, int) ([]social.Member, int64, error)
	Progression(context.Context, string, string, string) (social.MemberProgression, error)
	History(context.Context, string, string, string, int) ([]b.Experience, error)
	GrantTenure(context.Context, string, string, TenureRequest) (TenureResult, error)
}
type Auth func(*http.Request, bool) (string, error)

func bounded(s string, n int) bool {
	if !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
		return false
	}
	size := 0
	for _, r := range s {
		size += utf16.RuneLen(r)
	}
	return size <= n
}
func ValidateLabel(v LabelRequest) error {
	if strings.TrimSpace(v.Name) == "" || !bounded(v.Name, 1024) {
		return ErrInvalid
	}
	for _, x := range []struct {
		v *string
		n int
	}{{v.Description, 4096}, {v.Color, 64}, {v.Icon, 256}} {
		if x.v != nil && !bounded(*x.v, x.n) {
			return ErrInvalid
		}
	}
	return nil
}
func object(data []byte, allowed ...string) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	t, e := d.Token()
	if e != nil || t != json.Delim('{') {
		return nil, ErrInvalid
	}
	out := map[string]json.RawMessage{}
	keys := map[string]bool{}
	for _, k := range allowed {
		keys[k] = true
	}
	for d.More() {
		t, e = d.Token()
		if e != nil {
			return nil, ErrInvalid
		}
		k, ok := t.(string)
		if !ok || !keys[k] {
			return nil, ErrInvalid
		}
		if _, ok = out[k]; ok {
			return nil, ErrInvalid
		}
		var raw json.RawMessage
		if d.Decode(&raw) != nil {
			return nil, ErrInvalid
		}
		out[k] = raw
	}
	if t, e = d.Token(); e != nil || t != json.Delim('}') {
		return nil, ErrInvalid
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, ErrInvalid
	}
	return out, nil
}
func (v *LabelRequest) UnmarshalJSON(data []byte) error {
	m, e := object(data, "name", "description", "color", "icon")
	if e != nil {
		return e
	}
	if m["name"] == nil || bytes.Equal(bytes.TrimSpace(m["name"]), []byte("null")) {
		return ErrInvalid
	}
	type plain LabelRequest
	var p plain
	if json.Unmarshal(data, &p) != nil {
		return ErrInvalid
	}
	if e = ValidateLabel(LabelRequest(p)); e != nil {
		return e
	}
	*v = LabelRequest(p)
	return nil
}
func (v *AssignmentRequest) UnmarshalJSON(data []byte) error {
	m, e := object(data, "label_id")
	if e != nil || m["label_id"] == nil {
		return ErrInvalid
	}
	type plain AssignmentRequest
	var p plain
	if json.Unmarshal(data, &p) != nil || p.LabelID != nil && !authority.ValidID(*p.LabelID) {
		return ErrInvalid
	}
	*v = AssignmentRequest(p)
	return nil
}
func (v *TenureRequest) UnmarshalJSON(data []byte) error {
	m, e := object(data, "after", "take")
	if e != nil {
		return e
	}
	for _, raw := range m {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return ErrInvalid
		}
	}
	type plain TenureRequest
	p := plain{Take: 100}
	if json.Unmarshal(data, &p) != nil || p.Take < 1 || p.Take > 200 || p.After != "" && !authority.ValidID(p.After) {
		return ErrInvalid
	}
	*v = TenureRequest(p)
	return nil
}
