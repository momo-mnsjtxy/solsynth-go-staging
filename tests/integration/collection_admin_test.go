// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/internal/collectionadminpg"
	c "solsynth.local/go-migration/ports/admin/collections"
	admin "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/sphere/posts"
	"strings"
	"testing"
	"time"
)

func collectionAdminFixture(t *testing.T) (*moderationFixture, *collectionadminpg.Repository, string) {
	t.Helper()
	f := newModerationFixture(t)
	r, e := collectionadminpg.New(f.db, f.guard, f.config.Now)
	if e != nil {
		t.Fatal(e)
	}
	if e = r.Ready(f.ctx); e != nil {
		t.Fatal(e)
	}
	id := authorityUUID(t)
	query(t, f.db, `INSERT INTO owned_post_collections(id,publisher_id,slug,name,description,created_at,updated_at)VALUES($1::uuid,$2::uuid,$3::text,'Synthetic collection','Synthetic description',$4::timestamptz,$4::timestamptz)`, id, f.individual.ID, "synthetic-admin-"+id, f.config.Now())
	return f, r, id
}
func TestPGCollectionAdminMetadataAndNestedPrivacy(t *testing.T) {
	f, r, id := collectionAdminFixture(t)
	private := f.post(t, f.individual, "SYNTHETIC_PRIVATE_COLLECTION_BODY", posts.Private)
	public := f.post(t, f.individual, "Synthetic public collection post", posts.Public)
	for _, post := range []posts.Post{private, public} {
		query(t, f.db, `INSERT INTO owned_post_collection_items(collection_id,post_id,created_at)VALUES($1::uuid,$2::uuid,$3::timestamptz)`, id, post.ID, f.config.Now())
	}
	if _, e := r.Get(f.ctx, f.actor, id); !errors.Is(e, admin.ErrForbidden) {
		t.Fatal("publisher owner bypassed global gate", e)
	}
	v, e := r.Get(f.outsideCtx, f.outside, id)
	if e != nil || v.ItemCount != 2 || v.PublisherID != f.individual.ID {
		t.Fatal(v, e)
	}
	b, _ := json.Marshal(v)
	for _, secret := range []string{private.ID, public.ID, private.Content, "attachments", "storage_key"} {
		if strings.Contains(string(b), secret) {
			t.Fatal("nested content leaked", string(b))
		}
	}
	name := "Updated synthetic collection"
	v, e = r.Update(f.outsideCtx, f.outside, id, c.Patch{Name: &name})
	if e != nil || v.Name == nil || *v.Name != name || v.PublisherID != f.individual.ID || v.Description == nil || *v.Description != "Synthetic description" {
		t.Fatal(v, e)
	}
	rows, total, e := r.List(f.outsideCtx, f.outside, c.Page{PublisherID: f.individual.ID, Query: "Updated synthetic", Take: 200})
	if e != nil || total != 1 || len(rows) != 1 || rows[0].ID != id {
		t.Fatal(rows, total, e)
	}
	rows, total, e = r.List(f.outsideCtx, f.outside, c.Page{PublisherID: f.individual.ID, Query: "%"})
	if e != nil || total != 0 || len(rows) != 0 {
		t.Fatal("wildcard query interpreted", rows, total, e)
	}
	if e = r.Delete(f.outsideCtx, f.outside, id); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Get(f.outsideCtx, f.outside, id); !errors.Is(e, c.ErrNotFound) {
		t.Fatal(e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_post_collection_items WHERE collection_id=$1::uuid`, id); n != 0 {
		t.Fatal(n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_text_posts WHERE id IN($1::uuid,$2::uuid) AND deleted_at IS NULL`, private.ID, public.ID); n != 2 {
		t.Fatal("collection deletion deleted posts", n)
	}
}
func TestPGCollectionAdminAuditRollbackAndDeleteRace(t *testing.T) {
	f, r, id := collectionAdminFixture(t)
	name := "synthetic_collection_audit_" + strings.ReplaceAll(f.actor, "-", "")
	query(t, f.db, `ALTER TABLE owned_post_interaction_audit ADD CONSTRAINT `+name+` CHECK(collection_id IS DISTINCT FROM '`+id+`'::uuid) NOT VALID`)
	t.Cleanup(func() { _, _ = f.db.Exec(`ALTER TABLE owned_post_interaction_audit DROP CONSTRAINT IF EXISTS ` + name) })
	changed := "Should roll back"
	_, e := r.Update(f.outsideCtx, f.outside, id, c.Patch{Name: &changed})
	domainConstraint(t, e, name)
	v, e := r.Get(f.outsideCtx, f.outside, id)
	if e != nil || v.Name == nil || *v.Name != "Synthetic collection" {
		t.Fatal(v, e)
	}
	query(t, f.db, `ALTER TABLE owned_post_interaction_audit DROP CONSTRAINT `+name)
	tx, e := f.db.BeginTx(f.ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	var pid int
	if e = tx.QueryRow(`SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(`UPDATE owned_post_collections SET deleted_at=$2::timestamptz WHERE id=$1::uuid`, id, f.config.Now()); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(f.outsideCtx, 10*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, e := r.Update(ctx, f.outside, id, c.Patch{Name: &changed}); result <- e }()
	domainWait(t, f.db, pid)
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-result; !errors.Is(e, c.ErrNotFound) {
		t.Fatal("deleted collection resurrected", e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_post_interaction_audit WHERE collection_id=$1::uuid`, id); n != 0 {
		t.Fatal("failed mutation audit committed", n)
	}
}
