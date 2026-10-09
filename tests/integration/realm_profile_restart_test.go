// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/commercepg"
	"solsynth.local/go-migration/internal/moderationpg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/internal/realmboostpg"
	"solsynth.local/go-migration/internal/realmmemberpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	m "solsynth.local/go-migration/ports/passport/realmmembers"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere/posts"
	"testing"
	"time"
)

func TestPGRealmProfileAndLogRestartPersistence(t *testing.T) {
	db := database(t)
	phase := fixtureRestartPhase(t)
	const marker = "realm-profile-log-record.json"
	type saved struct {
		Actor, Realm, Wallet, Token string
		Authority                   restartAuthoritySnapshot
		Policy                      realmboostpg.OwnedPolicy
		Member                      social.Member
		Log                         m.ModerationLog
		Audits                      int
	}
	if phase == "before" {
		f := newRealmMemberFixture(t)
		// Issue the original bounded proof before any profile or removal target write.
		at, wall := f.config.Now().UTC(), time.Now()
		f.config.AccessTTL = time.Hour
		f.config.SessionTTL = time.Hour
		f.config.Now = func() time.Time { return at.Add(time.Since(wall)) }
		var e error
		f.issuer, e = authority.New(authoritypg.New(f.db), f.config)
		if e != nil {
			t.Fatal(e)
		}
		f.token = pgIssue(t, f.issuer, f.actor).Token
		f.verifier, e = authority.NewVerifier(authoritypg.New(f.db), f.config)
		if e != nil {
			t.Fatal(e)
		}
		f.principal, e = f.verifier.Authenticate(f.ctx, f.token)
		if e != nil {
			t.Fatal(e)
		}
		f.guard = authoritypg.NewTransactionGuard(authoritypg.New(f.db), f.verifier)
		f.ctx = f.guard.Bind(f.ctx, f.principal)
		f.cfg.Now = f.config.Now
		f.memberConfig.Now = f.config.Now
		f.r = commercepg.New(f.db, f.guard, commercepg.Adapters{}).WithClock(f.config.Now)
		f, _ = realmProfilePerk(t, f)
		if e = authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
			t.Fatal(e)
		}
		member, e := f.members.UpdateProfile(f.ctx, f.actor, f.realm.ID, m.ProfileRequest{Nick: postString("Persisted realm identity"), Bio: postString("Original realm bio")})
		if e != nil {
			t.Fatal(e)
		}
		postsRepo := postpg.NewWithAuthority(f.db, f.guard).WithVisibility(f.social)
		p, e := postsRepo.Create(f.ctx, f.actor, f.publisher, posts.Patch{Content: postString("Restart private body"), RealmID: &f.realm.ID})
		if e != nil {
			t.Fatal(e)
		}
		admin, e := moderationpg.New(f.db, f.guard, moderationpg.Config{Now: f.cfg.Now})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = admin.ModerateRealmPost(f.ctx, f.actor, p.ID, postString("Persisted reason")); e != nil {
			t.Fatal(e)
		}
		logs, _, e := f.members.ModerationLogs(f.ctx, f.actor, f.realm.ID, 0, 20)
		if e != nil || len(logs) != 1 {
			t.Fatal(logs, e)
		}
		saveRestartFixture(t, marker, saved{Actor: f.actor, Realm: f.realm.ID, Wallet: f.payee.ID, Token: f.token, Authority: saveAuthoritySnapshot(t, f.config), Policy: *f.cfg.Policy, Member: member, Log: logs[0], Audits: commerceCount(t, f.db, `SELECT count(*) FROM owned_realm_profile_audit WHERE realm_id=$1`, f.realm.ID)})
		return
	}
	var v saved
	loadRestartFixture(t, marker, &v)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	config := restoreAuthoritySnapshot(t, v.Authority) // original public keys, real elapsed clock
	verifier, e := authority.NewVerifier(authoritypg.New(db), config)
	if e != nil {
		t.Fatal(e)
	}
	proof, e := verifier.Authenticate(ctx, v.Token)
	if e != nil {
		t.Fatal(e)
	}
	guard := authoritypg.NewTransactionGuard(authoritypg.New(db), verifier)
	ctx = guard.Bind(ctx, proof)
	commerce := commercepg.New(db, guard, commercepg.Adapters{}).WithClock(config.Now)
	boosts, e := realmboostpg.New(db, guard, commerce, realmboostpg.Config{TreasuryAccountID: v.Actor, TreasuryWalletID: v.Wallet, Policy: &v.Policy, Now: config.Now})
	if e != nil {
		t.Fatal(e)
	}
	socialRepo := socialpg.NewWithAuthority(db, guard).WithBoosts(boosts)
	members, e := realmmemberpg.New(db, guard, boosts, socialRepo, realmmemberpg.Config{LevelThresholds: []int64{}, Now: config.Now})
	if e != nil {
		t.Fatal(e)
	}
	socialRepo.WithMemberDetails(members)
	// After restart reconstruct only adapters and consume the original proof. No
	// seed, credentials, replacement grant, fixture or policy writes are allowed.
	member, e := socialRepo.Me(ctx, v.Actor, v.Realm)
	if e != nil || member.ID != v.Member.ID || member.Nick == nil || *member.Nick != *v.Member.Nick || member.Bio == nil || *member.Bio != *v.Member.Bio || !member.UpdatedAt.Equal(v.Member.UpdatedAt) {
		t.Fatal(member, e)
	}
	logs, total, e := members.ModerationLogs(ctx, v.Actor, v.Realm, 0, 20)
	if e != nil || total != 1 || len(logs) != 1 || logs[0].ID != v.Log.ID || logs[0].PostID != v.Log.PostID || !logs[0].ModeratedAt.Equal(v.Log.ModeratedAt) {
		t.Fatal(logs, total, e)
	}
	if n := commerceCount(t, db, `SELECT count(*) FROM owned_realm_profile_audit WHERE realm_id=$1`, v.Realm); n != v.Audits {
		t.Fatal("read changed audit", n)
	}
}
