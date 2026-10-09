// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/chatpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere/posts"
	"strings"
	"testing"
	"time"
)

func realmRoleRequest(level social.Role, mask int) social.RolePermissionRequest {
	return social.RolePermissionRequest{RoleLevel: &level, PermissionOverrides: social.OverridesFromMasks(mask, (^mask)&255)}
}
func realmPermissionJoin(t *testing.T, r *socialpg.Repository, actor, id string) {
	t.Helper()
	if _, e := r.ChangeMember(context.Background(), actor, id, social.MemberChange{Action: social.Join}); e != nil {
		t.Fatal(e)
	}
}
func realmPermissionRole(t *testing.T, r *socialpg.Repository, actor, id, target string, role social.Role) {
	t.Helper()
	if _, e := r.ChangeMember(context.Background(), actor, id, social.MemberChange{Action: social.ChangeRole, AccountID: target, Role: role}); e != nil {
		t.Fatal(e)
	}
}
func realmPermissionMask(t *testing.T, r *socialpg.Repository, actor, id string, want int) {
	t.Helper()
	p, e := r.EffectivePermissions(context.Background(), actor, id)
	if e != nil || p.Mask() != want {
		t.Fatalf("permission mask %d want %d: %v", p.Mask(), want, e)
	}
}
func TestPGRealmPermissionLifecycleAndGenerationIsolation(t *testing.T) {
	db, r, ids := socialFixture(t)
	ctx := context.Background()
	owner, member := ids[0], ids[1]
	v := socialRealm(t, r, owner, true, true)
	realmPermissionJoin(t, r, member, v.ID)
	query(t, db, `UPDATE owned_realms SET boost_level=2 WHERE id=$1`, v.ID)
	role, e := r.SetRolePermission(ctx, owner, v.ID, realmRoleRequest(17, 15))
	if e != nil || role.Generation != 1 {
		t.Fatal(role, e)
	}
	realmPermissionRole(t, r, owner, v.ID, member, 17)
	realmPermissionMask(t, r, member, v.ID, 15)
	deny := false
	if _, e = r.SetUserPermission(ctx, owner, v.ID, social.UserPermissionRequest{AccountID: member, PermissionOverrides: social.PermissionOverrides{CanPost: &deny}}); e != nil {
		t.Fatal(e)
	}
	realmPermissionMask(t, r, member, v.ID, 13)
	realmPermissionMask(t, r, owner, v.ID, 255)
	if e = r.DeleteRolePermission(ctx, owner, v.ID, 17); e != nil {
		t.Fatal(e)
	}
	realmPermissionMask(t, r, member, v.ID, 0)
	role, e = r.SetRolePermission(ctx, owner, v.ID, realmRoleRequest(17, 15))
	if e != nil || role.Generation != 2 {
		t.Fatal(role, e)
	}
	realmPermissionMask(t, r, member, v.ID, 0)
	if u, e := r.UserPermissions(ctx, owner, v.ID, member); e != nil || u != nil {
		t.Fatal("deleted role retained override", u, e)
	}
	realmPermissionRole(t, r, owner, v.ID, member, 17)
	realmPermissionMask(t, r, member, v.ID, 15)
	second := socialRealm(t, r, owner, true, true)
	realmPermissionJoin(t, r, member, second.ID)
	query(t, db, `UPDATE owned_realms SET boost_level=2 WHERE id=$1`, second.ID)
	if _, e = r.ChangeMember(ctx, owner, second.ID, social.MemberChange{Action: social.ChangeRole, AccountID: member, Role: 17}); !errors.Is(e, social.ErrNotFound) {
		t.Fatal("cross-realm role definition reused", e)
	}
	if _, e = r.SetRolePermission(ctx, member, v.ID, realmRoleRequest(18, 255)); !errors.Is(e, social.ErrForbidden) {
		t.Fatal("member authored grants", e)
	}
}
func TestPGRealmPermissionEscalationOwnerAndMembershipIncarnation(t *testing.T) {
	db, r, ids := socialFixture(t)
	ctx := context.Background()
	owner, manager, member := ids[0], ids[1], ids[2]
	v := socialRealm(t, r, owner, true, true)
	for _, a := range []string{manager, member} {
		realmPermissionJoin(t, r, a, v.ID)
	}
	query(t, db, `UPDATE owned_realms SET boost_level=2 WHERE id=$1`, v.ID)
	if _, e := r.SetRolePermission(ctx, owner, v.ID, realmRoleRequest(75, 207)); e != nil {
		t.Fatal(e)
	}
	realmPermissionRole(t, r, owner, v.ID, manager, 75)
	for _, p := range []social.RolePermissionRequest{realmRoleRequest(25, 255), realmRoleRequest(75, 207), realmRoleRequest(100, 255)} {
		if _, e := r.SetRolePermission(ctx, manager, v.ID, p); e == nil {
			t.Fatal("role escalation accepted", p)
		}
	}
	yes := true
	if _, e := r.SetUserPermission(ctx, manager, v.ID, social.UserPermissionRequest{AccountID: manager, PermissionOverrides: social.PermissionOverrides{CanModerateChat: &yes}}); !errors.Is(e, social.ErrForbidden) {
		t.Fatal("self escalation", e)
	}
	if _, e := r.SetUserPermission(ctx, manager, v.ID, social.UserPermissionRequest{AccountID: owner, PermissionOverrides: social.PermissionOverrides{CanChat: &yes}}); !errors.Is(e, social.ErrForbidden) {
		t.Fatal("owner override", e)
	}
	for _, rank := range []social.Role{0, 50, 100} {
		if e := r.DeleteRolePermission(ctx, owner, v.ID, rank); !errors.Is(e, social.ErrForbidden) {
			t.Fatal("built-in deleted", rank, e)
		}
	}
	if _, e := r.SetUserPermission(ctx, owner, v.ID, social.UserPermissionRequest{AccountID: member, PermissionOverrides: social.PermissionOverrides{CanManageRealm: &yes}}); e != nil {
		t.Fatal(e)
	}
	realmPermissionMask(t, r, member, v.ID, 143)
	if _, e := r.ChangeMember(ctx, member, v.ID, social.MemberChange{Action: social.Leave}); e != nil {
		t.Fatal(e)
	}
	realmPermissionJoin(t, r, member, v.ID)
	realmPermissionMask(t, r, member, v.ID, 15)
	// Canonical realm ownership is authoritative even if a numeric row is corrupt.
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if _, e = tx.Exec(`UPDATE owned_realm_members SET role=0 WHERE realm_id=$1 AND account_id=$2`, v.ID, owner); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(`UPDATE owned_realm_members SET role=100 WHERE realm_id=$1 AND account_id=$2`, v.ID, member); e != nil {
		t.Fatal(e)
	}
	p, e := r.RealmPermissionsInTransaction(ctx, tx, owner, v.ID)
	if e != nil || p.Mask() != 255 {
		t.Fatal("canonical owner locked out", p, e)
	}
	p, e = r.RealmPermissionsInTransaction(ctx, tx, member, v.ID)
	if e != nil || p.Mask() != 0 {
		t.Fatal("numeric owner granted authority", p, e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		query(t, db, `UPDATE owned_realm_members SET role=0 WHERE realm_id=$1 AND account_id=$2`, v.ID, member)
		query(t, db, `UPDATE owned_realm_members SET role=100 WHERE realm_id=$1 AND account_id=$2`, v.ID, owner)
	})
	if _, e = r.ChangeMember(ctx, manager, v.ID, social.MemberChange{Action: social.ChangeRole, AccountID: owner, Role: 0}); !errors.Is(e, social.ErrForbidden) {
		t.Fatal("lowered owner rank permitted delegated role mutation", e)
	}

}
func TestPGRealmPermissionRevocationWaitAndRollback(t *testing.T) {
	db, r, ids := socialFixture(t)
	ctx := context.Background()
	owner, member := ids[0], ids[1]
	v := socialRealm(t, r, owner, true, true)
	realmPermissionJoin(t, r, member, v.ID)
	revoker, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer revoker.Rollback()
	if _, e = revoker.Exec(`SELECT id FROM owned_realms WHERE id=$1 FOR UPDATE`, v.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = revoker.Exec(`UPDATE owned_realm_role_permissions SET permission_mask=permission_mask & (~1) WHERE realm_id=$1 AND role_level=0`, v.ID); e != nil {
		t.Fatal(e)
	}
	writer, e := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		t.Fatal(e)
	}
	defer writer.Rollback()
	var pid int
	if e = writer.QueryRow(`SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	result := make(chan error, 1)
	go func() { result <- r.RealmPermissionForWriteInTransaction(ctx, writer, member, v.ID, social.CanChat) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if e = db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND wait_event_type='Lock')`, pid).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("permission writer did not wait")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if e = revoker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-result; !errors.Is(e, social.ErrForbidden) {
		t.Fatal("queued writer retained revoked permission", e)
	}
	if e = writer.Rollback(); e != nil {
		t.Fatal(e)
	}
	// Trigger failure proves that permission changes and audit are one transaction.
	trigger := "deny_permission_" + strings.ReplaceAll(owner, "-", "")
	query(t, db, fmt.Sprintf(`CREATE FUNCTION %s()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.actor_id='%s'::uuid THEN RAISE EXCEPTION 'synthetic permission audit failure';END IF;RETURN NEW;END$$;CREATE TRIGGER %s BEFORE INSERT ON owned_social_audit FOR EACH ROW EXECUTE FUNCTION %s()`, trigger, owner, trigger, trigger))
	t.Cleanup(func() { query(t, db, `DROP TRIGGER `+trigger+` ON owned_social_audit;DROP FUNCTION `+trigger+`()`) })
	if _, e = r.SetRolePermission(ctx, owner, v.ID, realmRoleRequest(22, 255)); e == nil {
		t.Fatal("audit failure ignored")
	}
	var n int
	if e = db.QueryRow(`SELECT count(*) FROM owned_realm_role_permissions WHERE realm_id=$1 AND role_level=22`, v.ID).Scan(&n); e != nil || n != 0 {
		t.Fatal("failed permission write survived", n, e)
	}
}
func TestPGRealmPermissionCanonicalChatPostAndReplyEnforcement(t *testing.T) {
	f := newRealmPublishingFixture(t)
	ctx := f.ctx
	yes := true
	query(t, f.db, `UPDATE owned_realms SET boost_level=2 WHERE id=$1`, f.private.ID)
	if _, e := f.social.SetRolePermission(ctx, f.actor, f.private.ID, realmRoleRequest(21, 47)); e != nil {
		t.Fatal(e)
	}
	if _, e := f.social.ChangeMember(ctx, f.actor, f.private.ID, social.MemberChange{Action: social.ChangeRole, AccountID: f.other, Role: 21}); e != nil {
		t.Fatal(e)
	}
	chat, e := chatpg.NewWithAuthority(f.db, chatPolicy, f.guard)
	if e != nil {
		t.Fatal(e)
	}
	chat.WithSocial(f.social)
	room, e := chat.Create(f.otherCtx, f.other, rooms.RoomPatch{Name: postString("Custom role room"), RealmID: &f.private.ID, IsPublic: &yes, IsCommunity: &yes})
	if e != nil {
		t.Fatal("explicit lower-rank chat moderation grant ignored", e)
	}
	if _, e = chat.Send(f.otherCtx, f.other, room.ID, rooms.TextRequest{Content: "allowed"}); e != nil {
		t.Fatal(e)
	}
	deny := false
	if _, e = f.social.SetUserPermission(ctx, f.actor, f.private.ID, social.UserPermissionRequest{AccountID: f.other, PermissionOverrides: social.PermissionOverrides{CanChat: &deny, CanPost: &deny}}); e != nil {
		t.Fatal(e)
	}
	if _, e = chat.Send(f.otherCtx, f.other, room.ID, rooms.TextRequest{Content: "denied"}); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal("chat bypassed current override", e)
	}
	name := "permission-personal-" + strings.ReplaceAll(f.other, "-", "")
	personal, e := f.accounts.CreatePublisher(f.otherCtx, f.other, accounts.PublisherPatch{Name: &name, Nick: &name})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.posts.Create(f.otherCtx, f.other, personal.ID, posts.Patch{Content: postString("denied realm post"), RealmID: &f.private.ID}); !errors.Is(e, posts.ErrForbidden) {
		t.Fatal("post bypassed current override", e)
	}
	parent := f.post(t, f.individual, "Realm reply target", 0)
	// Bind the parent explicitly to the realm using the canonical post mutation.
	parent, e = f.posts.Update(f.ctx, f.actor, parent.ID, posts.Patch{RealmID: &f.private.ID})
	if e != nil {
		t.Fatal(e)
	}
	explicitReply, x := f.posts.Create(f.otherCtx, f.other, personal.ID, posts.Patch{Content: postString("Explicit realm comment"), RepliedPostID: &parent.ID, RealmID: &f.private.ID})
	if x != nil {
		t.Fatal(x)
	}
	if _, x = f.posts.Update(f.otherCtx, f.other, explicitReply.ID, posts.Patch{Content: postString("Comment-only edit remains allowed")}); x != nil {
		t.Fatal("persisted reply selected post permission", x)
	}
	reply, e := f.posts.Create(f.otherCtx, f.other, personal.ID, posts.Patch{Content: postString("comment permission remains"), RepliedPostID: &parent.ID})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.social.SetUserPermission(ctx, f.actor, f.private.ID, social.UserPermissionRequest{AccountID: f.other, PermissionOverrides: social.PermissionOverrides{CanComment: &deny}}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.posts.Create(f.otherCtx, f.other, personal.ID, posts.Patch{Content: postString("denied new reply"), RepliedPostID: &parent.ID}); !errors.Is(e, posts.ErrForbidden) {
		t.Fatal("reply creation bypassed override", e)
	}
	if _, e = f.posts.Update(f.otherCtx, f.other, reply.ID, posts.Patch{Content: postString("denied reply edit")}); !errors.Is(e, posts.ErrForbidden) {
		t.Fatal("reply edit bypassed override", e)
	}
}
func TestPGRealmPermissionBackendDTOAndNullClear(t *testing.T) {
	f := newBackendFixture(t)
	query(t, f.db, `INSERT INTO owned_realm_quota(account_id,total,starts_at)VALUES($1,2,now())`, f.actor)
	slug := "permissions-" + f.actor
	v := backendDecode[social.Realm](t, backendJSONCall(t, f.app, "POST", "/passport/realms", f.token, social.RealmPatch{Slug: &slug, Name: postString("Permission route"), IsPublic: boolean(true), IsCommunity: boolean(true)}, 200))
	backendCall(t, f.app, "POST", "/passport/realms/"+slug+"/members/me", f.otherToken, "", 200)
	role := backendDecode[social.RolePermission](t, backendJSONCall(t, f.app, "POST", "/passport/realms/"+slug+"/permissions/roles", f.token, realmRoleRequest(17, 15), 200))
	if role.RoleLevel != 17 {
		t.Fatal(role)
	}
	backendCall(t, f.app, "GET", "/passport/realms/"+slug+"/permissions/roles", f.otherToken, "", 403)
	yes := true
	backendJSONCall(t, f.app, "POST", "/passport/realms/"+slug+"/permissions/users", f.token, social.UserPermissionRequest{AccountID: f.other, PermissionOverrides: social.PermissionOverrides{CanManageRealm: &yes}}, 200)
	p := backendDecode[social.RealmPermissions](t, backendCall(t, f.app, "GET", "/passport/realms/"+slug+"/permissions/me", f.otherToken, "", 200))
	if !p.CanManageRealm {
		t.Fatal(p)
	}
	body := fmt.Sprintf(`{"account_id":%q,"canChat":null,"canPost":null,"canComment":null,"canUploadMedia":null,"canModeratePosts":null,"canModerateChat":null,"canManageMembers":null,"canManageRealm":null}`, f.other)
	backendCall(t, f.app, "POST", "/passport/realms/"+slug+"/permissions/users", f.token, body, 200)
	p = backendDecode[social.RealmPermissions](t, backendCall(t, f.app, "GET", "/passport/realms/"+slug+"/permissions/me", f.otherToken, "", 200))
	if p.CanManageRealm || p.Mask() != 15 {
		t.Fatal("clear did not restore role defaults", p)
	}
	backendCall(t, f.app, "POST", "/passport/realms/"+slug+"/permissions/users", f.token, fmt.Sprintf(`{"account_id":%q,"can_chat":true,"can_chat":false}`, f.other), 400)
	backendCall(t, f.app, "DELETE", "/passport/realms/"+slug+"/permissions/roles/17", f.token, "", 204)
	_ = v
}

