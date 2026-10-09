// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/moderationpg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/authority"
	m "solsynth.local/go-migration/ports/passport/realmmembers"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere/posts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"strings"
	"testing"
	"time"
)

func realmProfilePerk(t *testing.T, f realmMemberFixture) (realmMemberFixture, c.Subscription) {
	t.Helper()
	f.cfg.Policy.PerkLevels[f.plan] = 2
	f.realmBoostFixture = configureRealmBoost(t, f.realmBoostFixture)
	f = configureRealmMember(t, f)
	s, e := f.r.CreateSubscription(f.ctx, f.actor, c.CreateSubscription{Identifier: f.plan}, "profile-perk", false)
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
	s, e = f.r.Subscription(f.ctx, f.actor, s.ID)
	if e != nil {
		t.Fatal(e)
	}
	return f, s
}
func realmProfileApp(t *testing.T, f realmMemberFixture) *application.Backend {
	t.Helper()
	v := int32(0)
	p := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	app, e := application.NewPostgres(f.db, nil, application.Config{Authority: f.config, PublicVisibility: &v, MessagerPolicy: &p, RealmBoosts: &f.cfg, RealmMembers: &f.memberConfig})
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(f.ctx); e != nil {
		t.Fatal(e)
	}
	return app
}
func realmQuotaPolicy(t *testing.T, db *sql.DB) {
	t.Helper()
	var old []byte
	e := db.QueryRow(`SELECT thresholds FROM owned_progression_level_policy WHERE id=1`).Scan(&old)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if old == nil {
			_, _ = db.Exec(`DELETE FROM owned_progression_level_policy WHERE id=1`)
		} else {
			_, _ = db.Exec(`INSERT INTO owned_progression_level_policy(id,thresholds,updated_at)VALUES(1,$1::jsonb,now())ON CONFLICT(id)DO UPDATE SET thresholds=EXCLUDED.thresholds`, old)
		}
	})
	query(t, db, `INSERT INTO owned_progression_level_policy(id,thresholds,updated_at)VALUES(1,'[0,100,1000]',now())ON CONFLICT(id)DO UPDATE SET thresholds=EXCLUDED.thresholds`)
}
func TestPGRealmProfileSelfReplacementCanonicalProjectionAndPerkExpiry(t *testing.T) {
	f := newRealmMemberFixture(t)
	if _, e := f.members.UpdateProfile(f.ctx, f.actor, f.realm.ID, m.ProfileRequest{Nick: postString("no perk")}); !errors.Is(e, m.ErrForbidden) {
		t.Fatal(e)
	}
	f, s := realmProfilePerk(t, f)
	// The source allows a normal member to edit only their own realm identity.
	other, ctx := addRealmMember(t, f)
	query(t, f.db, `INSERT INTO owned_realm_quota(account_id,total,starts_at)VALUES($1,3,now()-interval '1 day')`, other)
	realm, e := f.social.CreateRealm(ctx, other, social.RealmPatch{Slug: postString("profile-" + other), Name: postString("Other owner"), IsPublic: boolean(true), IsCommunity: boolean(true)})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.social.ChangeMember(f.ctx, f.actor, realm.ID, social.MemberChange{Action: social.Join}); e != nil {
		t.Fatal(e)
	}
	before, e := f.social.Me(f.ctx, f.actor, realm.ID)
	if e != nil {
		t.Fatal(e)
	}
	out, e := f.members.UpdateProfile(f.ctx, f.actor, realm.ID, m.ProfileRequest{Nick: postString("Realm nickname"), Bio: postString("Realm bio")})
	if e != nil || out.Role != social.Normal || out.Nick == nil || out.Account.Nick != before.Account.Nick {
		t.Fatal(out, e)
	}
	app := realmProfileApp(t, f)
	base := "/api/realms/" + realm.Slug
	me := backendDecode[social.Member](t, backendCall(t, app, "GET", base+"/members/me", f.token, "", 200))
	if me.Nick == nil || *me.Nick != "Realm nickname" || me.Bio == nil {
		t.Fatal(me)
	}
	out = backendDecode[social.Member](t, backendCall(t, app, "PATCH", base+"/members/me/profile", f.token, `{"bio":"Only bio"}`, 200))
	if out.Nick != nil || out.Bio == nil || *out.Bio != "Only bio" {
		t.Fatal("omitted override not cleared", out)
	}
	backendCall(t, app, "PATCH", base+"/members/"+other+"/profile", f.token, `{"nick":"other"}`, 404)
	f.boostNow.Store(s.EndedAt.UnixNano())
	if _, e = f.members.UpdateProfile(f.ctx, f.actor, realm.ID, m.ProfileRequest{}); !errors.Is(e, m.ErrForbidden) {
		t.Fatal("expired entitlement", e)
	}
	out, e = f.members.DeleteProfile(f.ctx, f.actor, realm.ID)
	if e != nil || out.Nick != nil || out.Bio != nil {
		t.Fatal("clear requires perk", out, e)
	}
	// Existing profile data is bound to the original join; rejoin never revives it.
	f.boostNow.Store(s.BegunAt.Add(time.Second).UnixNano())
	if _, e = f.members.UpdateProfile(f.ctx, f.actor, realm.ID, m.ProfileRequest{Nick: postString("old join")}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.social.ChangeMember(f.ctx, f.actor, realm.ID, social.MemberChange{Action: social.Leave}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.social.ChangeMember(f.ctx, f.actor, realm.ID, social.MemberChange{Action: social.Join}); e != nil {
		t.Fatal(e)
	}
	me, e = f.social.Me(f.ctx, f.actor, realm.ID)
	if e != nil || me.Nick != nil || me.Bio != nil {
		t.Fatal("stale profile revived", me, e)
	}
}
func TestPGRealmProfileWaitRechecksMembershipSessionScopeAndPerk(t *testing.T) {
	for _, mode := range []string{"membership", "session", "scope", "perk", "owner"} {
		t.Run(mode, func(t *testing.T) {
			f, s := realmProfilePerk(t, newRealmMemberFixture(t))
			table, id := "owned_realms", f.realm.ID
			if mode == "session" || mode == "scope" {
				table, id = "owned_authority_accounts", f.actor
			}
			blocker, pid := atomicBlocker(t, f.atomicAuthorityFixture, table, id)
			done := make(chan error, 1)
			go func() {
				_, e := f.members.UpdateProfile(f.ctx, f.actor, f.realm.ID, m.ProfileRequest{Nick: postString("stale")})
				done <- e
			}()
			waitAtomicBlocked(t, f.atomicAuthorityFixture, pid, table)
			var e error
			switch mode {
			case "membership":
				_, e = blocker.ExecContext(f.ctx, `UPDATE owned_realm_members SET leave_at=now() WHERE realm_id=$1 AND account_id=$2`, f.realm.ID, f.actor)
			case "session":
				_, e = blocker.ExecContext(f.ctx, `UPDATE owned_authority_sessions SET revoked_at=now(),epoch=epoch+1 WHERE id=$1`, f.principal.Session.ID)
			case "scope":
				_, e = blocker.ExecContext(f.ctx, `UPDATE owned_authority_sessions SET scopes='[]'::jsonb WHERE id=$1`, f.principal.Session.ID)
			case "perk":
				f.boostNow.Store(s.EndedAt.UnixNano())
			case "owner":
				_, e = blocker.ExecContext(f.ctx, `UPDATE owned_realms SET deleted_at=now() WHERE id=$1`, f.realm.ID)
			}
			if e != nil {
				t.Fatal(e)
			}
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			if e = <-done; e == nil {
				t.Fatal("stale write accepted", mode)
			}
			if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_realm_member_profiles WHERE realm_id=$1`, f.realm.ID); n != 0 {
				t.Fatal(n)
			}
		})
	}
}
func TestPGRealmProfileAuditRollbackAndSubscriptionContention(t *testing.T) {
	f, s := realmProfilePerk(t, newRealmMemberFixture(t))
	query(t, f.db, `CREATE FUNCTION synthetic_profile_fail()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.actor_id::text=TG_ARGV[0] THEN RAISE EXCEPTION 'synthetic profile rollback';END IF;RETURN NEW;END$$`)
	query(t, f.db, fmt.Sprintf(`CREATE TRIGGER synthetic_profile_fail BEFORE INSERT ON owned_realm_profile_audit FOR EACH ROW EXECUTE FUNCTION synthetic_profile_fail('%s')`, f.actor))
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DROP TRIGGER IF EXISTS synthetic_profile_fail ON owned_realm_profile_audit;DROP FUNCTION IF EXISTS synthetic_profile_fail()`)
	})
	if _, e := f.members.UpdateProfile(f.ctx, f.actor, f.realm.ID, m.ProfileRequest{Nick: postString("rollback")}); e == nil {
		t.Fatal("audit failure accepted")
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_realm_member_profiles WHERE realm_id=$1`, f.realm.ID); n != 0 {
		t.Fatal(n)
	}
	query(t, f.db, `DROP TRIGGER synthetic_profile_fail ON owned_realm_profile_audit`)
	blocker, e := f.db.BeginTx(f.ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer blocker.Rollback()
	if _, e = blocker.ExecContext(f.ctx, `SELECT id FROM commerce_v1.subscriptions WHERE id=$1 FOR UPDATE`, s.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.members.UpdateProfile(f.ctx, f.actor, f.realm.ID, m.ProfileRequest{Nick: postString("blocked")}); !errors.Is(e, m.ErrRetry) {
		t.Fatal(e)
	}
	if e = blocker.Rollback(); e != nil {
		t.Fatal(e)
	}
	if _, e = f.members.UpdateProfile(f.ctx, f.actor, f.realm.ID, m.ProfileRequest{Nick: postString("committed")}); e != nil {
		t.Fatal(e)
	}
}
func TestPGRealmQuotaConfiguredGrantShapeAndExpiry(t *testing.T) {
	f := newRealmMemberFixture(t)
	realmQuotaPolicy(t, f.db)
	f, _ = realmProfilePerk(t, f)
	app := realmProfileApp(t, f)
	w := backendCall(t, app, "GET", "/passport/realms/quota", f.token, "", 200)
	q := backendDecode[m.Quota](t, w)
	if q.Total != 20 || q.Used != 1 || q.Remaining != 19 || q.Level != 1 || q.PerkLevel != 2 || len(q.Records) != 1 || q.Records[0].ID != f.realm.ID || w.Header().Get("X-Realm-Quota-Policy") != "owned.configured.grant" {
		t.Fatal(q, w.Header())
	}
	query(t, f.db, `UPDATE owned_realm_quota SET expires_at=now()-interval '1 second' WHERE account_id=$1`, f.actor)
	q, e := f.members.Quota(f.ctx, f.actor)
	if e != nil || q.Total != 0 || q.Used != 1 || q.Remaining != 0 {
		t.Fatal(q, e)
	}
	if _, e = f.social.CreateRealm(f.ctx, f.actor, social.RealmPatch{Slug: postString("quota-denied-" + f.actor), Name: postString("Denied")}); !errors.Is(e, social.ErrQuota) {
		t.Fatal("read disagrees with creation", e)
	}
	if e = f.social.DeleteRealm(f.ctx, f.actor, f.realm.ID); e != nil {
		t.Fatal(e)
	}
	q, e = f.members.Quota(f.ctx, f.actor)
	if e != nil || q.Used != 0 || len(q.Records) != 0 {
		t.Fatal("deleted realm counted", q, e)
	}
	query(t, f.db, `DELETE FROM owned_progression_level_policy WHERE id=1`)
	w = backendCall(t, app, "GET", "/api/realms/quota", f.token, "", 503)
	if !strings.Contains(w.Body.String(), "REALM_QUOTA_POLICY_UNCONFIGURED") {
		t.Fatal(w.Body.String())
	}
}
func TestPGRealmModerationLogCanonicalRemovalPrivacyAndRevocation(t *testing.T) {
	f := newRealmMemberFixture(t)
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
		t.Fatal(e)
	}
	f.posts = postpg.NewWithAuthority(f.db, f.guard).WithVisibility(f.social)
	admin, e := moderationpg.New(f.db, f.guard, moderationpg.Config{Now: f.cfg.Now})
	if e != nil {
		t.Fatal(e)
	}
	p, e := f.posts.Create(f.ctx, f.actor, f.publisher, posts.Patch{Content: postString("Private body must not appear"), RealmID: &f.realm.ID})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = admin.ModerateRealmPost(f.ctx, f.actor, p.ID, postString("Realm removal reason")); e != nil {
		t.Fatal(e)
	}
	otherRealm, e := f.social.CreateRealm(f.ctx, f.actor, social.RealmPatch{Slug: postString("other-log-" + f.actor), Name: postString("Other log realm"), IsPublic: boolean(true)})
	if e != nil {
		t.Fatal(e)
	}
	otherPost, e := f.posts.Create(f.ctx, f.actor, f.publisher, posts.Patch{Content: postString("Other realm secret"), RealmID: &otherRealm.ID})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = admin.ModerateRealmPost(f.ctx, f.actor, otherPost.ID, postString("Other realm reason")); e != nil {
		t.Fatal(e)
	}
	app := realmProfileApp(t, f)
	path := "/api/realms/" + f.realm.Slug + "/posts/moderation-logs"
	w := backendCall(t, app, "GET", path, f.token, "", 200)
	logs := backendDecode[[]m.ModerationLog](t, w)
	if len(logs) != 1 || logs[0].PostID != p.ID || logs[0].RealmID != f.realm.ID || logs[0].ID == "" || logs[0].Reason == nil || w.Header().Get("X-Total") != "1" {
		t.Fatal(logs)
	}
	var wire []map[string]json.RawMessage
	if json.Unmarshal(w.Body.Bytes(), &wire) != nil || len(wire[0]) != 9 {
		t.Fatal("unexpected private fields", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "Private body") || strings.Contains(w.Body.String(), "automod") || strings.Contains(w.Body.String(), "reviewer") || strings.Contains(w.Body.String(), "Other realm") {
		t.Fatal(w.Body.String())
	}
	backendCall(t, app, "GET", path, "", "", 401)
	if got, total, e := f.members.ModerationLogs(f.ctx, f.actor, f.realm.ID, 1, 1); e != nil || len(got) != 0 || total != 1 {
		t.Fatal(got, total, e)
	}
	other, ctx := addRealmMember(t, f)
	if _, _, e = f.members.ModerationLogs(ctx, other, f.realm.ID, 0, 20); !errors.Is(e, m.ErrForbidden) {
		t.Fatal(e)
	}
	query(t, f.db, `INSERT INTO owned_realm_user_permissions(realm_id,account_id,member_id,membership_since,allow_mask,deny_mask)SELECT realm_id,account_id,id,joined_at,16,0 FROM owned_realm_members WHERE realm_id=$1 AND account_id=$2`, f.realm.ID, other)
	if got, _, e := f.members.ModerationLogs(ctx, other, f.realm.ID, 0, 20); e != nil || len(got) != 1 {
		t.Fatal(got, e)
	}
	blocker, pid := atomicBlocker(t, f.atomicAuthorityFixture, "owned_realms", f.realm.ID)
	done := make(chan error, 1)
	go func() { _, _, e := f.members.ModerationLogs(ctx, other, f.realm.ID, 0, 20); done <- e }()
	waitAtomicBlocked(t, f.atomicAuthorityFixture, pid, "owned_realms")
	if _, e = blocker.ExecContext(f.ctx, `UPDATE owned_realm_user_permissions SET allow_mask=0,deny_mask=16 WHERE realm_id=$1 AND account_id=$2`, f.realm.ID, other); e != nil {
		t.Fatal(e)
	}
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, m.ErrForbidden) {
		t.Fatal("revoked read leaked", e)
	}
}

func TestPGRealmQuotaRevalidatesAfterAccountWait(t *testing.T) {
	for _, mode := range []string{"session", "expiry", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			f := newRealmMemberFixture(t)
			realmQuotaPolicy(t, f.db)
			blocker, pid := atomicBlocker(t, f.atomicAuthorityFixture, "owned_authority_accounts", f.actor)
			type result struct {
				q m.Quota
				e error
			}
			done := make(chan result, 1)
			go func() { q, e := f.members.Quota(f.ctx, f.actor); done <- result{q, e} }()
			waitAtomicBlocked(t, f.atomicAuthorityFixture, pid, "owned_authority_accounts")
			var e error
			switch mode {
			case "session":
				_, e = blocker.ExecContext(f.ctx, `UPDATE owned_authority_sessions SET revoked_at=now(),epoch=epoch+1 WHERE id=$1`, f.principal.Session.ID)
			case "expiry":
				_, e = blocker.ExecContext(f.ctx, `UPDATE owned_realm_quota SET expires_at=now()-interval '1 second' WHERE account_id=$1`, f.actor)
			case "disabled":
				_, e = blocker.ExecContext(f.ctx, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor)
			}
			if e != nil {
				t.Fatal(e)
			}
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			out := <-done
			if mode == "expiry" {
				if out.e != nil || out.q.Total != 0 || out.q.Remaining != 0 {
					t.Fatal(out)
				}
			} else if out.e == nil {
				t.Fatal("stale quota authority", out)
			}
		})
	}
}

func TestPGRealmProfileAndQuotaPerkExpiresDuringDefinitionWait(t *testing.T) {
	for _, mode := range []string{"profile", "quota"} {
		for _, table := range []string{"subscriptions", "definitions"} {
			for _, transition := range []string{"expires", "begins", "policy-expires"} {
				t.Run(mode+"/"+table+"/"+transition, func(t *testing.T) {
					f, s := realmProfilePerk(t, newRealmMemberFixture(t))
					if mode == "quota" {
						realmQuotaPolicy(t, f.db)
					}
					// Prepare coherent synthetic rows before the later table lock. The
					// blocker never writes grants, punishments or other account policy.
					var boundary time.Time
					if e := f.db.QueryRowContext(f.ctx, `SELECT clock_timestamp()+interval '2 seconds'`).Scan(&boundary); e != nil {
						t.Fatal(e)
					}
					s.EndedAt = boundary
					if transition == "begins" {
						s.BegunAt = boundary
						s.EndedAt = boundary.Add(time.Hour)
					} else if transition == "policy-expires" {
						s.EndedAt = boundary.Add(time.Hour)
					}
					raw, e := json.Marshal(s)
					if e != nil {
						t.Fatal(e)
					}
					query(t, f.db, `UPDATE commerce_v1.subscriptions SET begun_at=$2::timestamptz,ended_at=$3::timestamptz,document=$4::jsonb WHERE id=$1::uuid`, s.ID, s.BegunAt, s.EndedAt, raw)
					blocker, e := f.db.BeginTx(f.ctx, nil)
					if e != nil {
						t.Fatal(e)
					}
					defer blocker.Rollback()
					var pid int
					if e = blocker.QueryRowContext(f.ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
						t.Fatal(e)
					}
					// table is chosen solely from the fixed literals above.
					if _, e = blocker.ExecContext(f.ctx, `LOCK TABLE commerce_v1.`+table+` IN ACCESS EXCLUSIVE MODE`); e != nil {
						t.Fatal(e)
					}
					type result struct {
						perk int
						e    error
					}
					done := make(chan result, 1)
					go func() {
						if mode == "profile" {
							_, e := f.members.UpdateProfile(f.ctx, f.actor, f.realm.ID, m.ProfileRequest{Nick: postString("current")})
							done <- result{0, e}
						} else {
							q, e := f.members.Quota(f.ctx, f.actor)
							done <- result{q.PerkLevel, e}
						}
					}()
					waitAtomicBlocked(t, f.atomicAuthorityFixture, pid, "commerce_v1."+table)
					if transition == "policy-expires" {
						f.boostNow.Store(s.EndedAt.UnixNano())
					} else if _, e = blocker.ExecContext(f.ctx, `SELECT pg_sleep(GREATEST(0,extract(epoch FROM $1::timestamptz-clock_timestamp())::double precision)+0.01)`, boundary); e != nil {
						t.Fatal(e)
					}
					if e = blocker.Commit(); e != nil {
						t.Fatal(e)
					}
					out := <-done
					wantPerk, wantProfiles := 0, 0
					if transition == "begins" {
						wantPerk = 2
						if mode == "profile" {
							wantProfiles = 1
						}
					}
					if mode == "profile" {
						if wantPerk == 2 && out.e != nil || wantPerk == 0 && !errors.Is(out.e, m.ErrForbidden) {
							t.Fatal("wrong final profile entitlement", out.e)
						}
					} else if out.e != nil || out.perk != wantPerk {
						t.Fatal("stale quota perk", out)
					}
					if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_realm_member_profiles WHERE realm_id=$1`, f.realm.ID); n != wantProfiles {
						t.Fatal(n)
					}
					if transition == "expires" {
						// An old caller clock must never resurrect a real expired cycle.
						f.boostNow.Store(s.BegunAt.UnixNano())
						if mode == "profile" {
							if _, e = f.members.UpdateProfile(f.ctx, f.actor, f.realm.ID, m.ProfileRequest{}); !errors.Is(e, m.ErrForbidden) {
								t.Fatal("old policy clock resurrected profile perk", e)
							}
						} else if q, e := f.members.Quota(f.ctx, f.actor); e != nil || q.PerkLevel != 0 {
							t.Fatal("old policy clock resurrected quota perk", q, e)
						}
					}
				})
			}
		}
	}
}
