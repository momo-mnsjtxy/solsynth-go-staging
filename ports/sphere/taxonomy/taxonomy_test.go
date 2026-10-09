// SPDX-License-Identifier: AGPL-3.0-only
package taxonomy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

const actor = "10000000-0000-0000-0000-000000000001"
const publisher = "10000000-0000-0000-0000-000000000002"
const otherPublisher = "10000000-0000-0000-0000-000000000003"

func ptr[T any](v T) *T { return &v }

var fixedNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func authority() FixtureAuthority {
	return FixtureAuthority{ActorID: actor, PublisherID: publisher, PublisherName: "fixture", ManagerPublisherIDs: []string{publisher}, ProtectedTagTotal: 1, Level: 3, PerkLevel: 2, Grants: []Grant{Moderate, CategoriesManage, CategoriesSubscribe, TagsCreate, TagsUpdate, TagsClaim, TagsAssign, TagsProtect, TagsEvent, TagsDelete}}
}

type memoryRepo struct {
	mu    sync.Mutex
	rows  map[string]Record
	query Query
	calls int
	err   error
}

func repo() *memoryRepo { return &memoryRepo{rows: map[string]Record{}} }
func (m *memoryRepo) List(_ context.Context, q Query) (Page, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.query = q
	if m.err != nil {
		return Page{}, m.err
	}
	rows := []Record{}
	for _, r := range m.rows {
		if r.Kind == q.Kind {
			rows = append(rows, r)
		}
	}
	return Page{Rows: rows, Total: int64(len(rows))}, nil
}
func (m *memoryRepo) Get(_ context.Context, k Kind, slug string, _ bool) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.err != nil {
		return Record{}, m.err
	}
	r, ok := m.rows[string(k)+":"+NormalizeSlug(slug)]
	if !ok {
		return Record{}, ErrNotFound
	}
	return r, nil
}
func (m *memoryRepo) Create(_ context.Context, r Record) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.err != nil {
		return Record{}, m.err
	}
	key := string(r.Kind) + ":" + r.Slug
	if _, exists := m.rows[key]; exists {
		return Record{}, Problem("TAG_CREATE_FAILED", "A tag with this slug already exists.", 400)
	}
	m.rows[key] = r
	return r, nil
}
func (m *memoryRepo) Mutate(_ context.Context, k Kind, slug string, op Mutation, c Change, f func(Record, MutationState) (Record, error)) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.err != nil {
		return Record{}, m.err
	}
	key := string(k) + ":" + NormalizeSlug(slug)
	r, ok := m.rows[key]
	if !ok {
		return Record{}, ErrNotFound
	}
	state := MutationState{PublisherExists: true}
	for _, v := range m.rows {
		if v.IsProtected && v.OwnerPublisherID != nil && r.OwnerPublisherID != nil && *v.OwnerPublisherID == *r.OwnerPublisherID {
			state.ProtectedUsed++
		}
	}
	v, err := f(r, state)
	if err != nil {
		return Record{}, err
	}
	delete(m.rows, key)
	if op != Delete {
		m.rows[string(k)+":"+v.Slug] = v
	}
	return v, nil
}
func (m *memoryRepo) Quota(_ context.Context, id string) ([]QuotaRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	v := []QuotaRecord{}
	for _, r := range m.rows {
		if r.OwnerPublisherID != nil && *r.OwnerPublisherID == id {
			v = append(v, QuotaRecord{ID: r.ID, Slug: r.Slug, IsProtected: r.IsProtected})
		}
	}
	return v, m.err
}
func (m *memoryRepo) Subscriptions(context.Context, string, int, int) ([]Subscription, int64, error) {
	return []Subscription{}, 0, m.err
}
func (m *memoryRepo) Subscription(context.Context, Kind, string, string, string, time.Time) (Subscription, bool, error) {
	return Subscription{ID: actor, AccountID: actor}, false, m.err
}
func service(t *testing.T, m Repository, a FixtureAuthority) *Service {
	t.Helper()
	s, err := NewSyntheticService(m, a, func() time.Time { return fixedNow })
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func create(t *testing.T, s *Service, k Kind, slug string) Record {
	t.Helper()
	r, e := s.Create(context.Background(), k, Change{Slug: ptr(slug), Name: ptr("Name")}, true, "")
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func code(err error) string {
	var e *DomainError
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestProductionServiceFailsClosed(t *testing.T) {
	for _, s := range []*Service{nil, {}, NewService(repo())} {
		ctx := context.Background()
		if _, err := s.List(ctx, Query{Kind: Tag}); !errors.Is(err, ErrPolicyUnverified) {
			t.Fatal(err)
		}
		if _, err := s.Get(ctx, Tag, "x", false); !errors.Is(err, ErrPolicyUnverified) {
			t.Fatal(err)
		}
		if _, err := s.Create(ctx, Tag, Change{}, false, ""); !errors.Is(err, ErrPolicyUnverified) {
			t.Fatal(err)
		}
		if _, err := s.Change(ctx, Tag, "x", Delete, Change{}, true, ""); !errors.Is(err, ErrPolicyUnverified) {
			t.Fatal(err)
		}
		if _, err := s.Quota(ctx, ""); !errors.Is(err, ErrPolicyUnverified) {
			t.Fatal(err)
		}
		if _, _, err := s.Subscriptions(ctx, 0, 20); !errors.Is(err, ErrPolicyUnverified) {
			t.Fatal(err)
		}
		if _, _, err := s.Subscription(ctx, Tag, "x", "subscribe"); !errors.Is(err, ErrPolicyUnverified) {
			t.Fatal(err)
		}
	}
}
func TestAuthorityDenialsBeforeRepository(t *testing.T) {
	m := repo()
	s := service(t, m, FixtureAuthority{})
	if _, e := s.List(context.Background(), Query{Kind: Tag, Admin: true}); !errors.Is(e, ErrUnauthenticated) {
		t.Fatal(e)
	}
	if _, e := s.Create(context.Background(), Tag, Change{Slug: ptr("x")}, false, ""); !errors.Is(e, ErrUnauthenticated) {
		t.Fatal(e)
	}
	a := authority()
	a.Grants = nil
	s = service(t, m, a)
	if _, e := s.Change(context.Background(), Tag, "x", Claim, Change{}, false, ""); !errors.Is(e, ErrForbidden) {
		t.Fatal(e)
	}
	if _, e := s.List(context.Background(), Query{Kind: Category, Admin: true}); !errors.Is(e, ErrForbidden) {
		t.Fatal(e)
	}
	if m.calls != 0 {
		t.Fatal("denied request hit repository")
	}
}
func TestTagLifecycleAndQuota(t *testing.T) {
	m := repo()
	s := service(t, m, authority())
	ctx := context.Background()
	r, e := s.Create(ctx, Tag, Change{Slug: ptr("  MiXeD 雪  "), Description: ptr("description")}, false, "fixture")
	if e != nil || r.Slug != "mixed 雪" || r.OwnerPublisherID == nil || *r.OwnerPublisherID != publisher || !ValidID(r.ID) {
		t.Fatal(r, e)
	}
	r, e = s.Change(ctx, Tag, r.Slug, Update, Change{Name: ptr(""), Description: nil}, false, "")
	if e != nil || r.Name == nil || *r.Name != "" || r.Description == nil || *r.Description != "description" {
		t.Fatal(r, e)
	}
	r, e = s.Change(ctx, Tag, r.Slug, Protect, Change{IsProtected: true}, false, "")
	if e != nil || !r.IsProtected {
		t.Fatal(r, e)
	}
	if _, e = s.Change(ctx, Tag, r.Slug, Protect, Change{IsProtected: true}, false, ""); e != nil {
		t.Fatal("idempotent protection consumed extra slot", e)
	}
	q, e := s.Quota(ctx, "")
	if e != nil || q.Used != 1 || q.Remaining != 0 || q.Total != 1 || len(q.Records) != 1 {
		t.Fatal(q, e)
	}
	other, e := s.Create(ctx, Tag, Change{Slug: ptr("second")}, false, "")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Change(ctx, Tag, other.Slug, Protect, Change{IsProtected: true}, false, ""); code(e) != "TAG_PROTECT_FAILED" {
		t.Fatal(e)
	}
	r, e = s.Change(ctx, Tag, r.Slug, Release, Change{}, false, "")
	if e != nil || r.OwnerPublisherID != nil || r.IsProtected {
		t.Fatal(r, e)
	}
	r, e = s.Change(ctx, Tag, r.Slug, Claim, Change{}, false, "")
	if e != nil || r.OwnerPublisherID == nil {
		t.Fatal(r, e)
	}
	if _, e = s.Change(ctx, Tag, r.Slug, Claim, Change{}, false, ""); code(e) != "TAG_CLAIM_FAILED" {
		t.Fatal(e)
	}
	r, e = s.Change(ctx, Tag, r.Slug, Assign, Change{PublisherID: ptr(otherPublisher)}, true, "")
	if e != nil || *r.OwnerPublisherID != otherPublisher {
		t.Fatal(r, e)
	}
	if _, e = s.Change(ctx, Tag, r.Slug, Update, Change{Name: ptr("bad")}, false, ""); code(e) != "FORBIDDEN" {
		t.Fatal(e)
	}
	r, e = s.Change(ctx, Tag, r.Slug, Unassign, Change{}, true, "")
	if e != nil || r.OwnerPublisherID != nil {
		t.Fatal(r, e)
	}
	if _, e = s.Change(ctx, Tag, r.Slug, Delete, Change{}, true, ""); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Get(ctx, Tag, r.Slug, false); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
}
func TestEventBoundariesAndUsage(t *testing.T) {
	m := repo()
	s := service(t, m, authority())
	r := create(t, s, Tag, "event")
	ctx := context.Background()
	for _, end := range []*time.Time{nil, ptr(fixedNow), ptr(fixedNow.Add(-time.Nanosecond))} {
		if _, err := s.Change(ctx, Tag, r.Slug, Event, Change{IsEvent: true, EndsAt: end}, true, ""); code(err) != "TAG_EVENT_FAILED" {
			t.Fatal(err)
		}
	}
	end := fixedNow.Add(time.Hour)
	r, err := s.Change(ctx, Tag, r.Slug, Event, Change{IsEvent: true, EndsAt: &end}, true, "")
	if err != nil || !r.IsEvent || !r.EventEndsAt.Equal(end) {
		t.Fatal(r, err)
	}
	if !IsTagAvailable(r, fixedNow) || IsTagAvailable(r, end) || ValidateTagUsage(r, nil, end) == nil {
		t.Fatal("event time boundary")
	}
	r, err = s.Change(ctx, Tag, r.Slug, Event, Change{IsEvent: false, EndsAt: &end}, true, "")
	if err != nil || r.EventEndsAt != nil || r.IsEvent {
		t.Fatal(r, err)
	}
	r.IsProtected = true
	r.OwnerPublisherID = ptr(publisher)
	if ValidateTagUsage(r, nil, fixedNow) != nil || ValidateTagUsage(r, ptr(publisher), fixedNow) != nil || ValidateTagUsage(r, ptr(otherPublisher), fixedNow) == nil {
		t.Fatal("source null-publisher guard changed")
	}
}
func TestCategoryCreateUpdateConstraints(t *testing.T) {
	m := repo()
	s := service(t, m, authority())
	r := create(t, s, Category, "  CAT  ")
	if r.Slug != "cat" {
		t.Fatal(r)
	}
	r, e := s.Change(context.Background(), Category, "cat", Update, Change{Slug: ptr(" New "), Name: ptr("")}, true, "")
	if e != nil || r.Slug != "new" || r.Name == nil || *r.Name != "" {
		t.Fatal(r, e)
	}
	if _, e = s.Change(context.Background(), Category, "new", Update, Change{Slug: ptr("  ")}, true, ""); code(e) != "CATEGORY_SLUG_REQUIRED" {
		t.Fatal(e)
	}
	for _, k := range []Kind{Tag, Category} {
		for _, c := range []Change{{Slug: nil}, {Slug: ptr(strings.Repeat("x", 129))}, {Slug: ptr("x"), Name: ptr(strings.Repeat("🙂", 129))}, {Slug: ptr("x"), Description: ptr(strings.Repeat("x", 4097))}} {
			if _, err := s.Create(context.Background(), k, c, true, ""); !errors.Is(err, ErrInvalid) {
				t.Fatal(k, c, err)
			}
		}
	}
	if UTF16Length("🙂雪") != 3 {
		t.Fatal("UTF-16 constraints")
	}
}
func TestQuerySemantics(t *testing.T) {
	for _, k := range []Kind{Tag, Category} {
		q, e := (Query{Kind: k, Admin: true, Search: "  x  ", Offset: -5, Take: 300}).Normalize()
		if e != nil || q.Search != "x" || q.Take != 200 || q.Offset != 0 {
			t.Fatal(q, e)
		}
	}
	q, e := (Query{Kind: Tag, Search: "  x  ", Take: 0}).Normalize()
	if e != nil || q.Search != "  x  " || q.Take != 0 {
		t.Fatal(q, e)
	}
	for _, q := range []Query{{Kind: Tag, Take: -1}, {Kind: Tag, Offset: -1}, {Kind: "unknown"}, {Kind: Tag, OwnerPublisherID: ptr("bad")}} {
		if _, e = q.Normalize(); !errors.Is(e, ErrInvalid) {
			t.Fatal(q, e)
		}
	}
}
func TestServiceCancellationAndAuthorityCopy(t *testing.T) {
	m := repo()
	a := authority()
	s := service(t, m, a)
	a.Grants[0] = "bad"
	a.ManagerPublisherIDs[0] = otherPublisher
	if _, err := s.List(context.Background(), Query{Kind: Tag, Admin: true}); err != nil {
		t.Fatal(err)
	}
	if !s.authority.manager(ptr(publisher)) {
		t.Fatal("authority slices alias caller")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.List(ctx, Query{Kind: Tag}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := s.Create(context.Background(), Tag, Change{Slug: ptr(fmt.Sprintf("race-%d", i))}, true, ""); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
}
func TestInvalidAuthorityFixtures(t *testing.T) {
	for _, a := range []FixtureAuthority{{ActorID: "bad"}, {PublisherID: publisher}, {ActorID: actor, ManagerPublisherIDs: []string{"bad"}}, {Grants: []Grant{"superuser"}}, {ProtectedTagTotal: -1}} {
		if _, e := NewSyntheticService(repo(), a, nil); e == nil {
			t.Fatal(a)
		}
	}
}
