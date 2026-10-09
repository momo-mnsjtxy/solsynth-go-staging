// SPDX-License-Identifier: AGPL-3.0-only
package activities

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/artwork"
)

const ManagePermission = "presences.activities.manage"

// Public is source-proven by the licensed Solian parser test. Private/Friends
// are explicitly owned encodings pending a lawful full shared-enum contract.
const (
	Private = 0
	Public  = 1
	Friends = 2
)

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (e *Failure) Error() string { return e.Code }

var (
	ErrInvalid         = &Failure{"VALIDATION_ERROR", "Invalid activity request.", 400}
	ErrNotFound        = &Failure{"PASSPORT_ACTIVITY_NOT_FOUND", "Activity not found.", 404}
	ErrAccountNotFound = &Failure{"PASSPORT_ACCOUNT_NOT_FOUND", "Account not found.", 404}
	ErrUnavailable     = &Failure{"ACTIVITIES_UNAVAILABLE", "Activities are unavailable.", 503}
	ErrConflict        = &Failure{"ACTIVITY_SESSION_CONFLICT", "End the existing session before changing its catalog identity.", 409}
	ErrArtwork         = &Failure{"PASSPORT_ACTIVITY_ARTWORK_NOT_FOUND", "Activity artwork is unavailable.", 400}
)

type Tag struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}
type Content struct {
	Type           *string         `json:"type"`
	Provider       *string         `json:"provider"`
	ReferenceID    *string         `json:"reference_id"`
	Title          *string         `json:"title"`
	Subtitle       *string         `json:"subtitle"`
	Caption        *string         `json:"caption"`
	LargeImage     *string         `json:"large_image"`
	SmallImage     *string         `json:"small_image"`
	TitleURL       *string         `json:"title_url"`
	SubtitleURL    *string         `json:"subtitle_url"`
	QueryableTerms []string        `json:"queryable_terms"`
	Meta           json.RawMessage `json:"meta"`
	Tags           []Tag           `json:"tags"`
}
type Request struct {
	VisibilitySpecified bool `json:"-"`
	Content
	ManualID     *string `json:"manual_id"`
	LeaseMinutes int     `json:"lease_minutes"`
	Visibility   int     `json:"visibility"`
	CatalogKey   *string `json:"catalog_key"`
	CatalogName  *string `json:"catalog_name"`
}
type Activity struct {
	Content
	ID             string     `json:"id"`
	AccountID      string     `json:"account_id"`
	ManualID       *string    `json:"manual_id"`
	CatalogID      *string    `json:"catalog_id"`
	Visibility     int        `json:"visibility"`
	LeaseMinutes   int        `json:"lease_minutes"`
	LeaseExpiresAt time.Time  `json:"lease_expires_at"`
	StartedAt      *time.Time `json:"started_at"`
	EndedAt        *time.Time `json:"ended_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	DeletedAt      *time.Time `json:"deleted_at"`
}
type Catalog struct {
	Content
	ID           string     `json:"id"`
	AccountID    string     `json:"account_id"`
	CatalogKey   string     `json:"catalog_key"`
	Name         *string    `json:"name"`
	Visibility   int        `json:"visibility"`
	TotalSeconds int64      `json:"total_seconds"`
	SessionCount int64      `json:"session_count"`
	LastActiveAt *time.Time `json:"last_active_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	DeletedAt    *time.Time `json:"deleted_at"`
}
type Filter struct {
	IncludeExpired                                      bool
	Offset, Take                                        int
	Query, Type, Provider, ReferenceID, Term, CatalogID string
	Tags                                                []string
	Visibility                                          *int
	From, To                                            *time.Time
	IsActive                                            *bool
	Filtered                                            bool
}
type Repository interface {
	List(context.Context, string, string, Filter) ([]Activity, int, error)
	Set(context.Context, string, Request, bool, string) (Activity, error)
	End(context.Context, string, string) (Activity, error)
	Delete(context.Context, string, string, string) error
	Catalog(context.Context, string, Filter) ([]Catalog, error)
	Stats(context.Context, string, Filter, bool) (map[string]int64, error)
}
type Authority interface {
	AuthenticateRequest(*http.Request) (authority.Principal, error)
}

