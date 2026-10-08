// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/autocompletepg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere/autocomplete"
	"solsynth.local/go-migration/ports/sphere/stickers"
	"solsynth.local/go-migration/ports/sphere/timeline"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func autocompleteRepository(t *testing.T, f *realmPublishingFixture) *autocompletepg.Repository {
	t.Helper()
	r, e := autocompletepg.New(f.db, f.guard, autocompletepg.Config{Stickers: f.stickers})
	if e != nil {
		t.Fatal(e)
	}
	if e = r.Ready(f.ctx); e != nil {
		t.Fatal(e)
	}
	return r
}
func autocompleteCount(t *testing.T, r *autocompletepg.Repository, ctx context.Context, actor, content string, n int) []autocomplete.Suggestion {
	t.Helper()
	items, e := r.Suggest(ctx, actor, "", content)
	if e != nil || len(items) != n {
		t.Fatal(content, len(items), n, e)
	}
	return items
}
func TestPGAutocompleteCanonicalVisibilityAndPreferences(t *testing.T) {
	f := newRealmPublishingFixture(t)
	r := autocompleteRepository(t, f)
	other, e := f.accounts.LookupAccount(f.ctx, f.other)
	if e != nil {
		t.Fatal(e)
	}
	items := autocompleteCount(t, r, f.ctx, f.actor, "@u/"+other.Name, 1)
	if items[0].Type != "user" || items[0].Keyword != "@u/"+other.Name || items[0].Data.(accounts.Account).ID != f.other {
		t.Fatal(items)
	}
	if _, e = f.timeline.Suppress(f.ctx, f.actor, timeline.PreferenceRequest{Kind: "account", ReferenceID: f.other}); e != nil {
		t.Fatal(e)
	}
	autocompleteCount(t, r, f.ctx, f.actor, "@"+other.Name, 0)
	if e = f.timeline.Unsuppress(f.ctx, f.actor, "account", f.other); e != nil {
		t.Fatal(e)
	}
	autocompleteCount(t, r, f.ctx, f.actor, "@"+other.Name, 1)
	autocompleteCount(t, r, f.outsideCtx, f.outside, "@p/"+f.privatePub.Name, 0)
	autocompleteCount(t, r, f.otherCtx, f.other, "@p/"+f.privatePub.Name, 1)
	autocompleteCount(t, r, f.outsideCtx, f.outside, "@r/"+f.private.Slug, 0)
	autocompleteCount(t, r, f.otherCtx, f.other, "@r/"+f.private.Slug, 1)
	autocompleteCount(t, r, f.outsideCtx, f.outside, "@p/"+f.publicPub.Name, 1)
	autocompleteCount(t, r, f.outsideCtx, f.outside, "@r/"+f.public.Slug, 1)
	if _, e = f.social.ChangeMember(f.otherCtx, f.other, f.private.ID, social.MemberChange{Action: social.Leave}); e != nil {
		t.Fatal(e)
	}
	autocompleteCount(t, r, f.otherCtx, f.other, "@p/"+f.privatePub.Name, 0)
	autocompleteCount(t, r, f.otherCtx, f.other, "@r/"+f.private.Slug, 0)
	if _, e = f.social.ChangeRelationship(f.ctx, f.actor, f.other, social.RelationshipChange{Action: social.Block}); e != nil {
		t.Fatal(e)
	}
	autocompleteCount(t, r, f.ctx, f.actor, "@"+other.Name, 0)
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor)
	autocompleteCount(t, r, f.outsideCtx, f.outside, "@p/"+f.publicPub.Name, 0)
	autocompleteCount(t, r, f.outsideCtx, f.outside, "@r/"+f.public.Slug, 0)
}
func TestPGAutocompleteStickerVisibilityAndMediaChanges(t *testing.T) {
	f := newRealmPublishingFixture(t)
	r := autocompleteRepository(t, f)
	body := "synthetic-image-bytes"
	file, _, e := f.repo.Upload(f.ctx, f.actor, media.Upload{Name: "synthetic.png", MIMEType: "image/png", Size: int64(len(body)), SHA256: mediaDigest(body), IdempotencyKey: "autocomplete-image", Public: true}, strings.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	prefix := "auto" + strings.ReplaceAll(f.actor, "-", "")
	pack, e := f.stickers.CreatePack(f.ctx, f.actor, f.individual.ID, stickers.PackPatch{Name: postString("Synthetic autocomplete pack"), Prefix: &prefix})
	if e != nil {
		t.Fatal(e)
	}
	s, e := f.stickers.CreateSticker(f.ctx, f.actor, pack.ID, stickers.StickerPatch{Slug: postString("sample"), Name: postString("Synthetic sticker"), ImageID: &file.ID})
	if e != nil {
		t.Fatal(e)
	}
	items := autocompleteCount(t, r, f.outsideCtx, f.outside, ":"+prefix, 1)
	if items[0].Type != "sticker" || items[0].Keyword != ":"+prefix+"+sample:" || items[0].Data.(stickers.Sticker).ID != s.ID {
		t.Fatal(items)
	}
	privatePack, privateSticker := authorityUUID(t), authorityUUID(t)
	privatePrefix := "private" + strings.ReplaceAll(f.actor, "-", "")
	query(t, f.db, `INSERT INTO owned_sticker_packs(id,publisher_id,name,description,prefix,created_at,updated_at)VALUES($1,$2,'Synthetic imported organization pack','',$3,$4,$4)`, privatePack, f.privatePub.ID, privatePrefix, f.config.Now())
	query(t, f.db, `INSERT INTO owned_stickers(id,pack_id,slug,name,image_id,size,mode,ordering,created_at,updated_at)VALUES($1,$2,'sample','Synthetic private sticker',$3,0,0,0,$4,$4)`, privateSticker, privatePack, file.ID, f.config.Now())
	autocompleteCount(t, r, f.outsideCtx, f.outside, ":"+privatePrefix, 0)
	autocompleteCount(t, r, f.otherCtx, f.other, ":"+privatePrefix, 1)
	if _, e = f.timeline.Suppress(f.outsideCtx, f.outside, timeline.PreferenceRequest{Kind: "publisher", ReferenceID: f.individual.ID}); e != nil {
		t.Fatal(e)
	}
	autocompleteCount(t, r, f.outsideCtx, f.outside, ":"+prefix, 0)
	if _, e = f.repo.Delete(f.ctx, f.actor, file.ID); e != nil {
		t.Fatal(e)
	}
	autocompleteCount(t, r, f.otherCtx, f.other, ":"+prefix, 0)
	autocompleteCount(t, r, f.otherCtx, f.other, ":"+privatePrefix, 0)
}
func TestPGAutocompleteFreshProofAndBoundedQueries(t *testing.T) {
	f := newRealmPublishingFixture(t)
	r := autocompleteRepository(t, f)
	prefix := "synthetic_autocomplete_bound_" + strings.ReplaceAll(f.actor, "-", "")
	for i := 0; i < 12; i++ {
		id := authorityUUID(t)
		name := prefix + string(rune('a'+i))
		if e := authoritypg.SeedSynthetic(f.ctx, f.db, id, name, f.config.Now()); e != nil {
			t.Fatal(e)
		}
	}
	items := autocompleteCount(t, r, f.ctx, f.actor, "@"+prefix, 10)
	for i, item := range items {
		if item.Keyword != "@"+prefix+string(rune('a'+i)) {
			t.Fatal("unstable prefix ordering", items)
		}
	}
	autocompleteCount(t, r, f.ctx, f.actor, "#tag", 0)
	autocompleteCount(t, r, f.ctx, f.actor, "@u/%", 0)
	if _, e := r.Suggest(context.Background(), f.actor, "", "@"+prefix); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("unverified actor authorized autocomplete", e)
	}
	if _, e := r.Suggest(f.ctx, f.actor, authorityUUID(t), "@"+prefix); !errors.Is(e, autocomplete.ErrUnavailable) {
		t.Fatal("missing room policy failed open", e)
	}
	ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
	defer cancel()
	af := atomicAuthorityFixture{db: f.db, ctx: ctx, actor: f.actor}
	block, pid := atomicBlocker(t, af, "owned_authority_accounts", f.actor)
	if _, e := block.ExecContext(ctx, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := r.Suggest(ctx, f.actor, "", "@"+prefix); done <- e }()
	waitAtomicBlocked(t, af, pid, "owned_authority_accounts")
	if e := block.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; !errors.Is(e, authority.ErrDisabled) {
		t.Fatal("disabled waiter received projections", e)
	}
}

