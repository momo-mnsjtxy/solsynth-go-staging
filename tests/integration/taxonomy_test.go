// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/taxonomypg"
	"solsynth.local/go-migration/ports/sphere/taxonomy"
)

var taxonomyNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

const taxonomyPublic = int32(42)

func taxonomyPtr[T any](v T) *T { return &v }
func taxonomyAuthority(group int) taxonomy.FixtureAuthority {
	owner := watcherID(group, 2)
	return taxonomy.FixtureAuthority{ActorID: watcherID(group, 1), PublisherID: owner, PublisherName: "fixture", ManagerPublisherIDs: []string{owner}, ProtectedTagTotal: 1, Grants: []taxonomy.Grant{taxonomy.Moderate, taxonomy.CategoriesManage, taxonomy.CategoriesSubscribe, taxonomy.TagsCreate, taxonomy.TagsUpdate, taxonomy.TagsClaim, taxonomy.TagsAssign, taxonomy.TagsProtect, taxonomy.TagsEvent, taxonomy.TagsDelete}}
}
func taxonomyService(t *testing.T, db *sql.DB, a taxonomy.FixtureAuthority) *taxonomy.Service {
	t.Helper()
	s, e := taxonomy.NewSyntheticService(taxonomypg.NewSyntheticRepository(db, taxonomyPublic), a, func() time.Time { return taxonomyNow })
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func taxonomyPublisher(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	query(t, db, "INSERT INTO taxonomy_candidate.publishers(id) VALUES($1)", id)
}
func taxonomyCreate(t *testing.T, s *taxonomy.Service, k taxonomy.Kind, slug string, owner *string) taxonomy.Record {
	t.Helper()
	v, e := s.Create(context.Background(), k, taxonomy.Change{Slug: &slug, PublisherID: owner}, true, "")
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func taxonomyHTTP(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	h.ServeHTTP(w, r)
	return w
}
func taxonomyCode(e error) string {
	var p *taxonomy.DomainError
	if errors.As(e, &p) {
		return p.Code
	}
	return ""
}

func TestPGTaxonomyListSearchOrder(t *testing.T) {
	db := database(t)
	a := taxonomyAuthority(0xb1000000)
	s := taxonomyService(t, db, a)
	ctx := context.Background()
	for _, k := range []taxonomy.Kind{taxonomy.Tag, taxonomy.Category} {
		prefix := "b1-" + string(k) + "-"
		cold := taxonomyCreate(t, s, k, prefix+"cold", nil)
		popular := taxonomyCreate(t, s, k, prefix+"popular", nil)
		tbl, link, key := "post_tags", "post_tag_links", "tags_id"
		if k == taxonomy.Category {
			tbl, link, key = "post_categories", "post_category_links", "categories_id"
		}
		query(t, db, "UPDATE taxonomy_candidate."+tbl+" SET name='NameOnly',created_at=$2,updated_at=$2 WHERE id=$1", cold.ID, taxonomyNow.Add(-time.Hour))
		query(t, db, "UPDATE taxonomy_candidate."+tbl+" SET name='Alpha',created_at=$2,updated_at=$2 WHERE id=$1", popular.ID, taxonomyNow)
		// Cold has more total usage; popularity ignores drafts, private, future,
		// older-than-seven-day, and deleted posts and includes both exact bounds.
		for i := 0; i < 9; i++ {
			n := i
			if k == taxonomy.Category {
				n += 30
			}
			id := watcherID(0xb1000000, 10+n)
			stamp := taxonomyNow.Add(-8 * 24 * time.Hour)
			visibility := int32(43)
			var drafted any
			if i < 2 {
				stamp = taxonomyNow.Add(-7 * 24 * time.Hour)
				if i == 1 {
					stamp = taxonomyNow
				}
				visibility = taxonomyPublic
			}
			if i == 2 {
				stamp = taxonomyNow.Add(time.Second)
				visibility = taxonomyPublic
			}
			if i == 3 {
				stamp = taxonomyNow.Add(-time.Hour)
				visibility = taxonomyPublic
				drafted = stamp
			}
			if i == 4 {
				stamp = taxonomyNow.Add(-time.Hour)
			}
			var deleted any
			if i == 8 {
				deleted = taxonomyNow
			}
			query(t, db, "INSERT INTO taxonomy_candidate.posts(id,created_at,drafted_at,visibility,deleted_at) VALUES($1,$2,$3,$4,$5)", id, stamp, drafted, visibility, deleted)
			target := cold.ID
			if i < 2 {
				target = popular.ID
			}
			query(t, db, "INSERT INTO taxonomy_candidate."+link+"("+key+",posts_id) VALUES($1,$2)", target, id)
		}
		page, e := s.List(ctx, taxonomy.Query{Kind: k, Search: prefix, Order: "usage", Take: 20})
		if e != nil || page.Total != 2 || len(page.Rows) != 2 || page.Rows[0].ID != cold.ID || page.Rows[0].Usage != 6 {
			t.Fatal(page, e)
		}
		page, e = s.List(ctx, taxonomy.Query{Kind: k, Search: prefix, Order: "popularity", Take: 20})
		if e != nil || page.Rows[0].ID != popular.ID || page.Rows[0].Usage != 2 {
			t.Fatal(page, e)
		}
		page, e = s.List(ctx, taxonomy.Query{Kind: k, Search: prefix, Take: 20})
		if e != nil || page.Rows[0].ID != popular.ID {
			t.Fatal(page, e)
		}
		page, e = s.List(ctx, taxonomy.Query{Kind: k, Search: "NameOnly", Take: 20})
		if e != nil || page.Total != 0 {
			t.Fatal("public search must use only slug", page, e)
		}
		page, e = s.List(ctx, taxonomy.Query{Kind: k, Admin: true, Search: "  NameOnly  ", Take: 50})
		if e != nil || page.Total < 1 {
			t.Fatal(page, e)
		}
		page, e = s.List(ctx, taxonomy.Query{Kind: k, Search: prefix, Offset: 1000, Take: 20})
		if e != nil || page.Total != 2 || len(page.Rows) != 0 || page.Rows == nil {
			t.Fatal(page, e)
		}
		page, e = s.List(ctx, taxonomy.Query{Kind: k, Search: prefix, Take: 0})
		if e != nil || page.Total != 2 || len(page.Rows) != 0 {
			t.Fatal(page, e)
		}
		got, e := s.Get(ctx, k, strings.ToUpper(cold.Slug), true)
		if e != nil || got.Usage != 6 {
			t.Fatal(got, e)
		}
		got, e = s.Get(ctx, k, cold.Slug, false)
		if e != nil || got.Usage != 0 {
			t.Fatal("public detail must not invent list Usage", got, e)
		}
		if _, e = s.Get(ctx, k, " "+cold.Slug+" ", false); (k == taxonomy.Tag && e != nil) || (k == taxonomy.Category && !errors.Is(e, taxonomy.ErrNotFound)) {
			t.Fatal(k, e)
		}
		h := taxonomy.NewSyntheticHandler(s)
		w := taxonomyHTTP(h, "GET", "/api/posts/"+string(k)+"?query="+prefix+"&take=0", "")
		if w.Code != 200 || w.Header().Get("X-Total") != "2" || strings.TrimSpace(w.Body.String()) != "[]" {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
func TestPGTaxonomyAdminFiltersAndTombstones(t *testing.T) {
	db := database(t)
	a := taxonomyAuthority(0xb2000000)
	taxonomyPublisher(t, db, a.PublisherID)
	s := taxonomyService(t, db, a)
	owned := taxonomyCreate(t, s, taxonomy.Tag, "b2-owned", &a.PublisherID)
	unowned := taxonomyCreate(t, s, taxonomy.Tag, "b2-unowned", nil)
	deleted := taxonomyCreate(t, s, taxonomy.Tag, "b2-deleted", nil)
	query(t, db, "UPDATE taxonomy_candidate.post_tags SET is_protected=true,is_event=true,description='DescriptionOnly' WHERE id=$1", owned.ID)
	query(t, db, "UPDATE taxonomy_candidate.post_tags SET deleted_at=$2 WHERE id=$1", deleted.ID, taxonomyNow)
	for _, q := range []taxonomy.Query{{OwnerPublisherID: &a.PublisherID}, {IsProtected: taxonomyPtr(true)}, {IsEvent: taxonomyPtr(true)}, {Search: "DescriptionOnly"}} {
		q.Kind = taxonomy.Tag
		q.Admin = true
		q.Take = 50
		if q.Search == "" {
			q.Search = "b2-"
		}
		p, e := s.List(context.Background(), q)
		if e != nil || p.Total != 1 || p.Rows[0].ID != owned.ID {
			t.Fatal(q, p, e)
		}
	}
	p, e := s.List(context.Background(), taxonomy.Query{Kind: taxonomy.Tag, Admin: true, Search: "b2-", Unowned: taxonomyPtr(true), Take: 50})
	if e != nil || p.Total != 1 || p.Rows[0].ID != unowned.ID {
		t.Fatal(p, e)
	}
	p, e = s.List(context.Background(), taxonomy.Query{Kind: taxonomy.Tag, Admin: true, Search: "b2-", Unowned: taxonomyPtr(true), OwnerPublisherID: &a.PublisherID, Take: 50})
	if e != nil || p.Total != 0 {
		t.Fatal(p, e)
	}
	if _, e = s.Get(context.Background(), taxonomy.Tag, deleted.Slug, false); !errors.Is(e, taxonomy.ErrNotFound) {
		t.Fatal(e)
	}
	// Active-only duplicate behavior is a candidate global filter policy.
	taxonomyCreate(t, s, taxonomy.Tag, deleted.Slug, nil)
}
func TestPGTaxonomyCreateUpdateConstraints(t *testing.T) {
	db := database(t)
	a := taxonomyAuthority(0xb3000000)
	taxonomyPublisher(t, db, a.PublisherID)
	s := taxonomyService(t, db, a)
	h := taxonomy.NewSyntheticHandler(s)
	w := taxonomyHTTP(h, "POST", "/api/posts/tags?pub=fixture", `{"slug":" B3-Created ","name":"Hello","description":"Initial"}`)
	if w.Code != 201 || w.Header().Get("Location") != "/api/posts/tags/b3-created" {
		t.Fatal(w.Code, w.Body.String())
	}
	var tag taxonomy.TagProjection
	if e := json.Unmarshal(w.Body.Bytes(), &tag); e != nil || tag.OwnerPublisherID == nil || *tag.OwnerPublisherID != a.PublisherID {
		t.Fatal(tag, e)
	}
	w = taxonomyHTTP(h, "PATCH", "/api/posts/tags/b3-created", `{"name":null,"description":""}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	r, e := s.Get(context.Background(), taxonomy.Tag, "B3-CREATED", false)
	if e != nil || r.Name == nil || *r.Name != "Hello" || r.Description == nil || *r.Description != "" {
		t.Fatal(r, e)
	}
	w = taxonomyHTTP(h, "POST", "/api/admin/tags", `{"slug":"B3-CREATED"}`)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "TAG_CREATE_FAILED") {
		t.Fatal(w.Code, w.Body.String())
	}
	cat := taxonomyCreate(t, s, taxonomy.Category, " b3-category ", nil)
	taken := taxonomyCreate(t, s, taxonomy.Category, "b3-taken", nil)
	_, e = s.Change(context.Background(), taxonomy.Category, cat.Slug, taxonomy.Update, taxonomy.Change{Slug: &taken.Slug}, true, "")
	if taxonomyCode(e) != "CATEGORY_SLUG_EXISTS" {
		t.Fatal(e)
	}
	if _, e = s.Get(context.Background(), taxonomy.Category, cat.Slug, true); e != nil {
		t.Fatal("duplicate rename did not roll back", e)
	}
	_, e = s.Change(context.Background(), taxonomy.Category, cat.Slug, taxonomy.Update, taxonomy.Change{Slug: taxonomyPtr(" B3-Renamed "), Name: taxonomyPtr("")}, true, "")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Get(context.Background(), taxonomy.Category, cat.Slug, false); !errors.Is(e, taxonomy.ErrNotFound) {
		t.Fatal(e)
	}
	for _, payload := range []string{`{"slug":" "}`, `{"slug":"` + strings.Repeat("x", 129) + `"}`, `{"slug":"long-name","name":"` + strings.Repeat("🙂", 129) + `"}`} {
		if w := taxonomyHTTP(h, "POST", "/api/admin/categories", payload); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	var created, updated time.Time
	if e = db.QueryRow("SELECT created_at,updated_at FROM taxonomy_candidate.post_tags WHERE id=$1", tag.ID).Scan(&created, &updated); e != nil || !created.Equal(taxonomyNow) || !updated.Equal(taxonomyNow) {
		t.Fatal(created, updated, e)
	}
}
func TestPGTaxonomyOwnershipQuotaEvent(t *testing.T) {
	db := database(t)
	a := taxonomyAuthority(0xb4000000)
	taxonomyPublisher(t, db, a.PublisherID)
	taxonomyPublisher(t, db, watcherID(0xb4000000, 3))
	s := taxonomyService(t, db, a)
	ctx := context.Background()
	r := taxonomyCreate(t, s, taxonomy.Tag, "b4-owned", nil)
	if _, e := s.Change(ctx, taxonomy.Tag, r.Slug, taxonomy.Update, taxonomy.Change{Name: taxonomyPtr("bad")}, false, ""); taxonomyCode(e) != "FORBIDDEN" {
		t.Fatal(e)
	}
	r, e := s.Change(ctx, taxonomy.Tag, r.Slug, taxonomy.Claim, taxonomy.Change{}, false, "")
	if e != nil || r.OwnerPublisherID == nil {
		t.Fatal(r, e)
	}
	r, e = s.Change(ctx, taxonomy.Tag, r.Slug, taxonomy.Protect, taxonomy.Change{IsProtected: true}, false, "")
	if e != nil || !r.IsProtected {
		t.Fatal(r, e)
	}
	if _, e = s.Change(ctx, taxonomy.Tag, r.Slug, taxonomy.Protect, taxonomy.Change{IsProtected: true}, false, ""); e != nil {
		t.Fatal(e)
	}
	second := taxonomyCreate(t, s, taxonomy.Tag, "b4-second", &a.PublisherID)
	if _, e = s.Change(ctx, taxonomy.Tag, second.Slug, taxonomy.Protect, taxonomy.Change{IsProtected: true}, false, ""); taxonomyCode(e) != "TAG_PROTECT_FAILED" {
		t.Fatal(e)
	}
	q, e := s.Quota(ctx, "")
	if e != nil || q.Used != 1 || q.Total != 1 || q.Remaining != 0 || len(q.Records) != 2 || q.Records[0].ID != r.ID {
		t.Fatal(q, e)
	}
	for _, end := range []*time.Time{nil, taxonomyPtr(taxonomyNow), taxonomyPtr(taxonomyNow.Add(-time.Hour))} {
		if _, e = s.Change(ctx, taxonomy.Tag, r.Slug, taxonomy.Event, taxonomy.Change{IsEvent: true, EndsAt: end}, true, ""); taxonomyCode(e) != "TAG_EVENT_FAILED" {
			t.Fatal(e)
		}
	}
	end := taxonomyNow.Add(time.Hour)
	r, e = s.Change(ctx, taxonomy.Tag, r.Slug, taxonomy.Event, taxonomy.Change{IsEvent: true, EndsAt: &end}, true, "")
	if e != nil || r.EventEndsAt == nil || !r.EventEndsAt.Equal(end) {
		t.Fatal(r, e)
	}
	r, e = s.Change(ctx, taxonomy.Tag, r.Slug, taxonomy.Event, taxonomy.Change{}, true, "")
	if e != nil || r.EventEndsAt != nil || r.IsEvent {
		t.Fatal(r, e)
	}
	r, e = s.Change(ctx, taxonomy.Tag, r.Slug, taxonomy.Release, taxonomy.Change{}, false, "")
	if e != nil || r.IsProtected || r.OwnerPublisherID != nil {
		t.Fatal(r, e)
	}
	r, e = s.Change(ctx, taxonomy.Tag, r.Slug, taxonomy.Assign, taxonomy.Change{PublisherID: taxonomyPtr(watcherID(0xb4000000, 3))}, true, "")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Change(ctx, taxonomy.Tag, r.Slug, taxonomy.Release, taxonomy.Change{}, false, ""); taxonomyCode(e) != "FORBIDDEN" {
		t.Fatal(e)
	}
	if _, e = s.Change(ctx, taxonomy.Tag, r.Slug, taxonomy.Assign, taxonomy.Change{PublisherID: taxonomyPtr(watcherID(0xb4000000, 99))}, true, ""); taxonomyCode(e) != "TAG_ASSIGN_FAILED" {
		t.Fatal(e)
	}
	r, e = s.Change(ctx, taxonomy.Tag, r.Slug, taxonomy.Unassign, taxonomy.Change{}, true, "")
	if e != nil || r.OwnerPublisherID != nil {
		t.Fatal(r, e)
	}
}

func TestPGTaxonomyTransactionalAudit(t *testing.T) {
	db := database(t)
	a := taxonomyAuthority(0xba000000)
	s := taxonomyService(t, db, a)
	ctx := context.Background()
	row := taxonomyCreate(t, s, taxonomy.Tag, "ba-audit", nil)
	if _, e := s.Change(ctx, taxonomy.Tag, row.Slug, taxonomy.Update, taxonomy.Change{Name: taxonomyPtr("recorded")}, true, ""); e != nil {
		t.Fatal(e)
	}
	var events int
	if e := db.QueryRow("SELECT count(*) FROM taxonomy_candidate.actions WHERE resource_id=$1 AND actor_id=$2 AND is_admin", row.ID, a.ActorID).Scan(&events); e != nil || events != 2 {
		t.Fatal(events, e)
	}
	// A real ledger constraint error must roll back the preceding entity update.
	query(t, db, "ALTER TABLE taxonomy_candidate.actions ADD CONSTRAINT ba_fixture_audit_fault CHECK (resource_id <> '"+row.ID+"' OR operation <> 'update') NOT VALID")
	if _, e := s.Change(ctx, taxonomy.Tag, row.Slug, taxonomy.Update, taxonomy.Change{Name: taxonomyPtr("must-roll-back")}, true, ""); e == nil {
		t.Fatal("audit failure accepted")
	}
	query(t, db, "ALTER TABLE taxonomy_candidate.actions DROP CONSTRAINT ba_fixture_audit_fault")
	got, e := s.Get(ctx, taxonomy.Tag, row.Slug, false)
	if e != nil || got.Name == nil || *got.Name != "recorded" {
		t.Fatal(got, e)
	}
	if e := db.QueryRow("SELECT count(*) FROM taxonomy_candidate.actions WHERE resource_id=$1", row.ID).Scan(&events); e != nil || events != 2 {
		t.Fatal(events, e)
	}
}

func TestPGTaxonomyConcurrentClaimAndQuota(t *testing.T) {
	db := database(t)
	a := taxonomyAuthority(0xb5000000)
	taxonomyPublisher(t, db, a.PublisherID)
	s := taxonomyService(t, db, a)
	ctx := context.Background()
	tag := taxonomyCreate(t, s, taxonomy.Tag, "b5-claim", nil)
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := s.Change(ctx, taxonomy.Tag, tag.Slug, taxonomy.Claim, taxonomy.Change{}, false, "")
			if e == nil {
				wins.Add(1)
			} else if taxonomyCode(e) != "TAG_CLAIM_FAILED" {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal(wins.Load())
	}
	tags := []taxonomy.Record{tag, taxonomyCreate(t, s, taxonomy.Tag, "b5-second", &a.PublisherID)}
	wins.Store(0)
	for _, row := range tags {
		wg.Add(1)
		go func(slug string) {
			defer wg.Done()
			_, e := s.Change(ctx, taxonomy.Tag, slug, taxonomy.Protect, taxonomy.Change{IsProtected: true}, false, "")
			if e == nil {
				wins.Add(1)
			} else if taxonomyCode(e) != "TAG_PROTECT_FAILED" {
				t.Error(e)
			}
		}(row.Slug)
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal(wins.Load())
	}
	q, e := s.Quota(ctx, "")
	if e != nil || q.Used != 1 {
		t.Fatal(q, e)
	}
}
func TestPGTaxonomySubscriptionsAndDeleteRollback(t *testing.T) {
	db := database(t)
	a := taxonomyAuthority(0xb6000000)
	s := taxonomyService(t, db, a)
	ctx := context.Background()
	tag := taxonomyCreate(t, s, taxonomy.Tag, "b6-tag", nil)
	cat := taxonomyCreate(t, s, taxonomy.Category, "b6-category", nil)
	for _, row := range []taxonomy.Record{tag, cat} {
		sub, created, e := s.Subscription(ctx, row.Kind, strings.ToUpper(row.Slug), "subscribe")
		if e != nil || !created || sub.AccountID != a.ActorID {
			t.Fatal(sub, created, e)
		}
		repeated, created, e := s.Subscription(ctx, row.Kind, row.Slug, "subscribe")
		if e != nil || created || repeated.ID != sub.ID {
			t.Fatal(repeated, created, e)
		}
		got, _, e := s.Subscription(ctx, row.Kind, row.Slug, "subscription")
		if e != nil || got.ID != sub.ID {
			t.Fatal(got, e)
		}
	}
	b := a
	b.ActorID = watcherID(0xb6000000, 4)
	other := taxonomyService(t, db, b)
	if _, _, e := other.Subscription(ctx, taxonomy.Tag, tag.Slug, "subscription"); taxonomyCode(e) != "TAG_SUBSCRIPTION_NOT_FOUND" {
		t.Fatal(e)
	}
	if list, total, e := other.Subscriptions(ctx, 0, 20); e != nil || len(list) != 0 || total != 0 {
		t.Fatal(list, total, e)
	}
	if list, total, e := s.Subscriptions(ctx, 1, 1); e != nil || len(list) != 0 || total != 2 {
		t.Fatal("Take then Skip semantics", list, total, e)
	}
	post := watcherID(0xb6000000, 10)
	query(t, db, "INSERT INTO taxonomy_candidate.posts(id,created_at,visibility) VALUES($1,$2,$3)", post, taxonomyNow, taxonomyPublic)
	query(t, db, "INSERT INTO taxonomy_candidate.post_tag_links(posts_id,tags_id) VALUES($1,$2)", post, tag.ID)
	query(t, db, "INSERT INTO taxonomy_candidate.post_category_links(posts_id,categories_id) VALUES($1,$2)", post, cat.ID)
	// Deliberate fixture fault verifies rollback without inferring inherited
	// source deletion hooks from relational metadata.
	query(t, db, "ALTER TABLE taxonomy_candidate.post_tags ADD CONSTRAINT b6_fixture_delete_fault CHECK (slug <> 'b6-tag' OR deleted_at IS NULL)")
	if _, e := s.Change(ctx, taxonomy.Tag, tag.Slug, taxonomy.Delete, taxonomy.Change{}, true, ""); e == nil {
		t.Fatal("deliberate fixture fault did not reject deletion")
	}
	query(t, db, "ALTER TABLE taxonomy_candidate.post_tags DROP CONSTRAINT b6_fixture_delete_fault")
	var links int
	if e := db.QueryRow("SELECT count(*) FROM taxonomy_candidate.post_tag_links WHERE tags_id=$1", tag.ID).Scan(&links); e != nil || links != 1 {
		t.Fatal("tag delete failed to roll back detached links", links, e)
	}
	if _, e := s.Change(ctx, taxonomy.Category, cat.Slug, taxonomy.Delete, taxonomy.Change{}, true, ""); e != nil {
		t.Fatal(e)
	}
	var remaining int
	if e := db.QueryRow("SELECT count(*) FROM taxonomy_candidate.post_category_subscriptions WHERE category_id=$1 AND deleted_at IS NULL", cat.ID).Scan(&remaining); e != nil || remaining != 0 {
		t.Fatal(remaining, e)
	}
	for i := 0; i < 2; i++ {
		if _, _, e := s.Subscription(ctx, taxonomy.Tag, tag.Slug, "unsubscribe"); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.Change(ctx, taxonomy.Tag, tag.Slug, taxonomy.Delete, taxonomy.Change{}, true, ""); e != nil {
		t.Fatal(e)
	}
}
func TestPGTaxonomyConcurrentCreateAndSubscribe(t *testing.T) {
	db := database(t)
	a := taxonomyAuthority(0xb7000000)
	s := taxonomyService(t, db, a)
	ctx := context.Background()
	var wg sync.WaitGroup
	var wins atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := s.Create(ctx, taxonomy.Category, taxonomy.Change{Slug: taxonomyPtr(" B7-Unique ")}, true, "")
			if e == nil {
				wins.Add(1)
			} else if taxonomyCode(e) != "CATEGORY_SLUG_EXISTS" {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal(wins.Load())
	}
	wins.Store(0)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, created, e := s.Subscription(ctx, taxonomy.Category, "b7-unique", "subscribe")
			if e != nil {
				t.Error(e)
			}
			if created {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal(wins.Load())
	}
	list, total, e := s.Subscriptions(ctx, 0, 20)
	if e != nil || total != 1 || len(list) != 1 {
		t.Fatal(list, total, e)
	}
}
func TestPGTaxonomyPolicyErrorsAndReadOnly(t *testing.T) {
	db := database(t)
	a := taxonomyAuthority(0xb8000000)
	s := taxonomyService(t, db, a)
	tag := taxonomyCreate(t, s, taxonomy.Tag, "b8-readonly", nil)
	defaultRepo := taxonomypg.New(db)
	defaultService, _ := taxonomy.NewSyntheticService(defaultRepo, a, nil)
	for _, h := range []http.Handler{taxonomy.NewHandler(s), taxonomy.NewSyntheticHandler(taxonomy.NewService(taxonomypg.NewSyntheticRepository(db, 42))), taxonomy.NewSyntheticHandler(defaultService)} {
		if w := taxonomyHTTP(h, "GET", "/api/posts/tags", ""); w.Code != 503 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	noGrant := a
	noGrant.Grants = nil
	h := taxonomy.NewSyntheticHandler(taxonomyService(t, db, noGrant))
	if w := taxonomyHTTP(h, "DELETE", "/api/admin/tags/"+tag.Slug, ""); w.Code != 403 {
		t.Fatal(w.Code)
	}
	readOnly := database(t)
	readOnly.SetMaxOpenConns(1)
	readOnly.SetMaxIdleConns(1)
	query(t, readOnly, "SET default_transaction_read_only=on")
	ro := taxonomyService(t, readOnly, a)
	if _, e := ro.Get(context.Background(), taxonomy.Tag, tag.Slug, false); e != nil {
		t.Fatal(e)
	}
	if _, e := ro.List(context.Background(), taxonomy.Query{Kind: taxonomy.Tag, Search: "b8-", Take: 20}); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := s.Get(ctx, taxonomy.Tag, tag.Slug, false); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	closed := database(t)
	closed.Close()
	w := taxonomyHTTP(taxonomy.NewSyntheticHandler(taxonomyService(t, closed, a)), "GET", "/api/posts/tags", "")
	if w.Code != 503 || strings.TrimSpace(w.Body.String()) != `{"error":"taxonomy_unavailable"}` {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestPGTaxonomyRestartPersistence(t *testing.T) {
	db := database(t)
	a := taxonomyAuthority(0xb9000000)
	s := taxonomyService(t, db, a)
	ctx := context.Background()
	slug := "b9-restart"
	switch os.Getenv("PG_TEST_PHASE") {
	case "before":
		taxonomyPublisher(t, db, a.PublisherID)
		taxonomyCreate(t, s, taxonomy.Tag, slug, &a.PublisherID)
		if _, e := s.Change(ctx, taxonomy.Tag, slug, taxonomy.Protect, taxonomy.Change{IsProtected: true}, false, ""); e != nil {
			t.Fatal(e)
		}
		if _, _, e := s.Subscription(ctx, taxonomy.Tag, slug, "subscribe"); e != nil {
			t.Fatal(e)
		}
	case "after":
	default:
		t.Fatal("missing restart phase")
	}
	r, e := s.Get(ctx, taxonomy.Tag, slug, false)
	if e != nil || !r.IsProtected || r.OwnerPublisherID == nil || *r.OwnerPublisherID != a.PublisherID {
		t.Fatal(r, e)
	}
	q, e := s.Quota(ctx, "")
	if e != nil || q.Used != 1 || len(q.Records) != 1 {
		t.Fatal(q, e)
	}
	sub, _, e := s.Subscription(ctx, taxonomy.Tag, slug, "subscription")
	if e != nil || sub.TagID == nil || *sub.TagID != r.ID {
		t.Fatal(sub, e)
	}
	w := taxonomyHTTP(taxonomy.NewSyntheticHandler(s), "GET", fmt.Sprintf("/api/posts/tags/%s", slug), "")
	if w.Code != 200 || !strings.Contains(w.Header().Get("X-Solsynth-Compatibility"), "unverified") {
		t.Fatal(w.Code, w.Body.String())
	}
}
