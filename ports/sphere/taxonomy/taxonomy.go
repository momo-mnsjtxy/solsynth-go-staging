// SPDX-License-Identifier: AGPL-3.0-only
// Source-informed local projections, not verified shared-entity JSON.
package taxonomy

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf16"
)

var (
	ErrPolicyUnverified = errors.New("taxonomy production policy and entity contract unverified")
	ErrDatabaseRequired = errors.New("taxonomy database required")
	ErrInvalid          = errors.New("invalid taxonomy request")
	ErrNotFound         = errors.New("taxonomy record not found")
	ErrForbidden        = errors.New("taxonomy authority denied")
	ErrUnauthenticated  = errors.New("taxonomy actor missing")
)

type Kind string

const (
	Tag      Kind = "tags"
	Category Kind = "categories"
)

func (k Kind) Valid() bool { return k == Tag || k == Category }

// Record is an internal source-owned SQL projection. Audit fields are not
// included in HTTP output: migration columns do not prove entity serialization.
type Record struct {
	Kind                                Kind
	AuditActorID                        string
	AuditAdmin                          bool
	ID, Slug                            string
	Name, Description, OwnerPublisherID *string
	IsProtected, IsEvent                bool
	EventEndsAt                         *time.Time
	Usage                               int64
	CreatedAt, UpdatedAt                time.Time
}

type TagProjection struct {
	ID               string     `json:"id"`
	Slug             string     `json:"slug"`
	Name             *string    `json:"name"`
	Description      *string    `json:"description"`
	OwnerPublisherID *string    `json:"owner_publisher_id"`
	IsProtected      bool       `json:"is_protected"`
	IsEvent          bool       `json:"is_event"`
	EventEndsAt      *time.Time `json:"event_ends_at"`
	Usage            int64      `json:"usage"`
}
type CategoryProjection struct {
	ID    string  `json:"id"`
	Slug  string  `json:"slug"`
	Name  *string `json:"name"`
	Usage int64   `json:"usage"`
}

func (r Record) Projection() any {
	if r.Kind == Category {
		return CategoryProjection{r.ID, r.Slug, r.Name, r.Usage}
	}
	return TagProjection{r.ID, r.Slug, r.Name, r.Description, r.OwnerPublisherID, r.IsProtected, r.IsEvent, r.EventEndsAt, r.Usage}
}

type Query struct {
	Kind                          Kind
	Admin                         bool
	Search, Order                 string
	Offset, Take                  int
	OwnerPublisherID              *string
	IsProtected, IsEvent, Unowned *bool
	Now                           time.Time
}

func (q Query) Normalize() (Query, error) {
	if !q.Kind.Valid() {
		return q, ErrInvalid
	}
	if q.OwnerPublisherID != nil && !ValidID(*q.OwnerPublisherID) {
		return q, ErrInvalid
	}
	if q.Admin {
		q.Search = strings.TrimSpace(q.Search)
		if q.Offset < 0 {
			q.Offset = 0
		}
		if q.Take < 1 {
			q.Take = 1
		}
		if q.Take > 200 {
			q.Take = 200
		}
	} else if q.Offset < 0 || q.Take < 0 {
		return q, ErrInvalid
	}
	return q, nil
}

type Page struct {
	Rows  []Record
	Total int64
}
type QuotaRecord struct {
	ID          string     `json:"id"`
	Slug        string     `json:"slug"`
	Name        *string    `json:"name"`
	Description *string    `json:"description"`
	IsProtected bool       `json:"is_protected"`
	IsEvent     bool       `json:"is_event"`
	EventEndsAt *time.Time `json:"event_ends_at"`
}

// Quota is the explicit service projection. Snake-case naming is a fixture
// convention, not evidence of ResourceQuotaResponse serializer attributes.
type Quota struct {
	Total     int64         `json:"total"`
	Used      int64         `json:"used"`
	Remaining int64         `json:"remaining"`
	Level     int           `json:"level"`
	PerkLevel int           `json:"perk_level"`
	Records   []QuotaRecord `json:"records"`
}

type Change struct {
	Slug, Name, Description *string
	PublisherID             *string
	IsProtected             bool
	IsEvent                 bool
	EndsAt                  *time.Time
}
type Mutation string