func Normalize(v *Request, session bool) error {
	if v.LeaseMinutes < 1 || v.LeaseMinutes > 60 || v.Visibility < Private || v.Visibility > Friends {
		return ErrInvalid
	}
	for _, x := range []struct {
		p           **string
		n           int
		trim, lower bool
	}{
		{&v.Type, 256, true, false}, {&v.ManualID, 4096, false, false}, {&v.Provider, 256, true, true}, {&v.ReferenceID, 1024, true, false},
		{&v.Title, 4096, false, false}, {&v.Subtitle, 4096, false, false}, {&v.Caption, 4096, false, false},
		{&v.LargeImage, 4096, true, false}, {&v.SmallImage, 4096, true, false}, {&v.TitleURL, 4096, true, false}, {&v.SubtitleURL, 4096, true, false},
		{&v.CatalogKey, 1024, true, false}, {&v.CatalogName, 4096, false, false},
	} {
		if *x.p != nil {
			s := **x.p
			if !utf8.ValidString(s) || strings.ContainsRune(s, 0) || utf8.RuneCountInString(s) > x.n {
				return ErrInvalid
			}
			if x.trim {
				s = strings.TrimSpace(s)
			}
			if x.lower {
				s = strings.ToLower(s)
			}
			if s == "" && x.trim {
				*x.p = nil
			} else {
				*x.p = &s
			}
		}
	}
	if session && (v.ManualID == nil || strings.TrimSpace(*v.ManualID) == "") {
		return ErrInvalid
	}
	if len(v.QueryableTerms) > 64 || len(v.Tags) > 64 || len(v.Meta) > 16384 {
		return ErrInvalid
	}
	terms := []string{}
	seen := map[string]bool{}
	for _, term := range v.QueryableTerms {
		term = strings.ToLower(strings.TrimSpace(term))
		if len(term) > 256 || strings.ContainsRune(term, 0) {
			return ErrInvalid
		}
		if term != "" && !seen[term] {
			seen[term] = true
			terms = append(terms, term)
		}
	}
	if v.QueryableTerms != nil {
		v.QueryableTerms = terms
	}
	tags := []Tag{}
	seen = map[string]bool{}
	for _, tag := range v.Tags {
		tag.Slug = strings.TrimSpace(tag.Slug)
		if len(tag.Slug) > 256 || len(tag.Name) > 256 || strings.ContainsRune(tag.Slug+tag.Name, 0) {
			return ErrInvalid
		}
		if tag.Slug != "" && !seen[tag.Slug] {
			seen[tag.Slug] = true
			tags = append(tags, tag)
		}
	}
	if v.Tags != nil {
		v.Tags = tags
	}
	if len(v.Meta) > 0 && string(v.Meta) != "null" {
		var meta map[string]json.RawMessage
		if json.Unmarshal(v.Meta, &meta) != nil || meta == nil {
			return ErrInvalid
		}
	}
	for _, p := range []*string{v.LargeImage, v.SmallImage} {
		if p == nil {
			continue
		}
		if strings.HasPrefix(strings.ToLower(*p), "sha256:") {
			s, e := artwork.NormalizeHash(*p)
			if e != nil {
				return ErrInvalid
			}
			*p = s
			continue
		}
		if !safeURL(*p) && !strings.HasPrefix(*p, "discord:") {
			return ErrInvalid
		}
	}
	for _, p := range []*string{v.TitleURL, v.SubtitleURL} {
		if p != nil && !safeURL(*p) {
			return ErrInvalid
		}
	}
	return nil
}
func safeURL(s string) bool {
	u, e := url.Parse(s)
	return e == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}
func NormalizeContent(v *Content) {
	if v.QueryableTerms == nil {
		v.QueryableTerms = []string{}
	}
	if v.Tags == nil {
		v.Tags = []Tag{}
	}
	if len(v.Meta) == 0 {
		v.Meta = json.RawMessage(`null`)
	}
}

// Catalogs name an accumulated object. The authored EF/catalog service has no
// activity type/title fields; the common internal document is never a reason
// to expose those extra fields on the catalog wire projection.
func (c Catalog) MarshalJSON() ([]byte, error) {
	type plain Catalog
	raw, err := json.Marshal(plain(c))
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	delete(fields, "type")
	delete(fields, "title")
	return json.Marshal(fields)
}
