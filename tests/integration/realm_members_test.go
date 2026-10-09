// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/commercepg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/realmboostpg"
	"solsynth.local/go-migration/internal/realmmemberpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	rb "solsynth.local/go-migration/ports/passport/realmboosts"
	m "solsynth.local/go-migration/ports/passport/realmmembers"
	"solsynth.local/go-migration/ports/passport/social"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"sync"
	"testing"
	"time"
)

type realmMemberFixture struct {
	realmBoostFixture
	members      *realmmemberpg.Repository
	memberConfig realmmemberpg.Config
}

func newRealmMemberFixture(t *testing.T) realmMemberFixture {
	t.Helper()
	f := realmMemberFixture{realmBoostFixture: newRealmBoostFixture(t)}
	query(t, f.db, realmmemberpg.Schema)
	f.memberConfig = realmmemberpg.Config{LevelThresholds: []int64{0, 1000, 2000}, EnableDailyTenure: true, Now: f.cfg.Now}
	return configureRealmMember(t, f)
}
func configureRealmMember(t *testing.T, f realmMemberFixture) realmMemberFixture {
	t.Helper()
	var e error
	f.members, e = realmmemberpg.New(f.db, f.guard, f.boosts, f.social, f.memberConfig)
	if e != nil {
		t.Fatal(e)
	}
	f.social.WithMemberDetails(f.members)
	if e = f.members.Ready(f.ctx); e != nil {
		t.Fatal(e)
	}
	return f
}
func unlockRealmLabels(t *testing.T, f realmMemberFixture) {
	t.Helper()
	boostPay(t, f.realmBoostFixture, boostOrder(t, f.realmBoostFixture, 10, "golds", "labels"))
}
func createRealmLabel(t *testing.T, f realmMemberFixture, key, name string) social.RealmLabel {
	t.Helper()
	v, e := f.members.CreateLabel(f.ctx, f.actor, f.realm.ID, key, m.LabelRequest{Name: name})
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func addRealmMember(t *testing.T, f realmMemberFixture) (string, context.Context) {
	t.Helper()
	other := newAtomicAuthorityFixture(t)
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, other.actor, []authority.Rule{{Pattern: "realms.*"}}, nil); e != nil {
		t.Fatal(e)
	}
	p, e := f.verifier.Authenticate(f.ctx, pgIssue(t, f.issuer, other.actor).Token)
	if e != nil {
		t.Fatal(e)
	}
	ctx := f.guard.Bind(f.ctx, p)
	if _, e = f.social.ChangeMember(ctx, other.actor, f.realm.ID, social.MemberChange{Action: social.Join}); e != nil {
		t.Fatal(e)
	}
	return other.actor, ctx
}
func TestPGRealmLabelsCRUDVisibilityAndCanonicalProjection(t *testing.T) {
	f := newRealmMemberFixture(t)
	unlockRealmLabels(t, f)
	v := createRealmLabel(t, f, "create", "Makers")
	color, icon := "#ffcc00", "✨"
	updated, e := f.members.UpdateLabel(f.ctx, f.actor, f.realm.Slug, v.ID, m.LabelRequest{Name: "Builders", Color: &color, Icon: &icon})
	if e != nil || updated.Name != "Builders" || updated.Icon == nil || *updated.Icon != icon {
		t.Fatal(updated, e)
	}
	assigned, e := f.members.AssignLabel(f.ctx, f.actor, f.realm.ID, f.actor, &v.ID)
	if e != nil || assigned.LabelID == nil || *assigned.LabelID != v.ID || assigned.Label == nil || assigned.Label.Name != "Builders" {
		t.Fatal(assigned, e)
	}
	me, e := f.social.Me(f.ctx, f.actor, f.realm.ID)
	if e != nil || me.LabelID == nil || me.Progression == nil || me.Progression.RecordedExperience != 1000 || me.Progression.ExperienceComplete {
		t.Fatal(me, e)
	}
	all, total, e := f.social.Members(f.ctx, "", f.realm.ID, 0, 20)
	if e != nil || total != 1 || len(all) != 1 || all[0].LabelID == nil {
		t.Fatal(all, total, e)
	}
	filtered, total, e := f.members.MembersWithLabel(f.ctx, "", f.realm.ID, v.ID, 0, 20)
	if e != nil || total != 1 || len(filtered) != 1 {
		t.Fatal(filtered, total, e)
	}
	no := false
	if _, e = f.social.UpdateRealm(f.ctx, f.actor, f.realm.ID, social.RealmPatch{IsPublic: &no}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.members.Labels(f.ctx, "", f.realm.ID); !errors.Is(e, m.ErrNotFound) {
		t.Fatal("private labels leaked", e)
	}
	if _, e = f.members.Progression(f.ctx, "", f.realm.ID, f.actor); !errors.Is(e, m.ErrNotFound) {
		t.Fatal("private XP leaked", e)
	}
	if e = f.members.DeleteLabel(f.ctx, f.actor, f.realm.ID, v.ID); e != nil {
		t.Fatal(e)
	}
	me, e = f.social.Me(f.ctx, f.actor, f.realm.ID)
	if e != nil || me.LabelID != nil || me.Label != nil {
		t.Fatal("deleted assignment survived", me, e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_realm_label_assignments WHERE realm_id=$1::uuid`, f.realm.ID); n != 0 {
		t.Fatal(n)
	}
	if _, e = f.members.CreateLabel(f.ctx, f.actor, f.realm.ID, "create", m.LabelRequest{Name: "Makers"}); !errors.Is(e, m.ErrConflict) {
		t.Fatal("deleted replay resurrected label", e)
	}
}
func TestPGRealmLabelsConcurrentQuotaAndReplay(t *testing.T) {
	f := newRealmMemberFixture(t)
	f.cfg.Policy.LabelCaps = [4]int{0, 1, 1, 1}
	f.realmBoostFixture = configureRealmBoost(t, f.realmBoostFixture)
	f = configureRealmMember(t, f)
	unlockRealmLabels(t, f)
	type result struct {
		v social.RealmLabel
		e error
	}
	start := make(chan struct{})
	done := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			v, e := f.members.CreateLabel(f.ctx, f.actor, f.realm.ID, "same", m.LabelRequest{Name: "Only"})
			done <- result{v, e}
		}()
	}
	close(start)
	a, b := <-done, <-done
	if a.e != nil || b.e != nil || a.v.ID != b.v.ID {
		t.Fatal(a, b)
	}
	if _, e := f.members.CreateLabel(f.ctx, f.actor, f.realm.ID, "other", m.LabelRequest{Name: "Too many"}); !errors.Is(e, m.ErrQuota) {
		t.Fatal(e)
	}
	if _, e := f.members.CreateLabel(f.ctx, f.actor, f.realm.ID, "same", m.LabelRequest{Name: "Changed"}); !errors.Is(e, m.ErrConflict) {
		t.Fatal(e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_realm_member_audit WHERE realm_id=$1::uuid AND action='label.created'`, f.realm.ID); n != 1 {
		t.Fatal("duplicate replay audit", n)
	}
	if e := f.members.DeleteLabel(f.ctx, f.actor, f.realm.ID, a.v.ID); e != nil {
		t.Fatal(e)
	}
	start = make(chan struct{})
	for i := 0; i < 2; i++ {
		go func(i int) {
			<-start
			v, e := f.members.CreateLabel(f.ctx, f.actor, f.realm.ID, fmt.Sprintf("cap-%d", i), m.LabelRequest{Name: "One slot"})
			done <- result{v, e}
		}(i)
	}
	close(start)
	a, b = <-done, <-done
	if (a.e == nil) == (b.e == nil) || a.e != nil && !errors.Is(a.e, m.ErrQuota) || b.e != nil && !errors.Is(b.e, m.ErrQuota) {
		t.Fatal("cap race", a, b)
	}
}
func TestPGRealmLabelsMembershipIncarnationAndCurrentRole(t *testing.T) {
	f := newRealmMemberFixture(t)
	unlockRealmLabels(t, f)
	v := createRealmLabel(t, f, "", "Guild")
	other, ctx := addRealmMember(t, f)
	if _, e := f.members.AssignLabel(f.ctx, f.actor, f.realm.ID, other, &v.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.members.CreateLabel(ctx, other, f.realm.ID, "", m.LabelRequest{Name: "Forbidden"}); !errors.Is(e, m.ErrForbidden) {
		t.Fatal("normal member label admin", e)
	}
	if _, e := f.social.ChangeMember(ctx, other, f.realm.ID, social.MemberChange{Action: social.Leave}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.members.AssignLabel(f.ctx, f.actor, f.realm.ID, other, &v.ID); !errors.Is(e, m.ErrNotFound) {
		t.Fatal("assignment to departed member", e)
	}
	if _, e := f.social.ChangeMember(ctx, other, f.realm.ID, social.MemberChange{Action: social.Join}); e != nil {
		t.Fatal(e)
	}
	me, e := f.social.Me(ctx, other, f.realm.ID)
	if e != nil || me.LabelID != nil {
		t.Fatal("rejoin revived label", me, e)
	}
	query(t, f.db, `UPDATE owned_realm_members SET role=50 WHERE realm_id=$1::uuid AND account_id=$2::uuid`, f.realm.ID, other)
	if _, e = f.members.CreateLabel(ctx, other, f.realm.ID, "moderator", m.LabelRequest{Name: "Moderator"}); e != nil {
		t.Fatal("source moderator gate", e)
	}
	query(t, f.db, `UPDATE owned_realm_members SET role=0 WHERE realm_id=$1::uuid AND account_id=$2::uuid`, f.realm.ID, other)
	if _, e = f.members.CreateLabel(ctx, other, f.realm.ID, "moderator", m.LabelRequest{Name: "Moderator"}); !errors.Is(e, m.ErrForbidden) {
		t.Fatal("replay retained removed role", e)
	}
}
func TestPGRealmLabelsExpiryAndAuthorityAfterWait(t *testing.T) {
	for _, mode := range []string{"expiry", "revocation", "role"} {
		t.Run(mode, func(t *testing.T) {
			f := newRealmMemberFixture(t)
			unlockRealmLabels(t, f)
			table, id := "owned_realms", f.realm.ID
			if mode == "revocation" {
				table, id = "owned_authority_accounts", f.actor
			}
			blocker, pid := atomicBlocker(t, f.atomicAuthorityFixture, table, id)
			done := make(chan error, 1)
			go func() {
				_, e := f.members.CreateLabel(f.ctx, f.actor, f.realm.ID, "wait", m.LabelRequest{Name: "Waiting"})
				done <- e
			}()
			waitAtomicBlocked(t, f.atomicAuthorityFixture, pid, table)
			switch mode {
			case "expiry":
				f.boostNow.Add(int64(30*24*time.Hour + time.Microsecond))
				querySQL := `UPDATE owned_realms SET boost_level=3 WHERE id=$1::uuid`
				if _, e := blocker.ExecContext(f.ctx, querySQL, f.realm.ID); e != nil {
					t.Fatal(e)
				}
			case "revocation":
				if _, e := blocker.ExecContext(f.ctx, `UPDATE owned_authority_sessions SET revoked_at=now(),epoch=epoch+1 WHERE id=$1::uuid`, f.principal.Session.ID); e != nil {
					t.Fatal(e)
				}
			case "role":
				if _, e := blocker.ExecContext(f.ctx, `UPDATE owned_realm_members SET role=0 WHERE realm_id=$1::uuid AND account_id=$2::uuid`, f.realm.ID, f.actor); e != nil {
					t.Fatal(e)
				}
			}
			if e := blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			e := <-done
			if mode == "revocation" {
				if !errors.Is(e, authority.ErrRevoked) {
					t.Fatal(e)
				}
			} else if !errors.Is(e, m.ErrForbidden) {
				t.Fatal(e)
			}
			if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_realm_labels WHERE realm_id=$1::uuid`, f.realm.ID); n != 0 {
				t.Fatal("stale write committed", n)
			}
		})
	}
}
func TestPGRealmLabelsAuditRollbackAndCrossRealmIsolation(t *testing.T) {
	f := newRealmMemberFixture(t)
	unlockRealmLabels(t, f)
	query(t, f.db, `CREATE FUNCTION synthetic_member_fail()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.actor_id::text=TG_ARGV[0] THEN RAISE EXCEPTION 'synthetic rollback';END IF;RETURN NEW;END$$`)
	query(t, f.db, fmt.Sprintf(`CREATE TRIGGER synthetic_member_fail BEFORE INSERT ON owned_realm_member_audit FOR EACH ROW EXECUTE FUNCTION synthetic_member_fail('%s')`, f.actor))
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DROP TRIGGER IF EXISTS synthetic_member_fail ON owned_realm_member_audit;DROP FUNCTION IF EXISTS synthetic_member_fail()`)
	})
	if _, e := f.members.CreateLabel(f.ctx, f.actor, f.realm.ID, "rollback", m.LabelRequest{Name: "Rolled back"}); e == nil {
		t.Fatal("audit failure accepted")
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_realm_label_requests WHERE actor_id=$1::uuid`, f.actor); n != 0 {
		t.Fatal("orphan replay key", n)
	}
	query(t, f.db, `DROP TRIGGER synthetic_member_fail ON owned_realm_member_audit`)
	v := createRealmLabel(t, f, "rollback", "Rolled back")
	second := newRealmMemberFixture(t)
	unlockRealmLabels(t, second)
	if _, e := second.members.AssignLabel(second.ctx, second.actor, second.realm.ID, second.actor, &v.ID); !errors.Is(e, m.ErrLabelRealm) {
		t.Fatal("foreign label assignment", e)
	}
	if _, e := second.members.UpdateLabel(second.ctx, second.actor, second.realm.ID, v.ID, m.LabelRequest{Name: "Foreign"}); !errors.Is(e, m.ErrNotFound) {
		t.Fatal("foreign label update", e)
	}
	if _, _, e := second.members.MembersWithLabel(second.ctx, "", second.realm.ID, v.ID, 0, 20); !errors.Is(e, m.ErrLabelRealm) {
		t.Fatal("foreign member filter", e)
	}
}
func TestPGRealmTenureHistoryReplayAndBoundedProgression(t *testing.T) {
	f := newRealmMemberFixture(t)
	unlockRealmLabels(t, f)
	other, ctx := addRealmMember(t, f)
	query(t, f.db, `UPDATE owned_realm_members SET joined_at=$2::timestamptz WHERE realm_id=$1::uuid`, f.realm.ID, f.cfg.Now().Add(-48*time.Hour))
	if _, e := f.members.GrantTenure(ctx, other, f.realm.ID, m.TenureRequest{Take: 100}); !errors.Is(e, m.ErrForbidden) {
		t.Fatal("nonowner awarded tenure", e)
	}
	out, e := f.members.GrantTenure(f.ctx, f.actor, f.realm.ID, m.TenureRequest{Take: 1})
	if e != nil || out.Awarded != 1 || out.Next == nil {
		t.Fatal(out, e)
	}
	next, e := f.members.GrantTenure(f.ctx, f.actor, f.realm.ID, m.TenureRequest{Take: 1, After: *out.Next})
	if e != nil || next.Awarded != 1 || next.Next != nil {
		t.Fatal(next, e)
	}
	replay, e := f.members.GrantTenure(f.ctx, f.actor, f.realm.ID, m.TenureRequest{Take: 100})
	if e != nil || replay.Awarded != 0 {
		t.Fatal("daily replay", replay, e)
	}
	p, e := f.members.Progression(f.ctx, "", f.realm.ID, f.actor)
	if e != nil || p.RecordedExperience != 1005 || p.ExperienceComplete || p.Level == nil || *p.Level != 1 {
		t.Fatal(p, e)
	}
	history, e := f.members.History(f.ctx, "", f.realm.ID, f.actor, 50)
	if e != nil || len(history) != 2 {
		t.Fatal(history, e)
	}
	// A date cast follows the connection's DateStyle; the reason contract must
	// remain the original UTC ISO day on every configured PostgreSQL session.
	// This fixture owns its pool, so one connection pins the temporary setting.
	f.db.SetMaxOpenConns(1)
	var dateStyle string
	if e = f.db.QueryRowContext(f.ctx, `SHOW DateStyle`).Scan(&dateStyle); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `SET DateStyle TO 'SQL, DMY'`)
	localized, e := f.members.History(f.ctx, "", f.realm.ID, f.actor, 50)
	query(t, f.db, `SELECT set_config('DateStyle',$1::text,false)`, dateStyle)
	if e != nil || len(localized) != len(history) {
		t.Fatal("non-ISO session history", localized, e)
	}
	var tenureFound bool
	for i, record := range localized {
		if record.ID != history[i].ID || record.Reason != history[i].Reason || !record.CreatedAt.Equal(history[i].CreatedAt) {
			t.Fatal("session formatting changed history", record, history[i])
		}
		if record.ReasonType == "realm.tenure.daily" {
			tenureFound = true
			if record.Reason != out.Day {
				t.Fatal("tenure reason is not the UTC ISO day", record.Reason, out.Day)
			}
		}
	}
	if !tenureFound {
		t.Fatal("tenure record absent from scoped history")
	}
	boosts, e := f.boosts.Experience(f.ctx, "", f.realm.ID, f.actor, 50)
	if e != nil || len(boosts) != 1 || boosts[0].Delta != 1000 {
		t.Fatal("frozen boost ledger changed", boosts, e)
	}
	f.boostNow.Add(int64(24 * time.Hour))
	out, e = f.members.GrantTenure(f.ctx, f.actor, f.realm.ID, m.TenureRequest{Take: 100})
	if e != nil || out.Awarded != 2 {
		t.Fatal(out, e)
	}
	f.memberConfig.LevelThresholds = []int64{}
	f = configureRealmMember(t, f)
	p, e = f.members.Progression(f.ctx, "", f.realm.ID, f.actor)
	if e != nil || p.RecordedExperience != 1010 || p.Level != nil {
		t.Fatal("implicit level formula", p, e)
	}
	f.memberConfig.EnableDailyTenure = false
	f = configureRealmMember(t, f)
	if _, e = f.members.GrantTenure(f.ctx, f.actor, f.realm.ID, m.TenureRequest{Take: 100}); !errors.Is(e, m.ErrUnavailable) {
		t.Fatal("disabled tenure", e)
	}
}
func TestPGRealmTenureCurrentPerkAndConcurrentDeduplication(t *testing.T) {
	f := newRealmMemberFixture(t)
	f.cfg.Policy.PerkLevels[f.plan] = 3
	f.realmBoostFixture = configureRealmBoost(t, f.realmBoostFixture)
	f = configureRealmMember(t, f)
	query(t, f.db, `UPDATE owned_realm_members SET joined_at=$2::timestamptz WHERE realm_id=$1::uuid`, f.realm.ID, f.cfg.Now().Add(-48*time.Hour))
	s, e := f.r.CreateSubscription(f.ctx, f.actor, c.CreateSubscription{Identifier: f.plan}, "perk", false)
	if e != nil {
		t.Fatal(e)
	}
	o, e := f.r.SubscriptionOrder(f.ctx, f.actor, s.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			_, e := f.members.GrantTenure(f.ctx, f.actor, f.realm.ID, m.TenureRequest{Take: 100})
			errs <- e
		})
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_realm_tenure_experience WHERE realm_id=$1::uuid AND delta=12 AND perk_level=3`, f.realm.ID); n != 1 {
		t.Fatal("perk or dedup", n)
	}
	stored, e := f.r.Subscription(f.ctx, f.actor, s.ID)
	if e != nil {
		t.Fatal(e)
	}
	f.boostNow.Store(stored.EndedAt.UnixNano())
	if _, e = f.members.GrantTenure(f.ctx, f.actor, f.realm.ID, m.TenureRequest{Take: 100}); e != nil {
		t.Fatal(e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_realm_tenure_experience WHERE realm_id=$1::uuid AND delta=5 AND perk_level=0`, f.realm.ID); n != 1 {
		t.Fatal("expired perk remained effective", n)
	}
}
func TestPGBackendRealmMemberRoutesAndFeatureGates(t *testing.T) {
	f := newRealmMemberFixture(t)
	query(t, f.db, `UPDATE owned_realm_members SET joined_at=$2::timestamptz WHERE realm_id=$1::uuid AND account_id=$3::uuid`, f.realm.ID, f.cfg.Now().Add(-48*time.Hour), f.actor)
	unlockRealmLabels(t, f)
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	cfg := application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, RealmBoosts: &f.cfg, RealmMembers: &f.memberConfig}
	app, e := application.NewPostgres(f.db, nil, cfg)
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(f.ctx); e != nil {
		t.Fatal(e)
	}
	base := "/passport/realms/" + f.realm.ID
	backendCall(t, app, "POST", base+"/labels", "", `{"name":"A"}`, 401)
	v := backendDecode[social.RealmLabel](t, backendCall(t, app, "POST", base+"/labels", f.token, `{"name":"A","icon":"✨"}`, 200))
	backendCall(t, app, "PATCH", base+"/members/"+f.actor+"/label", f.token, `{"label_id":"`+v.ID+`"}`, 200)
	me := backendDecode[social.Member](t, backendCall(t, app, "GET", base+"/members/me", f.token, "", 200))
	if me.LabelID == nil || me.Progression == nil {
		t.Fatal(me)
	}
	backendCall(t, app, "GET", base+"/members?label_id="+v.ID, "", "", 200)
	backendCall(t, app, "GET", base+"/members/"+f.actor+"/progression/history", "", "", 200)
	w := backendCall(t, app, "GET", base+"/members/"+f.actor+"/experience", "", "", 200)
	if w.Header().Get("X-Realm-Experience-Scope") != "realm.boost.purchase,realm.tenure.daily" {
		t.Fatal("combined known-source experience route missing")
	}
	backendCall(t, app, "GET", base+"/labels", "invalid", "", 401)
	backendCall(t, app, "POST", base+"/progression/tenure", f.token, `{"day":"2020-01-01"}`, 400)
	backendCall(t, app, "POST", base+"/progression/tenure", f.token, `{}`, 200)
	historyPath := base + "/members/" + f.actor + "/experience"
	full := backendDecode[[]rb.Experience](t, backendCall(t, app, "GET", historyPath, "", "", 200))
	if len(full) != 2 || full[0].ID == full[1].ID || full[0].Delta+full[1].Delta != 1005 {
		t.Fatal("known ledger duplicated or omitted", full)
	}
	for _, prefix := range []string{"/passport", "/api"} {
		path := prefix + "/realms/" + f.realm.ID + "/members/" + f.actor + "/experience?take=1"
		first := backendDecode[[]rb.Experience](t, backendCall(t, app, "GET", path, "", "", 200))
		if len(first) != 1 || first[0].ID != full[0].ID {
			t.Fatal("unstable authored pagination", first, full)
		}
	}
	backendCall(t, app, "POST", base+"/progression/tenure", f.token, `{}`, 200)
	replay := backendDecode[[]rb.Experience](t, backendCall(t, app, "GET", historyPath, "", "", 200))
	if len(replay) != 2 || replay[0].ID != full[0].ID || replay[1].ID != full[1].ID {
		t.Fatal("replay changed history", replay)
	}

	cfg.RealmMembers = nil
	disabled, e := application.NewPostgres(f.db, nil, cfg)
	if e != nil {
		t.Fatal(e)
	}
	backendCall(t, disabled, "GET", base+"/labels", "", "", 503)
	old := backendDecode[[]rb.Experience](t, backendCall(t, disabled, "GET", historyPath, "", "", 200))
	if len(old) != 1 || old[0].ReasonType != "realm.boost.purchase" {
		t.Fatal("disabled member module broke boost history", old)
	}

	cfg.RealmMembers = &f.memberConfig
	cfg.RealmBoosts = nil
	if _, e = application.NewPostgres(f.db, nil, cfg); e == nil {
		t.Fatal("member labels enabled without live boosts")
	}
}
func TestPGRealmTenureMembershipWaitAndAuditRollback(t *testing.T) {
	f := newRealmMemberFixture(t)
	other, _ := addRealmMember(t, f)
	query(t, f.db, `UPDATE owned_realm_members SET joined_at=$2::timestamptz WHERE realm_id=$1::uuid`, f.realm.ID, f.cfg.Now().Add(-48*time.Hour))
	blocker, pid := atomicBlocker(t, f.atomicAuthorityFixture, "owned_realms", f.realm.ID)
	done := make(chan error, 1)
	go func() {
		_, e := f.members.GrantTenure(f.ctx, f.actor, f.realm.ID, m.TenureRequest{Take: 100})
		done <- e
	}()
	waitAtomicBlocked(t, f.atomicAuthorityFixture, pid, "owned_realms")
	if _, e := blocker.ExecContext(f.ctx, `UPDATE owned_realm_members SET leave_at=now() WHERE realm_id=$1::uuid AND account_id=$2::uuid`, f.realm.ID, other); e != nil {
		t.Fatal(e)
	}
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_realm_tenure_experience WHERE realm_id=$1::uuid AND account_id=$2::uuid`, f.realm.ID, other); n != 0 {
		t.Fatal("departed member awarded", n)
	}
	f.boostNow.Add(int64(24 * time.Hour))
	query(t, f.db, `CREATE FUNCTION synthetic_tenure_fail()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.actor_id::text=TG_ARGV[0] AND NEW.action='tenure.granted' THEN RAISE EXCEPTION 'synthetic tenure rollback';END IF;RETURN NEW;END$$`)
	query(t, f.db, fmt.Sprintf(`CREATE TRIGGER synthetic_tenure_fail BEFORE INSERT ON owned_realm_member_audit FOR EACH ROW EXECUTE FUNCTION synthetic_tenure_fail('%s')`, f.actor))
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DROP TRIGGER IF EXISTS synthetic_tenure_fail ON owned_realm_member_audit;DROP FUNCTION IF EXISTS synthetic_tenure_fail()`)
	})
	if _, e := f.members.GrantTenure(f.ctx, f.actor, f.realm.ID, m.TenureRequest{Take: 100}); e == nil {
		t.Fatal("tenure audit failure accepted")
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_realm_tenure_experience WHERE realm_id=$1::uuid`, f.realm.ID); n != 1 {
		t.Fatal("partial XP committed", n)
	}
	query(t, f.db, `DROP TRIGGER synthetic_tenure_fail ON owned_realm_member_audit`)
	out, e := f.members.GrantTenure(f.ctx, f.actor, f.realm.ID, m.TenureRequest{Take: 100})
	if e != nil || out.Awarded != 1 {
		t.Fatal("failed transaction consumed day", out, e)
	}
}
func TestPGRealmLabelsExpiryPreservesReadsAndBlocksReplay(t *testing.T) {
	f := newRealmMemberFixture(t)
	unlockRealmLabels(t, f)
	v := createRealmLabel(t, f, "stable", "Stable")
	if _, e := f.members.AssignLabel(f.ctx, f.actor, f.realm.ID, f.actor, &v.ID); e != nil {
		t.Fatal(e)
	}
	f.boostNow.Add(int64(30 * 24 * time.Hour))
	if _, e := f.members.UpdateLabel(f.ctx, f.actor, f.realm.ID, v.ID, m.LabelRequest{Name: "Stable"}); e != nil {
		t.Fatal("inclusive entitlement boundary", e)
	}
	f.boostNow.Add(int64(time.Microsecond))
	for _, run := range []func() error{func() error {
		_, e := f.members.CreateLabel(f.ctx, f.actor, f.realm.ID, "stable", m.LabelRequest{Name: "Stable"})
		return e
	}, func() error {
		_, e := f.members.UpdateLabel(f.ctx, f.actor, f.realm.ID, v.ID, m.LabelRequest{Name: "Stale"})
		return e
	}, func() error { return f.members.DeleteLabel(f.ctx, f.actor, f.realm.ID, v.ID) }, func() error { _, e := f.members.AssignLabel(f.ctx, f.actor, f.realm.ID, f.actor, nil); return e }} {
		if e := run(); !errors.Is(e, m.ErrForbidden) {
			t.Fatal("expired write succeeded", e)
		}
	}
	labels, e := f.members.Labels(f.ctx, "", f.realm.ID)
	if e != nil || len(labels) != 1 {
		t.Fatal("expired boost destroyed labels", labels, e)
	}
	me, e := f.social.Me(f.ctx, f.actor, f.realm.ID)
	if e != nil || me.LabelID == nil || me.Progression.RecordedExperience != 1000 {
		t.Fatal("expiry erased identity/XP", me, e)
	}
}
func TestPGRealmMemberRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	const marker = "realm-member-record.json"
	type saved struct {
		Actor, Realm, Payee, Token, Day string
		Authority                       restartAuthoritySnapshot
		At                              int64
		BoostPolicy                     realmboostpg.OwnedPolicy
		Thresholds                      []int64
		Label                           social.RealmLabel
		Recorded                        int64
		AuditCount                      int
	}
	if phase == "before" {
		f := newRealmMemberFixture(t)
		// Original one-hour synthetic proof is issued before target writes.
		f.config.AccessTTL, f.config.SessionTTL = time.Hour, time.Hour
		f.config.Now = func() time.Time { return time.Now().UTC() }
		var issueErr error
		f.issuer, issueErr = authority.New(authoritypg.New(f.db), f.config)
		if issueErr != nil {
			t.Fatal(issueErr)
		}
		f.token = pgIssue(t, f.issuer, f.actor).Token
		f.verifier, issueErr = authority.NewVerifier(authoritypg.New(f.db), f.config)
		if issueErr != nil {
			t.Fatal(issueErr)
		}
		f.principal, issueErr = f.verifier.Authenticate(f.ctx, f.token)
		if issueErr != nil {
			t.Fatal(issueErr)
		}
		f.guard = authoritypg.NewTransactionGuard(authoritypg.New(f.db), f.verifier)
		f.ctx = f.guard.Bind(f.ctx, f.principal)
		f.r = commercepg.New(f.db, f.guard, commercepg.Adapters{}).WithClock(f.cfg.Now)
		f.realmBoostFixture = configureRealmBoost(t, f.realmBoostFixture)
		f = configureRealmMember(t, f)
		unlockRealmLabels(t, f)
		query(t, f.db, `UPDATE owned_realm_members SET joined_at=$2::timestamptz WHERE realm_id=$1::uuid`, f.realm.ID, f.cfg.Now().Add(-48*time.Hour))
		label := createRealmLabel(t, f, "restart", "Persisted label")
		if _, e := f.members.AssignLabel(f.ctx, f.actor, f.realm.ID, f.actor, &label.ID); e != nil {
			t.Fatal(e)
		}
		out, e := f.members.GrantTenure(f.ctx, f.actor, f.realm.ID, m.TenureRequest{Take: 100})
		if e != nil || out.Awarded != 1 {
			t.Fatal(out, e)
		}
		p, e := f.members.Progression(f.ctx, f.actor, f.realm.ID, f.actor)
		if e != nil {
			t.Fatal(e)
		}
		v := saved{Actor: f.actor, Realm: f.realm.ID, Payee: f.payee.ID, Token: f.token, Authority: saveAuthoritySnapshot(t, f.config), At: f.boostNow.Load(), BoostPolicy: *f.cfg.Policy, Thresholds: f.memberConfig.LevelThresholds, Label: label, Day: out.Day, Recorded: p.RecordedExperience, AuditCount: commerceCount(t, f.db, `SELECT count(*) FROM owned_realm_member_audit WHERE realm_id=$1::uuid`, f.realm.ID)}
		saveRestartFixture(t, marker, v)
		return
	}
	var v saved
	loadRestartFixture(t, marker, &v)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cfg := restoreAuthoritySnapshot(t, v.Authority)
	store := authoritypg.New(db)
	verifier, e := authority.NewVerifier(store, cfg)
	if e != nil {
		t.Fatal(e)
	}
	proof, e := verifier.Authenticate(ctx, v.Token)
	if e != nil || proof.Account.ID != v.Actor {
		t.Fatal("saved proof", e)
	}
	guard := authoritypg.NewTransactionGuard(store, verifier)
	ctx = guard.Bind(ctx, proof)
	now := func() time.Time { return time.Unix(0, v.At).UTC() }
	checkout := commercepg.New(db, guard, commercepg.Adapters{}).WithClock(now)
	boosts, e := realmboostpg.New(db, guard, checkout, realmboostpg.Config{TreasuryAccountID: v.Actor, TreasuryWalletID: v.Payee, Policy: &v.BoostPolicy, Now: now})
	if e != nil {
		t.Fatal(e)
	}
	socialRepo := socialpg.NewWithAuthority(db, guard).WithBoosts(boosts)
	members, e := realmmemberpg.New(db, guard, boosts, socialRepo, realmmemberpg.Config{LevelThresholds: v.Thresholds, EnableDailyTenure: true, Now: now})
	if e != nil {
		t.Fatal(e)
	}
	socialRepo.WithMemberDetails(members)
	// Only adapters over the original saved state are reconstructed after restart.
	// No seeding, account creation, grant or token issuance occurs here.
	label, e := members.CreateLabel(ctx, v.Actor, v.Realm, "restart", m.LabelRequest{Name: v.Label.Name})
	if e != nil || label.ID != v.Label.ID || !label.CreatedAt.Equal(v.Label.CreatedAt) || !label.UpdatedAt.Equal(v.Label.UpdatedAt) {
		t.Fatal("replay changed persisted label", label, e)
	}
	me, e := socialRepo.Me(ctx, v.Actor, v.Realm)
	if e != nil || me.LabelID == nil || *me.LabelID != v.Label.ID || me.Progression == nil || me.Progression.RecordedExperience != v.Recorded || me.Progression.ExperienceComplete {
		t.Fatal("persisted identity/progression", me, e)
	}
	tenure, e := members.GrantTenure(ctx, v.Actor, v.Realm, m.TenureRequest{Take: 100})
	if e != nil || tenure.Day != v.Day || tenure.Awarded != 0 {
		t.Fatal("restart re-awarded tenure", tenure, e)
	}
	if n := commerceCount(t, db, `SELECT count(*) FROM owned_realm_member_audit WHERE realm_id=$1::uuid`, v.Realm); n != v.AuditCount {
		t.Fatal("restart changed audit count", n)
	}
	history, e := members.History(ctx, v.Actor, v.Realm, v.Actor, 50)
	if e != nil || len(history) != 2 {
		t.Fatal(history, e)
	}
	raw, e := json.Marshal(me.Progression)
	if e != nil || len(raw) == 0 {
		t.Fatal(e)
	}
}
func TestPGRealmLabelsUnknownRoleAndMemberPermissionDeny(t *testing.T) {
	f := newRealmMemberFixture(t)
	unlockRealmLabels(t, f)
	v := createRealmLabel(t, f, "", "Scoped")
	other, ctx := addRealmMember(t, f)
	query(t, f.db, `UPDATE owned_realm_members SET role=99 WHERE realm_id=$1::uuid AND account_id=$2::uuid`, f.realm.ID, other)
	if _, e := f.members.CreateLabel(ctx, other, f.realm.ID, "", m.LabelRequest{Name: "Unknown role"}); !errors.Is(e, m.ErrForbidden) {
		t.Fatal("unconfigured role authorized labels", e)
	}
	query(t, f.db, `UPDATE owned_realm_members SET role=50 WHERE realm_id=$1::uuid AND account_id=$2::uuid`, f.realm.ID, other)
	// A configured moderator rank may manage labels, but cannot assign them
	// after the canonical member-management permission has been explicitly denied.
	query(t, f.db, `INSERT INTO owned_realm_user_permissions(realm_id,account_id,member_id,membership_since,allow_mask,deny_mask)SELECT realm_id,account_id,id,joined_at,0,64 FROM owned_realm_members WHERE realm_id=$1::uuid AND account_id=$2::uuid`, f.realm.ID, other)
	if _, e := f.members.AssignLabel(ctx, other, f.realm.ID, other, &v.ID); !errors.Is(e, social.ErrForbidden) {
		t.Fatal("current membership permission ignored", e)
	}
	query(t, f.db, `UPDATE owned_realm_members SET permission_generation=2 WHERE realm_id=$1::uuid AND account_id=$2::uuid`, f.realm.ID, other)
	if _, e := f.members.CreateLabel(ctx, other, f.realm.ID, "", m.LabelRequest{Name: "Stale role"}); !errors.Is(e, m.ErrForbidden) {
		t.Fatal("stale role generation authorized labels", e)
	}
}
func TestPGRealmTenurePerkProjectionFailuresAndContention(t *testing.T) {
	for _, kind := range []string{"unpaid", "testing", "queued", "disabled", "mismatch", "contention"} {
		t.Run(kind, func(t *testing.T) {
			f := newRealmMemberFixture(t)
			f.cfg.Policy.PerkLevels[f.plan] = 3
			f.realmBoostFixture = configureRealmBoost(t, f.realmBoostFixture)
			f = configureRealmMember(t, f)
			query(t, f.db, `UPDATE owned_realm_members SET joined_at=$2::timestamptz WHERE realm_id=$1::uuid`, f.realm.ID, f.cfg.Now().Add(-48*time.Hour))
			s, e := f.r.CreateSubscription(f.ctx, f.actor, c.CreateSubscription{Identifier: f.plan}, "state", false)
			if e != nil {
				t.Fatal(e)
			}
			if kind != "unpaid" {
				o, e := f.r.SubscriptionOrder(f.ctx, f.actor, s.ID)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); e != nil {
					t.Fatal(e)
				}
				s, e = f.r.Subscription(f.ctx, f.actor, s.ID)
				if e != nil {
					t.Fatal(e)
				}
			}
			switch kind {
			case "testing":
				s.IsTesting = true
			case "queued":
				s.BegunAt = f.cfg.Now().Add(time.Hour)
			case "mismatch":
				s.AccountID = authorityUUID(t)
			case "disabled":
				query(t, f.db, `UPDATE commerce_v1.definitions SET enabled=false WHERE identifier=$1`, f.plan)
			}
			if kind == "testing" || kind == "queued" || kind == "mismatch" {
				raw, e := json.Marshal(s)
				if e != nil {
					t.Fatal(e)
				}
				query(t, f.db, `UPDATE commerce_v1.subscriptions SET begun_at=$2::timestamptz,document=$3::jsonb WHERE id=$1::uuid`, s.ID, s.BegunAt, raw)
			}
			if kind == "contention" {
				blocker, e := f.db.BeginTx(f.ctx, nil)
				if e != nil {
					t.Fatal(e)
				}
				defer blocker.Rollback()
				if _, e = blocker.ExecContext(f.ctx, `SELECT identifier FROM commerce_v1.definitions WHERE identifier=$1 FOR UPDATE`, f.plan); e != nil {
					t.Fatal(e)
				}
				if _, e = f.members.GrantTenure(f.ctx, f.actor, f.realm.ID, m.TenureRequest{Take: 100}); !errors.Is(e, m.ErrRetry) {
					t.Fatal("contended perk not rolled back", e)
				}
				if _, e = blocker.ExecContext(f.ctx, `UPDATE commerce_v1.definitions SET enabled=false WHERE identifier=$1`, f.plan); e != nil {
					t.Fatal(e)
				}
				if e = blocker.Commit(); e != nil {
					t.Fatal(e)
				}
			}
			out, e := f.members.GrantTenure(f.ctx, f.actor, f.realm.ID, m.TenureRequest{Take: 100})
			if kind == "mismatch" {
				if !errors.Is(e, c.ErrConflict) {
					t.Fatal("mismatched projection accepted", e)
				}
				if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_realm_tenure_experience WHERE realm_id=$1::uuid`, f.realm.ID); n != 0 {
					t.Fatal("invalid projection awarded XP")
				}
				return
			}
			if e != nil || out.Awarded != 1 {
				t.Fatal(out, e)
			}
			if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_realm_tenure_experience WHERE realm_id=$1::uuid AND delta=5 AND perk_level=0`, f.realm.ID); n != 1 {
				t.Fatal("ineligible perk applied", kind, n)
			}
		})
	}
}
func TestPGRealmTenureUsesTargetMemberPerk(t *testing.T) {
	f := newRealmMemberFixture(t)
	f.cfg.Policy.PerkLevels[f.plan] = 3
	f.realmBoostFixture = configureRealmBoost(t, f.realmBoostFixture)
	f = configureRealmMember(t, f)
	other, ctx := addRealmMember(t, f)
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, other, []authority.Rule{{Pattern: "realms.*"}, {Pattern: "orders.*"}, {Pattern: "subscriptions.*"}, {Pattern: "wallets.*"}}, nil); e != nil {
		t.Fatal(e)
	}
	var payer string
	if e := f.db.QueryRow(`SELECT id::text FROM wallet_v1.wallets WHERE account_id=$1::uuid AND realm_id IS NULL AND deleted_at IS NULL LIMIT 1`, other).Scan(&payer); e != nil {
		t.Fatal(e)
	}
	walletFund(t, f.db, wallet.Wallet{ID: payer}, "synthetic-commerce", "100.000")
	s, e := f.r.CreateSubscription(ctx, other, c.CreateSubscription{Identifier: f.plan}, "target-perk", false)
	if e != nil {
		t.Fatal(e)
	}
	o, e := f.r.SubscriptionOrder(ctx, other, s.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.PayOrder(ctx, other, o.ID, &payer); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE owned_realm_members SET joined_at=$2::timestamptz WHERE realm_id=$1::uuid`, f.realm.ID, f.cfg.Now().Add(-48*time.Hour))
	out, e := f.members.GrantTenure(f.ctx, f.actor, f.realm.ID, m.TenureRequest{Take: 100})
	if e != nil || out.Awarded != 2 {
		t.Fatal(out, e)
	}
	for account, want := range map[string]int64{f.actor: 5, other: 12} {
		var delta int64
		if e = f.db.QueryRow(`SELECT delta FROM owned_realm_tenure_experience WHERE realm_id=$1::uuid AND account_id=$2::uuid`, f.realm.ID, account).Scan(&delta); e != nil || delta != want {
			t.Fatal("wrong recipient perk", account, delta, e)
		}
	}
}