const (
	Update   Mutation = "update"
	Claim    Mutation = "claim"
	Assign   Mutation = "assign"
	Unassign Mutation = "unassign"
	Release  Mutation = "release"
	Protect  Mutation = "protect"
	Event    Mutation = "event"
	Delete   Mutation = "delete"
)

type DomainError struct {
	Code, Message string
	Status        int
}

func (e *DomainError) Error() string                 { return e.Message }
func Problem(code, message string, status int) error { return &DomainError{code, message, status} }

// MutationState is gathered inside one fixture database transaction.
// Manager/permission decisions are deliberately external explicit fixtures.
type MutationState struct {
	ProtectedUsed   int64
	PublisherExists bool
}
type Repository interface {
	Subscriptions(context.Context, string, int, int) ([]Subscription, int64, error)
	Subscription(context.Context, Kind, string, string, string, time.Time) (Subscription, bool, error)
	List(context.Context, Query) (Page, error)
	Get(context.Context, Kind, string, bool) (Record, error)
	Create(context.Context, Record) (Record, error)
	Mutate(context.Context, Kind, string, Mutation, Change, func(Record, MutationState) (Record, error)) (Record, error)
	Quota(context.Context, string) ([]QuotaRecord, error)
}

type Subscription struct {
	ID           string  `json:"id"`
	AccountID    string  `json:"account_id"`
	CategoryID   *string `json:"category_id"`
	TagID        *string `json:"tag_id"`
	CollectionID *string `json:"collection_id"`
}

type Grant string

const (
	CategoriesSubscribe Grant = "PostCategoriesSubscribe"
	Moderate            Grant = "PostsModerate"
	CategoriesManage    Grant = "PostCategoriesManage"
	TagsCreate          Grant = "PostsTagsCreate"
	TagsUpdate          Grant = "PostsTagsUpdate"
	TagsClaim           Grant = "PostsTagsClaim"
	TagsAssign          Grant = "PostsTagsAssign"
	TagsProtect         Grant = "PostsTagsProtect"
	TagsEvent           Grant = "PostsTagsEvent"
	TagsDelete          Grant = "PostsTagsDelete"
)

// FixtureAuthority is immutable after construction. It has no token parser and
// no networking. All grants describe test data, never production authority.
type FixtureAuthority struct {
	ActorID             string   `json:"actor_id"`
	PublisherID         string   `json:"publisher_id"`
	PublisherName       string   `json:"publisher_name"`
	ManagerPublisherIDs []string `json:"manager_publisher_ids"`
	Grants              []Grant  `json:"grants"`
	ProtectedTagTotal   int64    `json:"protected_tag_total"`
	Level               int      `json:"level"`
	PerkLevel           int      `json:"perk_level"`
}

func (a FixtureAuthority) Validate() error {
	if a.ActorID != "" && !ValidID(a.ActorID) {
		return ErrInvalid
	}
	if a.PublisherID != "" && (!ValidID(a.PublisherID) || a.ActorID == "") {
		return ErrInvalid
	}
	if a.ProtectedTagTotal < 0 {
		return ErrInvalid
	}
	for _, id := range a.ManagerPublisherIDs {
		if !ValidID(id) || a.ActorID == "" {
			return ErrInvalid
		}
	}
	for _, g := range a.Grants {
		switch g {
		case CategoriesSubscribe, Moderate, CategoriesManage, TagsCreate, TagsUpdate, TagsClaim, TagsAssign, TagsProtect, TagsEvent, TagsDelete:
		default:
			return ErrInvalid
		}
	}
	return nil
}
func (a FixtureAuthority) allows(g Grant) bool {
	for _, v := range a.Grants {
		if v == g {
			return true
		}
	}
	return false
}
func (a FixtureAuthority) manager(id *string) bool {
	if id == nil {
		return false
	}
	for _, v := range a.ManagerPublisherIDs {
		if strings.EqualFold(v, *id) {
			return true
		}
	}
	return false
}
func (a FixtureAuthority) publisher(name string) (string, error) {
	if a.PublisherID == "" || name != "" && name != a.PublisherName {
		return "", Problem("TAG_PUBLISHER_NOT_RESOLVED", "Cannot resolve publisher. Specify one via ?pub= or set a default.", 400)
	}
	return strings.ToLower(a.PublisherID), nil
}

type Service struct {
	repo      Repository
	synthetic bool
	authority FixtureAuthority
	trusted   Authority
	now       func() time.Time
}