func TestPGAutocompleteStickerRealmLockOrderingAndRecheck(t *testing.T) {
	f := newRealmPublishingFixture(t)
	r := autocompleteRepository(t, f)
	body := "synthetic wait image"
	file, _, e := f.repo.Upload(f.ctx, f.actor, media.Upload{Name: "synthetic-wait.png", MIMEType: "image/png", Size: int64(len(body)), SHA256: mediaDigest(body), IdempotencyKey: "autocomplete-wait", Public: true}, strings.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	prefix := "autowait" + strings.ReplaceAll(f.actor, "-", "")
	for i, pub := range []accounts.Publisher{f.privatePub, f.publicPub} {
		pack, id := authorityUUID(t), authorityUUID(t)
		packPrefix := prefix + string(rune('a'+i))
		query(t, f.db, `INSERT INTO owned_sticker_packs(id,publisher_id,name,description,prefix,created_at,updated_at)VALUES($1,$2,'Synthetic ordered pack','',$3,$4,$4)`, pack, pub.ID, packPrefix, f.config.Now())
		query(t, f.db, `INSERT INTO owned_stickers(id,pack_id,slug,name,image_id,size,mode,ordering,created_at,updated_at)VALUES($1,$2,'sample','Synthetic ordered sticker',$3,0,0,0,$4,$4)`, id, pack, file.ID, f.config.Now())
	}
	ctx, cancel := context.WithTimeout(f.otherCtx, 20*time.Second)
	defer cancel()
	af := atomicAuthorityFixture{db: f.db, ctx: ctx, actor: f.other}
	low, high := f.private.ID, f.public.ID
	if low > high {
		low, high = high, low
	}
	type result struct {
		items []autocomplete.Suggestion
		e     error
	}
	done := make(chan result, 1)
	run := func(realm, content string) *sql.Tx {
		t.Helper()
		block := realmPublishingBlock(t, ctx, f.db, realm)
		var pid int
		if e := block.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
			t.Fatal(e)
		}
		go func() { items, e := r.Suggest(ctx, f.other, "", content); done <- result{items, e} }()
		waitAtomicBlocked(t, af, pid, "owned_realms")
		return block
	}
	block := run(low, ":"+prefix)
	check, e := f.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	_, e = check.ExecContext(ctx, `SELECT id FROM owned_realms WHERE id=$1 FOR UPDATE NOWAIT`, high)
	if e == nil {
		_, e = check.ExecContext(ctx, `SELECT id FROM owned_publishers WHERE id IN($1::uuid,$2::uuid) FOR UPDATE NOWAIT`, f.privatePub.ID, f.publicPub.ID)
	}
	_ = check.Rollback()
	if e != nil {
		t.Fatal("suggestions locked later realm/publisher before smallest realm", e)
	}
	if e = block.Commit(); e != nil {
		t.Fatal(e)
	}
	got := <-done
	if got.e != nil || len(got.items) != 2 {
		t.Fatal("ordered batch", got)
	}
	block = run(f.private.ID, ":"+prefix+"a")
	if _, e = block.ExecContext(ctx, `UPDATE owned_realm_members SET leave_at=$3 WHERE realm_id=$1 AND account_id=$2`, f.private.ID, f.other, f.config.Now()); e != nil {
		t.Fatal(e)
	}
	if e = block.Commit(); e != nil {
		t.Fatal(e)
	}
	got = <-done
	if got.e != nil || len(got.items) != 0 {
		t.Fatal("membership changed during wait but stale sticker metadata returned", got)
	}
}