// The after phase is strictly read-only; the harness must restart PostgreSQL.
func TestPGRealmPermissionRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	r := socialpg.New(db)
	ctx := context.Background()
	var saved struct {
		Owner, Member, RealmID, MemberID string
		MemberJoinedAt                   time.Time
		MemberGeneration                 int64
		Role                             social.RolePermission
		Override                         social.UserPermission
	}
	const marker = "realm-permission-restart.json"
	if phase == "before" {
		saved.Owner, saved.Member = authorityUUID(t), authorityUUID(t)
		for _, id := range []string{saved.Owner, saved.Member} {
			if e := authoritypg.SeedSynthetic(ctx, db, id, "synthetic_permission_restart_"+strings.ReplaceAll(id, "-", ""), time.Now()); e != nil {
				t.Fatal(e)
			}
		}
		query(t, db, `INSERT INTO owned_realm_quota(account_id,total,starts_at)VALUES($1,1,now())`, saved.Owner)
		name, slug := "Restart permission", "permission-restart-"+authorityUUID(t)
		v, e := r.CreateRealm(ctx, saved.Owner, social.RealmPatch{Slug: &slug, Name: &name, IsPublic: boolean(true), IsCommunity: boolean(true)})
		if e != nil {
			t.Fatal(e)
		}
		saved.RealmID = v.ID
		realmPermissionJoin(t, r, saved.Member, v.ID)
		query(t, db, `UPDATE owned_realms SET boost_level=2 WHERE id=$1`, v.ID)
		if saved.Role, e = r.SetRolePermission(ctx, saved.Owner, v.ID, realmRoleRequest(19, 15)); e != nil {
			t.Fatal(e)
		}
		realmPermissionRole(t, r, saved.Owner, v.ID, saved.Member, 19)
		deny := false
		override, e := r.SetUserPermission(ctx, saved.Owner, v.ID, social.UserPermissionRequest{AccountID: saved.Member, PermissionOverrides: social.PermissionOverrides{CanChat: &deny}})
		if e != nil || override == nil {
			t.Fatal(override, e)
		}
		saved.Override = *override
		if e = db.QueryRow(`SELECT id::text,joined_at,permission_generation FROM owned_realm_members WHERE realm_id=$1 AND account_id=$2`, saved.RealmID, saved.Member).Scan(&saved.MemberID, &saved.MemberJoinedAt, &saved.MemberGeneration); e != nil {
			t.Fatal(e)
		}
		saveRestartFixture(t, marker, saved)
	} else {
		loadRestartFixture(t, marker, &saved)
	}
	realmPermissionMask(t, r, saved.Member, saved.RealmID, 14)
	realmPermissionMask(t, r, saved.Owner, saved.RealmID, 255)
	p, e := r.UserPermissions(ctx, saved.Owner, saved.RealmID, saved.Member)
	if e != nil || p == nil || p.CanChat == nil || *p.CanChat {
		t.Fatal("override missing after restart", p, e)
	}
	raw, e := json.Marshal(p)
	if e != nil {
		t.Fatal(e)
	}
	expected, e := json.Marshal(saved.Override)
	if e != nil || string(raw) != string(expected) || strings.Contains(string(raw), "allow_mask") {
		t.Fatal("persisted override differs from pre-restart record", string(raw), string(expected), e)
	}
	roles, e := r.RolePermissions(ctx, saved.Owner, saved.RealmID)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, role := range roles {
		if role.RoleLevel != saved.Role.RoleLevel {
			continue
		}
		found = true
		if role.ID != saved.Role.ID || role.Generation != saved.Role.Generation || role.Mask() != saved.Role.Mask() || !role.CreatedAt.Equal(saved.Role.CreatedAt) || !role.UpdatedAt.Equal(saved.Role.UpdatedAt) {
			t.Fatal("persisted role differs from pre-restart record", role, saved.Role)
		}
	}
	if !found {
		t.Fatal("custom role missing after restart")
	}
	var memberID string
	var joinedAt time.Time
	var generation int64
	if e = db.QueryRow(`SELECT id::text,joined_at,permission_generation FROM owned_realm_members WHERE realm_id=$1 AND account_id=$2`, saved.RealmID, saved.Member).Scan(&memberID, &joinedAt, &generation); e != nil {
		t.Fatal(e)
	}
	if memberID != saved.MemberID || !joinedAt.Equal(saved.MemberJoinedAt) || generation != saved.MemberGeneration {
		t.Fatal("membership incarnation changed across restart", memberID, joinedAt, generation)
	}
}