func NewService(r Repository) *Service { return &Service{repo: r} }
func NewSyntheticService(r Repository, a FixtureAuthority, now func() time.Time) (*Service, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	a.ActorID = strings.ToLower(a.ActorID)
	a.PublisherID = strings.ToLower(a.PublisherID)
	a.ManagerPublisherIDs = append([]string(nil), a.ManagerPublisherIDs...)
	a.Grants = append([]Grant(nil), a.Grants...)
	if now == nil {
		now = time.Now
	}
	return &Service{repo: r, synthetic: true, authority: a, now: now}, nil
}
func (s *Service) guard(ctx context.Context) error {
	if s == nil || (!s.synthetic && s.trusted == nil) {
		return ErrPolicyUnverified
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.repo == nil {
		return ErrDatabaseRequired
	}
	return nil
}
func (s *Service) List(ctx context.Context, q Query) (Page, error) {
	if err := s.guard(ctx); err != nil {
		return Page{}, err
	}
	if q.Admin {
		if err := s.authorize(ctx, Moderate); err != nil {
			return Page{}, err
		}
	}
	q.Now = s.now().UTC()
	q, err := q.Normalize()
	if err != nil {
		return Page{}, err
	}
	return s.repo.List(ctx, q)
}
func (s *Service) Get(ctx context.Context, k Kind, slug string, admin bool) (Record, error) {
	if err := s.guard(ctx); err != nil {
		return Record{}, err
	}
	if admin {
		if err := s.authorize(ctx, Moderate); err != nil {
			return Record{}, err
		}
	}
	if !k.Valid() {
		return Record{}, ErrInvalid
	}
	return s.repo.Get(ctx, k, slug, admin)
}
func (s *Service) Create(ctx context.Context, k Kind, c Change, admin bool, pub string) (Record, error) {
	if err := s.guard(ctx); err != nil {
		return Record{}, err
	}
	grant := Grant("")
	if k == Category {
		if !admin {
			return Record{}, ErrForbidden
		}
		grant = CategoriesManage
	} else if admin {
		grant = TagsCreate
	}
	if err := s.authorize(ctx, grant); err != nil {
		return Record{}, err
	}
	if !k.Valid() || c.Slug == nil || UTF16Length(*c.Slug) > 128 || tooLong(c.Name, 256) || tooLong(c.Description, 4096) {
		return Record{}, ErrInvalid
	}
	slug := NormalizeSlug(*c.Slug)
	if slug == "" {
		code := "TAG_SLUG_REQUIRED"
		if k == Category {
			code = "CATEGORY_SLUG_REQUIRED"
		}
		return Record{}, Problem(code, "Slug is required.", 400)
	}
	owner := clone(c.PublisherID)
	if owner != nil {
		*owner = strings.ToLower(*owner)
	}
	if k == Tag && !admin {
		id, err := s.publisher(ctx, pub)
		if err != nil {
			return Record{}, err
		}
		owner = &id
	}
	if owner != nil && !ValidID(*owner) {
		return Record{}, ErrInvalid
	}
	id, err := newID()
	if err != nil {
		return Record{}, err
	}
	now := s.now().UTC()
	actor, err := s.actor(ctx)
	if err != nil {
		return Record{}, err
	}
	row := Record{AuditActorID: actor, AuditAdmin: admin, Kind: k, ID: id, Slug: slug, Name: clone(c.Name), CreatedAt: now, UpdatedAt: now}
	if k == Tag {
		row.Description = clone(c.Description)
		row.OwnerPublisherID = clone(owner)
	}
	return s.repo.Create(ctx, row)
}
func (s *Service) Change(ctx context.Context, k Kind, slug string, op Mutation, c Change, admin bool, pub string) (Record, error) {
	if err := s.guard(ctx); err != nil {
		return Record{}, err
	}
	if !k.Valid() {
		return Record{}, ErrInvalid
	}
	var grant Grant
	if k == Category {
		if !admin || op != Update && op != Delete {
			return Record{}, ErrForbidden
		}
		grant = CategoriesManage
	} else if admin {
		switch op {
		case Update:
			grant = TagsUpdate
		case Assign, Unassign:
			grant = TagsAssign
		case Protect:
			grant = TagsProtect
		case Event:
			grant = TagsEvent
		case Delete:
			grant = TagsDelete
		default:
			return Record{}, ErrInvalid
		}
	} else {
		switch op {
		case Update, Release, Protect:
		case Claim:
			grant = TagsClaim
		default:
			return Record{}, ErrForbidden
		}
	}
	if err := s.authorize(ctx, grant); err != nil {
		return Record{}, err
	}
	if tooLong(c.Slug, 128) || tooLong(c.Name, 256) || tooLong(c.Description, 4096) {
		return Record{}, ErrInvalid
	}
	if c.PublisherID != nil && !ValidID(*c.PublisherID) {
		return Record{}, ErrInvalid
	}
	if c.PublisherID != nil {
		v := strings.ToLower(*c.PublisherID)
		c.PublisherID = &v
	}
	if op == Assign && c.PublisherID == nil {
		return Record{}, ErrInvalid
	}
	if !admin && (op == Claim || op == Release || op == Protect) {
		id, err := s.publisher(ctx, pub)
		if err != nil {
			return Record{}, err
		}
		c.PublisherID = &id
	}
	now := s.now().UTC()
	return s.repo.Mutate(ctx, k, slug, op, c, func(row Record, state MutationState) (Record, error) {
		if err := s.authorize(ctx, grant); err != nil {
			return Record{}, err
		}
		err := s.apply(ctx, &row, state, op, c, admin, now)
		return row, err
	})
}
func (s *Service) apply(ctx context.Context, row *Record, state MutationState, op Mutation, c Change, admin bool, now time.Time) error {
	forbidden := func(message string) error { return Problem("FORBIDDEN", message, 403) }
	bad := func(message string) error { return Problem("TAG_"+strings.ToUpper(string(op))+"_FAILED", message, 400) }
	switch op {
	case Update:
		if row.Kind == Tag && !admin {
			if row.OwnerPublisherID == nil {
				return forbidden("This tag has no owner. Only admin can edit it.")
			}
			if err := s.manager(ctx, row.OwnerPublisherID); err != nil {
				return err
			}
		}
		if row.Kind == Category && c.Slug != nil {
			if NormalizeSlug(*c.Slug) == "" {
				return Problem("CATEGORY_SLUG_REQUIRED", "Slug cannot be empty.", 400)
			}
			row.Slug = NormalizeSlug(*c.Slug)
		}
		if c.Name != nil {
			row.Name = clone(c.Name)
		}
		if row.Kind == Tag && c.Description != nil {
			row.Description = clone(c.Description)
		}
	case Claim:
		if row.OwnerPublisherID != nil {
			return bad("This tag is already owned by a publisher.")
		}
		if !state.PublisherExists {
			return bad("Publisher not found.")
		}
		row.OwnerPublisherID = clone(c.PublisherID)
	case Assign:
		if !state.PublisherExists {
			return bad("Publisher not found.")
		}
		row.OwnerPublisherID = clone(c.PublisherID)
	case Release:
		if row.OwnerPublisherID == nil {
			return bad("This tag has no owner.")
		}
		if c.PublisherID == nil || *row.OwnerPublisherID != *c.PublisherID {
			return forbidden("This tag is not owned by the specified publisher.")
		}
		if err := s.manager(ctx, row.OwnerPublisherID); err != nil {
			return err
		}
		row.OwnerPublisherID = nil
		row.IsProtected = false
	case Unassign:
		row.OwnerPublisherID = nil
		row.IsProtected = false
	case Protect:
		if !admin {
			if err := s.manager(ctx, c.PublisherID); err != nil {
				return err
			}
		}
		if row.OwnerPublisherID == nil {
			return Problem("TAG_NO_OWNER", "Tag has no owner. Assign ownership first.", 400)
		}
		if !admin && (c.PublisherID == nil || *row.OwnerPublisherID != *c.PublisherID) {
			return forbidden("This tag is not owned by the specified publisher.")
		}
		if !state.PublisherExists {
			return Problem("TAG_OWNER_PUBLISHER_NOT_FOUND", "Owner publisher not found.", 400)
		}
		if c.IsProtected && !row.IsProtected {
			entitlement, err := s.entitlement(ctx, *row.OwnerPublisherID)
			if err != nil {
				return err
			}
			if state.ProtectedUsed >= entitlement.Total {
				return bad(fmt.Sprintf("Protected tag quota exceeded (%d/%d).", state.ProtectedUsed, entitlement.Total))
			}
		}
		row.IsProtected = c.IsProtected
	case Event:
		if c.IsEvent && c.EndsAt == nil {
			return bad("Event tags must have an end time.")
		}
		if c.IsEvent && !c.EndsAt.After(now) {
			return bad("Event end time must be in the future.")
		}
		row.IsEvent = c.IsEvent
		row.EventEndsAt = nil
		if c.IsEvent {
			v := c.EndsAt.UTC()
			row.EventEndsAt = &v
		}
	case Delete:
	default:
		return ErrInvalid
	}
	row.UpdatedAt = now
	actor, err := s.actor(ctx)
	if err != nil {
		return err
	}
	row.AuditActorID = actor
	row.AuditAdmin = admin
	return nil
}
func (s *Service) Quota(ctx context.Context, pub string) (Quota, error) {
	if err := s.guard(ctx); err != nil {
		return Quota{}, err
	}
	if err := s.authorize(ctx, ""); err != nil {
		return Quota{}, err
	}
	id, err := s.publisher(ctx, pub)
	if err != nil {
		return Quota{}, err
	}
	rows, err := s.repo.Quota(ctx, id)
	if err != nil {
		return Quota{}, err
	}
	entitlement, err := s.entitlement(ctx, id)
	if err != nil {
		return Quota{}, err
	}
	q := Quota{Total: entitlement.Total, Level: entitlement.Level, PerkLevel: entitlement.PerkLevel, Records: rows}
	for _, r := range rows {
		if r.IsProtected {
			q.Used++
		}
	}
	q.Remaining = max(0, q.Total-q.Used)
	return q, nil
}

// ValidateTagUsage mirrors the source service, including its null-publisher
// condition. It is not an authorization boundary and must not be used as one.
func ValidateTagUsage(r Record, publisherID *string, now time.Time) error {
	if r.IsEvent && r.EventEndsAt != nil && !r.EventEndsAt.After(now) {
		return fmt.Errorf("Tag '%s' is an event tag that has expired.", r.Slug)
	}
	if r.IsProtected && r.OwnerPublisherID != nil && publisherID != nil && *r.OwnerPublisherID != *publisherID {
		return fmt.Errorf("Tag '%s' is protected and can only be used by its owner.", r.Slug)
	}
	return nil
}
func IsTagAvailable(r Record, now time.Time) bool {
	return !r.IsEvent || r.EventEndsAt == nil || r.EventEndsAt.After(now)
}
func NormalizeSlug(s string) string { return LowerInvariant(strings.TrimSpace(s)) }
func UTF16Length(s string) int      { return len(utf16.Encode([]rune(s))) }
func tooLong(s *string, n int) bool { return s != nil && UTF16Length(*s) > n }
func clone(s *string) *string {
	if s == nil {
		return nil
	}
	v := *s
	return &v
}
func ValidID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}
func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