func TestPGAutocompleteChatRelationAndRealmPrivacy(t *testing.T) {
	f := newChatRelationsFixture(t)
	r, e := autocompletepg.New(f.db, f.guard, autocompletepg.Config{Rooms: f.chats})
	if e != nil {
		t.Fatal(e)
	}
	direct := relationDirect(t, f)
	accountsRepo := accountpg.New(f.db)
	other, e := accountsRepo.LookupAccount(f.base, f.other)
	if e != nil {
		t.Fatal(e)
	}
	third, e := accountsRepo.LookupAccount(f.base, f.third)
	if e != nil {
		t.Fatal(e)
	}
	items, e := r.Suggest(f.contexts[f.owner], f.owner, direct.ID, "@"+other.Name)
	if e != nil || len(items) != 1 || items[0].Data.(accounts.Account).ID != f.other {
		t.Fatal(items, e)
	}
	if items, e = r.Suggest(f.contexts[f.owner], f.owner, direct.ID, "@"+third.Name); e != nil || len(items) != 0 {
		t.Fatal("nonmember chat mention", items, e)
	}
	if _, e = r.Suggest(f.contexts[f.outsider], f.outsider, direct.ID, "@"+other.Name); !errors.Is(e, autocomplete.ErrForbidden) {
		t.Fatal("outsider chat suggestions", e)
	}
	if _, e = f.social.ChangeRelationship(f.contexts[f.other], f.other, f.owner, social.RelationshipChange{Action: social.Block}); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Suggest(f.contexts[f.owner], f.owner, direct.ID, "@"+other.Name); !errors.Is(e, autocomplete.ErrForbidden) {
		t.Fatal("blocked DM suggestions", e)
	}
	if _, e = f.social.ChangeRelationship(f.contexts[f.other], f.other, f.owner, social.RelationshipChange{Action: social.Forget}); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.other)
	if _, e = r.Suggest(f.contexts[f.owner], f.owner, direct.ID, "@"+other.Name); !errors.Is(e, autocomplete.ErrForbidden) {
		t.Fatal("disabled-peer DM suggestions", e)
	}
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=false WHERE id=$1`, f.other)
	realm := relationRealm(t, f, false)
	room := relationRoom(t, f, realm)
	items, e = r.Suggest(f.contexts[f.third], f.third, room.ID, "@"+other.Name)
	if e != nil || len(items) != 1 {
		t.Fatal("joined realm member suggestions", items, e)
	}
	if _, e = f.social.ChangeMember(f.contexts[f.third], f.third, realm.ID, social.MemberChange{Action: social.Leave}); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Suggest(f.contexts[f.third], f.third, room.ID, "@"+other.Name); !errors.Is(e, autocomplete.ErrForbidden) {
		t.Fatal("former realm member kept room suggestions", e)
	}
	if _, e = f.chats.ChangeMember(f.contexts[f.owner], f.owner, room.ID, rooms.Leave, ""); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Suggest(f.contexts[f.owner], f.owner, room.ID, "@"+other.Name); !errors.Is(e, autocomplete.ErrForbidden) {
		t.Fatal("former room member kept suggestions", e)
	}
	if e = f.social.DeleteRealm(f.contexts[f.owner], f.owner, realm.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Suggest(f.contexts[f.other], f.other, room.ID, "@"+other.Name); !errors.Is(e, autocomplete.ErrForbidden) {
		t.Fatal("deleted realm suggestions", e)
	}
}
func TestPGAutocompleteRestartPersistence(t *testing.T) {
	const name = "synthetic_autocomplete_restart"
	if os.Getenv("PG_TEST_PHASE") == "after" {
		db := database(t)
		var actor string
		var created time.Time
		if e := db.QueryRow(`SELECT id::text,created_at FROM owned_authority_accounts WHERE name=$1`, name).Scan(&actor, &created); e != nil {
			t.Fatal(e)
		}
		clock := &atomic.Int64{}
		clock.Store(created.Unix())
		cfg := authorityConfig(t, authorityKeys(t), clock)
		store := authoritypg.New(db)
		issuer, e := authority.New(store, cfg)
		if e != nil {
			t.Fatal(e)
		}
		token := pgIssue(t, issuer, actor).Token
		verifier, e := authority.NewVerifier(store, cfg)
		if e != nil {
			t.Fatal(e)
		}
		proof, e := verifier.Authenticate(context.Background(), token)
		if e != nil {
			t.Fatal(e)
		}
		guard := authoritypg.NewTransactionGuard(store, verifier)
		r, e := autocompletepg.New(db, guard, autocompletepg.Config{})
		if e != nil {
			t.Fatal(e)
		}
		items := autocompleteCount(t, r, guard.Bind(context.Background(), proof), actor, "@"+name, 1)
		if items[0].Data.(accounts.Account).ID != actor {
			t.Fatal(items)
		}
		return
	}
	f := newRealmPublishingFixture(t)
	query(t, f.db, `UPDATE owned_authority_accounts SET name=$2 WHERE id=$1`, f.actor, name)
	autocompleteCount(t, autocompleteRepository(t, f), f.ctx, f.actor, "@"+name, 1)
}
