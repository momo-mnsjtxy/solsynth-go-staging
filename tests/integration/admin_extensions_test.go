// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"os"
	"solsynth.local/go-migration/internal/adminpg"
	"solsynth.local/go-migration/internal/authoritypg"
	c "solsynth.local/go-migration/ports/admin/content"
	s "solsynth.local/go-migration/ports/admin/statistics"
	v "solsynth.local/go-migration/ports/admin/verification"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/social"
	a "solsynth.local/go-migration/ports/sphere/automod"
	"solsynth.local/go-migration/ports/sphere/posts"
	"strings"
	"testing"
	"time"
)

type adminExtensionsFixture struct {
	*moderationFixture
	ext *adminpg.Repository
}

func newAdminExtensionsFixture(t *testing.T) *adminExtensionsFixture {
	t.Helper()
	f := newModerationFixture(t)
	query(t, f.db, adminpg.Schema)
	r, e := adminpg.New(f.db, f.guard, f.config.Now)
	if e != nil {
		t.Fatal(e)
	}
	if e = r.Ready(f.ctx); e != nil {
		t.Fatal(e)
	}
	return &adminExtensionsFixture{f, r}
}
func syntheticMark() *accounts.VerificationMark {
	return &accounts.VerificationMark{Type: 0, Title: postString("Synthetic verification fixture"), Description: postString("Only a disposable test account"), VerifiedBy: postString("Synthetic fixture issuer")}
}
func rulePatch(name string) a.Patch {
	kind, action := a.KeywordBlocklist, a.Flag
	return a.Patch{Name: &name, Type: &kind, DefaultAction: &action, Pattern: postString("synthetic marker")}
}
func TestPGAdminVerificationLifecycleAndPrivacy(t *testing.T) {
	f := newAdminExtensionsFixture(t)
	mark := syntheticMark()
	if _, e := f.social.ChangeMember(f.ctx, f.actor, f.private.ID, social.MemberChange{Action: social.Invite, AccountID: f.outside}); e != nil {
		t.Fatal(e)
	}
	joined, total, e := f.admin.Members(f.outsideCtx, f.outside, f.private.ID, c.Query{Take: 20})
	if e != nil || total != 2 || len(joined) != 2 {
		t.Fatal("default joined member filter", joined, total, e)
	}
	for _, member := range joined {
		if member.JoinedAt == nil {
			t.Fatal("pending invitation included as joined")
		}
	}
	pending := true
	invites, total, e := f.admin.Members(f.outsideCtx, f.outside, f.private.ID, c.Query{Take: 20, Pending: &pending})
	if e != nil || total != 1 || len(invites) != 1 || invites[0].AccountID != f.outside || invites[0].JoinedAt != nil {
		t.Fatal("pending-only member filter", invites, total, e)
	}
	for _, target := range []struct {
		kind v.Kind
		ref  string
	}{{v.Account, f.actor}, {v.Publisher, f.privatePub.ID}, {v.Realm, f.private.ID}} {
		if _, e := f.ext.SetVerification(f.outsideCtx, f.outside, target.kind, target.ref, mark); e != nil {
			t.Fatal(target, e)
		}
	}
	account, e := f.accounts.LookupAccount(f.ctx, f.actor)
	if e != nil || account.Profile == nil || account.Profile.Verification == nil || *account.Profile.Verification.Title != *mark.Title {
		t.Fatal(account, e)
	}
	verified := true
	if _, total, e := f.admin.List(f.outsideCtx, f.outside, c.PublisherKind, c.Query{Text: f.privatePub.Name, Verified: &verified, Take: 20}); e != nil || total != 1 {
		t.Fatal("verified publisher filter", total, e)
	}
	pub, e := f.accounts.LookupPublisherForViewer(f.ctx, f.actor, f.privatePub.ID)
	if e != nil || pub.Verification == nil {
		t.Fatal(pub, e)
	}
	realm, e := f.social.GetRealm(f.ctx, f.actor, f.private.ID)
	if e != nil || realm.Verification == nil {
		t.Fatal(realm, e)
	}
	if _, e = f.accounts.LookupPublisherForViewer(f.outsideCtx, f.outside, f.privatePub.ID); !errors.Is(e, accounts.ErrNotFound) {
		t.Fatal("mark bypassed private publisher gate", e)
	}
	if _, e = f.social.GetRealm(f.outsideCtx, f.outside, f.private.ID); !errors.Is(e, social.ErrNotFound) {
		t.Fatal("mark bypassed realm gate", e)
	}
	for _, target := range []struct {
		kind v.Kind
		ref  string
	}{{v.Account, f.actor}, {v.Publisher, f.privatePub.ID}, {v.Realm, f.private.ID}} {
		if _, e = f.ext.SetVerification(f.outsideCtx, f.outside, target.kind, target.ref, nil); e != nil {
			t.Fatal(target, e)
		}
	}
	account, e = f.accounts.LookupAccount(f.ctx, f.actor)
	if e != nil || account.Profile.Verification != nil {
		t.Fatal(account, e)
	}
	pub, e = f.accounts.LookupPublisherForViewer(f.ctx, f.actor, f.privatePub.ID)
	if e != nil || pub.Verification != nil {
		t.Fatal(pub, e)
	}
}
func TestPGAdminVerificationAuthorityAndRollback(t *testing.T) {
	f := newAdminExtensionsFixture(t)
	if _, e := f.ext.SetVerification(f.ctx, f.actor, v.Account, f.actor, syntheticMark()); !errors.Is(e, c.ErrForbidden) {
		t.Fatal("self-verification granted", e)
	}
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.outside, []authority.Rule{{Pattern: "*"}, {Pattern: "publishers.moderate", Deny: true}}, nil); e != nil {
		t.Fatal(e)
	}
	if _, e := f.ext.SetVerification(f.outsideCtx, f.outside, v.Publisher, f.individual.ID, syntheticMark()); e == nil {
		t.Fatal("named denial bypassed")
	}
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.outside, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `ALTER TABLE owned_admin_extension_audit ADD CONSTRAINT synthetic_verification_rollback CHECK(action<>'verification.set') NOT VALID`)
	t.Cleanup(func() {
		query(t, f.db, `ALTER TABLE owned_admin_extension_audit DROP CONSTRAINT IF EXISTS synthetic_verification_rollback`)
	})
	_, e := f.ext.SetVerification(f.outsideCtx, f.outside, v.Publisher, f.individual.ID, syntheticMark())
	var pg *pgconn.PgError
	if !errors.As(e, &pg) || pg.Code != "23514" || pg.ConstraintName != "synthetic_verification_rollback" {
		t.Fatal(e)
	}
	var empty bool
	if e = f.db.QueryRow(`SELECT verification IS NULL FROM owned_publishers WHERE id=$1`, f.individual.ID).Scan(&empty); e != nil || !empty {
		t.Fatal("verification survived audit rollback", empty, e)
	}
}
func TestPGAdminVerificationCurrentTargetAfterWait(t *testing.T) {
	f := newAdminExtensionsFixture(t)
	ctx, cancel := context.WithTimeout(f.outsideCtx, 10*time.Second)
	defer cancel()
	tx, e := f.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	var pid int
	if e = tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, f.other); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := f.ext.SetVerification(ctx, f.outside, v.Account, f.other, syntheticMark()); done <- e }()
	moderationWait(t, ctx, f.db, pid, "owned_authority_accounts")
	if _, e = tx.ExecContext(ctx, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.other); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, c.ErrConflict) {
		t.Fatal("disabled target verified after wait", e)
	}
	var count int
	if e = f.db.QueryRow(`SELECT count(*)FROM owned_accounts_profiles WHERE account_id=$1 AND verification IS NOT NULL`, f.other).Scan(&count); e != nil || count != 0 {
		t.Fatal(count, e)
	}
}
func TestPGAdminStatisticsCanonicalCounts(t *testing.T) {
	f := newAdminExtensionsFixture(t)
	now := f.config.Now()
	before, e := f.ext.Statistics(f.outsideCtx, f.outside, s.Passport)
	if e != nil {
		t.Fatal(e)
	}
	b := before.(s.AccountStats)
	for i, age := range []time.Duration{0, 24 * time.Hour, 7 * 24 * time.Hour, 30 * 24 * time.Hour, 31 * 24 * time.Hour} {
		id := authorityUUID(t)
		if e = authoritypg.SeedSynthetic(f.ctx, f.db, id, "synthetic_admin_stats_"+strings.ReplaceAll(id, "-", ""), now.Add(-age)); e != nil {
			t.Fatal(e)
		}
		query(t, f.db, `INSERT INTO owned_accounts_profiles(account_id,id,updated_at)VALUES($1,$2,$3)`, id, authorityUUID(t), now)
		query(t, f.db, `INSERT INTO owned_presence_last_seen(account_id,last_seen_at)VALUES($1,$2)`, id, now.Add(-age))
		if i == 4 {
			query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, id)
		}
	}
	after, e := f.ext.Statistics(f.outsideCtx, f.outside, s.Passport)
	if e != nil {
		t.Fatal(e)
	}
	got := after.(s.AccountStats)
	if got.TotalProfiledAccounts-b.TotalProfiledAccounts != 4 || got.ActiveUsersLastDay-b.ActiveUsersLastDay != 2 || got.ActiveUsersLastWeek-b.ActiveUsersLastWeek != 3 || got.ActiveUsersLastMonth-b.ActiveUsersLastMonth != 4 || got.RegisteredUsersLastDay-b.RegisteredUsersLastDay != 2 || !got.CalculatedAt.Equal(now) {
		t.Fatal(b, got)
	}
	before, e = f.ext.Statistics(f.outsideCtx, f.outside, s.Sphere)
	if e != nil {
		t.Fatal(e)
	}
	pb := before.(s.PostStats)
	post := f.post(t, f.individual, "Synthetic admin stats post", posts.Private)
	query(t, f.db, `UPDATE owned_text_posts SET created_at=$2,published_at=$2 WHERE id=$1`, post.ID, now)
	after, e = f.ext.Statistics(f.outsideCtx, f.outside, s.Sphere)
	if e != nil {
		t.Fatal(e)
	}
	pgot := after.(s.PostStats)
	if pgot.TotalPosts != pb.TotalPosts+1 || pgot.PostsLastDay != pb.PostsLastDay+1 || pgot.PublishedPosts != pb.PublishedPosts+1 {
		t.Fatal(pb, pgot)
	}

	walletBefore, e := f.ext.Statistics(f.outsideCtx, f.outside, s.Wallet)
	if e != nil {
		t.Fatal(e)
	}
	wb := walletBefore.(s.WalletStats)
	// Disposable ledger rows exercise aggregates; no payment command/provider runs.
	payer, payee := authorityUUID(t), authorityUUID(t)
	query(t, f.db, `INSERT INTO wallet_v1.wallets(id,account_id,name,created_at,updated_at)VALUES($1,$2,'Synthetic admin stats wallet',$3,$3),($4,$5,'Synthetic admin stats wallet',$3,$3)`, payer, f.actor, now, payee, f.other)

	// Aggregate-only imported rows intentionally do not create ledger holds. Keep
	// them confined to this fixture, including on assertion failure, so unrelated
	// lifecycle workers never observe an unbacked pending transaction.
	t.Cleanup(func() {
		if _, err := f.db.Exec(`DELETE FROM wallet_v1.transactions WHERE payer_wallet_id=$1::uuid AND payee_wallet_id=$2::uuid AND currency='synthetic-admin-stats'`, payer, payee); err != nil {
			t.Error("aggregate fixture cleanup", err)
		}
	})
	for i, status := range []int{0, 2, 4} {
		created := now.Add(-time.Duration(i) * 8 * 24 * time.Hour)
		query(t, f.db, `INSERT INTO wallet_v1.transactions(id,actor_id,payer_wallet_id,payee_wallet_id,currency,amount,status,is_frozen,require_confirmation,expires_at,created_at,updated_at,fingerprint)VALUES($1,$2,$3,$4,'synthetic-admin-stats',1,$5,false,false,$6,$7,$7,$8)`, authorityUUID(t), f.actor, payer, payee, status, now.Add(time.Hour), created, strings.Repeat("0", 64))
	}
	walletAfter, e := f.ext.Statistics(f.outsideCtx, f.outside, s.Wallet)
	if e != nil {
		t.Fatal(e)
	}
	wg := walletAfter.(s.WalletStats)
	if wg.TotalWallets-wb.TotalWallets != 2 || wg.TotalTransactions-wb.TotalTransactions != 3 || wg.PendingTransactions-wb.PendingTransactions != 1 || wg.ConfirmedTransactions-wb.ConfirmedTransactions != 1 || wg.TransactionsLastDay-wb.TransactionsLastDay != 1 || wg.TransactionsLastWeek-wb.TransactionsLastWeek != 1 || wg.TransactionsLastMonth-wb.TransactionsLastMonth != 3 {
		t.Fatal(wb, wg)
	}
}
func TestPGAdminStatisticsPermissionAndCurrentProof(t *testing.T) {
	f := newAdminExtensionsFixture(t)
	for _, service := range []s.Service{s.Passport, s.Sphere, s.Wallet} {
		if _, e := f.ext.Statistics(f.ctx, f.actor, service); !errors.Is(e, c.ErrForbidden) {
			t.Fatal("local grant gained aggregate admin", service, e)
		}
	}
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.outside, []authority.Rule{{Pattern: "*"}, {Pattern: "accounts.view", Deny: true}}, nil); e != nil {
		t.Fatal(e)
	}
	if _, e := f.ext.Statistics(f.outsideCtx, f.outside, s.Passport); e == nil {
		t.Fatal("stats permission denial bypass")
	}
	query(t, f.db, `UPDATE owned_authority_sessions SET revoked_at=$2 WHERE account_id=$1`, f.outside, f.config.Now())
	if _, e := f.ext.Statistics(f.outsideCtx, f.outside, s.Sphere); e == nil {
		t.Fatal("revoked proof accepted")
	}
}
func TestPGAutomodRulesLifecycleAndExplicitEvaluation(t *testing.T) {
	f := newAdminExtensionsFixture(t)
	p := rulePatch("Synthetic rule " + f.actor)
	rule, e := f.ext.CreateRule(f.outsideCtx, f.outside, p)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.ext.CreateRule(f.ctx, f.actor, rulePatch("Synthetic denied "+f.actor)); !errors.Is(e, c.ErrForbidden) {
		t.Fatal("non-global rule management", e)
	}
	matches, e := f.ext.TestRule(f.outsideCtx, f.outside, rule.ID, "SYNTHETIC MARKER")
	if e != nil || len(matches) != 1 || matches[0].Action != a.Flag {
		t.Fatal(matches, e)
	}
	disabled := false
	rule, e = f.ext.UpdateRule(f.outsideCtx, f.outside, rule.ID, a.Patch{IsEnabled: &disabled})
	if e != nil || rule.IsEnabled {
		t.Fatal(rule, e)
	}
	enabled := true
	rules, e := f.ext.Rules(f.outsideCtx, f.outside, &enabled)
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range rules {
		if v.ID == rule.ID {
			t.Fatal("disabled rule listed as enabled")
		}
	}
	if matches, e = f.ext.TestRule(f.outsideCtx, f.outside, rule.ID, "synthetic marker"); e != nil || len(matches) != 1 {
		t.Fatal("explicit test stopped evaluating disabled rule", matches, e)
	}
	post := f.post(t, f.individual, "synthetic marker remains a normal post", posts.Public)
	if _, e = f.posts.Get(f.otherCtx, f.other, post.ID); e != nil {
		t.Fatal("CRUD implied unconfigured enforcement", e)
	}
	if e = f.ext.DeleteRule(f.outsideCtx, f.outside, rule.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.ext.Rule(f.outsideCtx, f.outside, rule.ID); !errors.Is(e, a.ErrNotFound) {
		t.Fatal(e)
	}
}
func TestPGAutomodBulkAtomicityAndConcurrency(t *testing.T) {
	f := newAdminExtensionsFixture(t)
	name := "Synthetic bulk " + f.actor
	first, e := f.ext.CreateRule(f.outsideCtx, f.outside, rulePatch(name))
	if e != nil {
		t.Fatal(e)
	}
	var before int64
	if e = f.db.QueryRow(`SELECT generation FROM owned_automod_state WHERE singleton`).Scan(&before); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `ALTER TABLE owned_admin_extension_audit ADD CONSTRAINT synthetic_automod_rollback CHECK(action<>'automod.create') NOT VALID`)
	t.Cleanup(func() {
		query(t, f.db, `ALTER TABLE owned_admin_extension_audit DROP CONSTRAINT IF EXISTS synthetic_automod_rollback`)
	})
	one := rulePatch(name)
	one.Pattern = postString("synthetic replacement")
	_, e = f.ext.BulkRules(f.outsideCtx, f.outside, []a.Patch{one, rulePatch(name + " second")})
	var pg *pgconn.PgError
	if !errors.As(e, &pg) || pg.Code != "23514" || pg.ConstraintName != "synthetic_automod_rollback" {
		t.Fatal(e)
	}
	unchanged, e := f.ext.Rule(f.outsideCtx, f.outside, first.ID)
	if e != nil || unchanged.Pattern != first.Pattern {
		t.Fatal("partial bulk", unchanged, e)
	}
	var generation int64
	if e = f.db.QueryRow(`SELECT generation FROM owned_automod_state WHERE singleton`).Scan(&generation); e != nil || generation != before {
		t.Fatal("rollback generation", generation, before, e)
	}
	query(t, f.db, `ALTER TABLE owned_admin_extension_audit DROP CONSTRAINT synthetic_automod_rollback`)
	query(t, f.db, `UPDATE owned_authority_accounts SET is_superuser=true WHERE id=$1`, f.actor)
	ctx, cancel := context.WithTimeout(f.outsideCtx, 10*time.Second)
	defer cancel()
	ctx2, cancel2 := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel2()
	done := make(chan error, 2)
	for i, principal := range []struct {
		ctx   context.Context
		actor string
	}{{ctx, f.outside}, {ctx2, f.actor}} {
		patch := rulePatch(name + " concurrent")
		weight := i + 20
		patch.DerankWeight = &weight
		go func(p a.Patch, ctx context.Context, actor string) {
			_, e := f.ext.BulkRules(ctx, actor, []a.Patch{p})
			done <- e
		}(patch, principal.ctx, principal.actor)
	}
	for i := 0; i < 2; i++ {
		if e = <-done; e != nil {
			t.Fatal(e)
		}
	}
	var count int
	if e = f.db.QueryRow(`SELECT count(*)FROM owned_automod_rules WHERE lower(name)=lower($1)AND deleted_at IS NULL`, name+" concurrent").Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
}
func TestPGAutomodRuleMutationRevocation(t *testing.T) {
	f := newAdminExtensionsFixture(t)
	rule, e := f.ext.CreateRule(f.outsideCtx, f.outside, rulePatch("Synthetic revoke "+f.actor))
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(f.outsideCtx, 10*time.Second)
	defer cancel()
	tx, e := f.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	var pid int
	if e = tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, f.outside); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, e := f.ext.UpdateRule(ctx, f.outside, rule.ID, a.Patch{Pattern: postString("synthetic revoked update")})
		done <- e
	}()
	moderationWait(t, ctx, f.db, pid, "owned_authority_accounts")
	if _, e = tx.ExecContext(ctx, `UPDATE owned_authority_accounts SET is_superuser=false WHERE id=$1`, f.outside); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, c.ErrForbidden) {
		t.Fatal("revoked global rule authority accepted", e)
	}
	var pattern string
	if e = f.db.QueryRow(`SELECT pattern FROM owned_automod_rules WHERE id=$1`, rule.ID).Scan(&pattern); e != nil || pattern != rule.Pattern {
		t.Fatal(pattern, e)
	}
}
func TestPGAdminVerificationNestedPublisherProjection(t *testing.T) {
	f := newAdminExtensionsFixture(t)
	post := f.post(t, f.privatePub, "Synthetic verified nested publisher", posts.Public)
	if _, e := f.ext.SetVerification(f.outsideCtx, f.outside, v.Publisher, f.privatePub.ID, syntheticMark()); e != nil {
		t.Fatal(e)
	}
	got, e := f.posts.Get(f.otherCtx, f.other, post.ID)
	if e != nil || got.Publisher.Verification == nil {
		t.Fatal(got, e)
	}
	reply, e := f.posts.Create(f.ctx, f.actor, f.individual.ID, posts.Patch{Content: postString("Synthetic visible relation"), RepliedPostID: &post.ID})
	if e != nil {
		t.Fatal(e)
	}
	got, e = f.posts.Get(f.outsideCtx, f.outside, reply.ID)
	if e != nil || got.RepliedPost != nil || got.RepliedPostID != nil {
		t.Fatal("verified mark exposed private relation", got, e)
	}
	if _, e = f.ext.SetVerification(f.outsideCtx, f.outside, v.Publisher, f.individual.ID, syntheticMark()); e != nil {
		t.Fatal(e)
	}
	got, e = f.posts.Get(f.otherCtx, f.other, reply.ID)
	if e != nil || got.Publisher.Verification == nil {
		t.Fatal(got, e)
	}
	f.mutate(t, c.PublisherKind, f.individual.ID, c.Command{Action: "shadowban", Reason: postString("other")})
	if _, e = f.posts.Get(f.otherCtx, f.other, reply.ID); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("verification overrode moderation", e)
	}
	if _, e = f.ext.SetVerification(f.outsideCtx, f.outside, v.Publisher, f.individual.ID, nil); e != nil {
		t.Fatal(e)
	}
	f.mutate(t, c.PublisherKind, f.individual.ID, c.Command{Action: "unshadowban"})
	got, e = f.posts.Get(f.otherCtx, f.other, reply.ID)
	if e != nil || got.Publisher.Verification != nil {
		t.Fatal("stale verification after clear", got, e)
	}
}
func TestPGAdminExtensionsRestartPersistence(t *testing.T) {
	const title = "Synthetic durable admin extensions checkpoint"
	const ruleName = "Synthetic durable admin extensions rule"
	if os.Getenv("PG_TEST_PHASE") == "after" {
		db := database(t)
		var account, admin string
		if e := db.QueryRow(`SELECT p.account_id::text,a.actor_id::text FROM owned_accounts_profiles p JOIN owned_admin_extension_audit a ON a.target_id=p.account_id AND a.action='verification.set' WHERE p.verification->>'title'=$1 ORDER BY a.id DESC LIMIT 1`, title).Scan(&account, &admin); e != nil {
			t.Fatal(e)
		}
		_, verifier, guard, token := interactionAuthority(t, db, admin)
		proof, e := verifier.Authenticate(context.Background(), token)
		if e != nil {
			t.Fatal(e)
		}
		ctx := guard.Bind(context.Background(), proof)
		r, e := adminpg.New(db, guard, time.Now)
		if e != nil {
			t.Fatal(e)
		}
		var id string
		if e = db.QueryRow(`SELECT id::text FROM owned_automod_rules WHERE name=$1 AND deleted_at IS NULL`, ruleName).Scan(&id); e != nil {
			t.Fatal(e)
		}
		matches, e := r.TestRule(ctx, admin, id, "synthetic marker")
		if e != nil || len(matches) != 1 {
			t.Fatal(matches, e)
		}
		var raw []byte
		if e = db.QueryRow(`SELECT verification FROM owned_accounts_profiles WHERE account_id=$1`, account).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		var mark accounts.VerificationMark
		if e = json.Unmarshal(raw, &mark); e != nil || mark.Title == nil || *mark.Title != title {
			t.Fatal(mark, e)
		}
		return
	}
	f := newAdminExtensionsFixture(t)
	mark := syntheticMark()
	mark.Title = postString(title)
	if _, e := f.ext.SetVerification(f.outsideCtx, f.outside, v.Account, f.actor, mark); e != nil {
		t.Fatal(e)
	}
	if _, e := f.ext.CreateRule(f.outsideCtx, f.outside, rulePatch(ruleName)); e != nil {
		t.Fatal(e)
	}
}