// Subscription records are explicit local field projections. Source list
// relationship includes remain a complete-entity production gate.
func (s *Service) Subscriptions(ctx context.Context, offset, take int) ([]Subscription, int64, error) {
	if err := s.guard(ctx); err != nil {
		return nil, 0, err
	}
	if err := s.authorize(ctx, ""); err != nil {
		return nil, 0, err
	}
	if offset < 0 || take < 0 {
		return nil, 0, ErrInvalid
	}
	actor, err := s.actor(ctx)
	if err != nil {
		return nil, 0, err
	}
	return s.repo.Subscriptions(ctx, actor, offset, take)
}
func (s *Service) Subscription(ctx context.Context, k Kind, slug, action string) (Subscription, bool, error) {
	if err := s.guard(ctx); err != nil {
		return Subscription{}, false, err
	}
	grant := Grant("")
	if action != "subscription" {
		grant = CategoriesSubscribe
	}
	if err := s.authorize(ctx, grant); err != nil {
		return Subscription{}, false, err
	}
	if !k.Valid() || action != "subscription" && action != "subscribe" && action != "unsubscribe" {
		return Subscription{}, false, ErrInvalid
	}
	actor, err := s.actor(ctx)
	if err != nil {
		return Subscription{}, false, err
	}
	return s.repo.Subscription(ctx, k, slug, actor, action, s.now().UTC())
}