func TestPGRealmPermissionMediaAdmissionAndScopedModeration(t *testing.T) {
	f := newModerationFixture(t)
	ctx := f.ctx
	query(t, f.db, `UPDATE owned_realms SET boost_level=2 WHERE id=$1`, f.private.ID)
	if _, e := f.social.SetRolePermission(ctx, f.actor, f.private.ID, realmRoleRequest(23, 31)); e != nil {
		t.Fatal(e)
	}
	if _, e := f.social.ChangeMember(ctx, f.actor, f.private.ID, social.MemberChange{Action: social.ChangeRole, AccountID: f.other, Role: 23}); e != nil {
		t.Fatal(e)
	}
	post, e := f.posts.Create(ctx, f.actor, f.individual.ID, posts.Patch{Content: postString("Custom moderator target"), RealmID: &f.private.ID})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.admin.ModerateRealmPost(f.otherCtx, f.other, post.ID, postString("Synthetic scoped reason")); e != nil {
		t.Fatal("lower numeric role's explicit moderation grant ignored", e)
	}
	second, e := f.posts.Create(ctx, f.actor, f.individual.ID, posts.Patch{Content: postString("Revoked moderator target"), RealmID: &f.private.ID})
	if e != nil {
		t.Fatal(e)
	}
	deny := false
	if _, e = f.social.SetUserPermission(ctx, f.actor, f.private.ID, social.UserPermissionRequest{AccountID: f.other, PermissionOverrides: social.PermissionOverrides{CanModeratePosts: &deny, CanUploadMedia: &deny}}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.admin.ModerateRealmPost(f.otherCtx, f.other, second.ID, nil); e == nil {
		t.Fatal("revoked moderation grant accepted")
	}
	name := "permission-media-" + strings.ReplaceAll(f.other, "-", "")
	pub, e := f.accounts.CreatePublisher(f.otherCtx, f.other, accounts.PublisherPatch{Name: &name, Nick: &name})
	if e != nil {
		t.Fatal(e)
	}
	data := "synthetic realm media"
	file, _, e := f.repo.Upload(f.otherCtx, f.other, mediaUpload("permission-media", data, false), strings.NewReader(data))
	if e != nil {
		t.Fatal(e)
	}
	ids := []string{file.ID}
	personal, e := f.posts.Create(f.otherCtx, f.other, pub.ID, posts.Patch{Content: postString("Personal attachment"), Attachments: &ids})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.posts.Update(f.otherCtx, f.other, personal.ID, posts.Patch{RealmID: &f.private.ID}); !errors.Is(e, posts.ErrForbidden) {
		t.Fatal("moving existing media into realm bypassed permission", e)
	}
	if _, e = f.posts.Create(f.otherCtx, f.other, pub.ID, posts.Patch{Content: postString("Reply attachment"), RepliedPostID: &second.ID, Attachments: &ids}); !errors.Is(e, posts.ErrForbidden) {
		t.Fatal("reply media bypassed realm permission", e)
	}
}
