// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"solsynth.local/go-migration/internal/accounthistorypg"
	"solsynth.local/go-migration/internal/accountsettingspg"
	"solsynth.local/go-migration/internal/activitypg"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/auditcapture"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/connectionpg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/notificationpg"
	"solsynth.local/go-migration/internal/progressionpg"
	"solsynth.local/go-migration/internal/socialpg"
	a "solsynth.local/go-migration/ports/admin/accounts"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/signin"
	"solsynth.local/go-migration/ports/notifications"
	"solsynth.local/go-migration/ports/passport/activities"
	links "solsynth.local/go-migration/ports/passport/connections"
	"solsynth.local/go-migration/ports/passport/history"
	"solsynth.local/go-migration/ports/passport/progression"
	"solsynth.local/go-migration/ports/passport/social"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	commerce "solsynth.local/go-migration/ports/wallet/commerce"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type pgVerifiedProvider struct {
	identifier string
	calls      *atomic.Int64
	before     func()
}

func (p pgVerifiedProvider) Name() string { return "steam" }
func (p pgVerifiedProvider) Start(state, challenge string) (string, error) {
	return "https://provider.example/authorize?state=" + state, nil
}
func (p pgVerifiedProvider) Verify(ctx context.Context, v links.Completion, verifier string) (links.Verified, error) {
	p.calls.Add(1)
	if p.before != nil {
		p.before()
	}
	return links.Verified{Identifier: p.identifier, DisplayName: "Synthetic verified user"}, nil
}
func pgConnections(t *testing.T, f presenceProgressionFixture, provider links.Provider) *connectionpg.Repository {
	t.Helper()
	query(t, f.db, connectionpg.Schema)
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "account.connections.manage"}, {Pattern: activities.ManagePermission}, {Pattern: "progression.*"}}, nil); e != nil {
		t.Fatal(e)
	}
	r, e := connectionpg.New(f.db, f.guard, connectionpg.Config{Providers: []links.Provider{provider}, EncryptionKey: make([]byte, 32)})
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func TestPGVerifiedProviderLinkProofReplayAndCurrentAuthority(t *testing.T) {
	f := newPresenceProgressionFixture(t)
	calls := &atomic.Int64{}
	p := pgVerifiedProvider{identifier: "76561198000000001", calls: calls}
	r := pgConnections(t, f, p)
	state, e := r.Start(f.ctx, f.actor, "steam")
	if e != nil {
		t.Fatal(e)
	}
	row, e := r.Complete(f.ctx, f.actor, "steam", links.Completion{State: state.State, Code: "synthetic-code"})
	if e != nil || row.IsPublic || row.ProvidedIdentifier != p.identifier {
		t.Fatal(row, e)
	}
	if _, e = r.Complete(f.ctx, f.actor, "steam", links.Completion{State: state.State, Code: "replay"}); !errors.Is(e, links.ErrProof) || calls.Load() != 1 {
		t.Fatal("replayed provider proof", e, calls.Load())
	}
	if _, e = r.SetPublic(f.ctx, f.actor, row.ID, true); e != nil {
		t.Fatal(e)
	}
	if e = r.Delete(f.ctx, f.actor, row.ID); e != nil {
		t.Fatal(e)
	}
	// Revoke the originating opaque credential while the provider call is running.
	p.before = func() {
		query(t, f.db, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp() WHERE id=$1`, f.principal.Session.ID)
	}
	r = pgConnections(t, f, p)
	state, e = r.Start(f.ctx, f.actor, "steam")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = r.Complete(f.ctx, f.actor, "steam", links.Completion{State: state.State, Code: "after-revoke"}); e == nil {
		t.Fatal("late provider callback resurrected authority")
	}
	var live int
	if e = f.db.QueryRow(`SELECT count(*) FROM owned_account_connections WHERE account_id=$1 AND revoked_at IS NULL`, f.actor).Scan(&live); e != nil || live != 0 {
		t.Fatal(live, e)
	}
}
func TestPGAccountAdminAggregateAndSecurityMutations(t *testing.T) {
	f := newAccountAdminFixture(t)
	list, total, e := f.repo.ListAccounts(f.ctx, f.actor, a.Filter{Query: f.other, Take: 20, OrderBy: "name_asc"})
	if e != nil {
		t.Fatal(e)
	}
	// UUID is an explicit reference, whereas the list query searches names/nicks.
	if total != 0 || len(list) != 0 {
		t.Fatal(list, total)
	}
	detail, e := f.repo.AccountDetail(f.ctx, f.actor, f.other)
	if e != nil || detail.Account.ID != f.other {
		t.Fatal(detail, e)
	}
	raw, _ := json.Marshal(detail)
	if strings.Contains(string(raw), "synthetic-secret") || strings.Contains(string(raw), "refresh_hash") {
		t.Fatal("security secret exposed")
	}
	email := "synthetic-contact-" + f.other + "@example.invalid"
	v, e := f.repo.MutateAccount(f.ctx, f.actor, f.other, a.Mutation{Operation: "contact.create", Contact: &signin.ContactInput{Type: 0, Content: email}})
	if e != nil {
		t.Fatal(e)
	}
	contact, ok := v.(signin.Contact)
	if !ok || contact.VerifiedAt != nil || contact.IsPublic {
		t.Fatal(v)
	}
	if _, e = f.repo.MutateAccount(f.ctx, f.actor, f.other, a.Mutation{Operation: "contact.visibility", ResourceID: contact.ID, Public: boolean(true)}); e == nil {
		t.Fatal("unverified contact made public")
	}
	password := "synthetic-new-password-12345"
	if _, e = f.repo.MutateAccount(f.ctx, f.actor, f.other, a.Mutation{Operation: "password.reset", Password: &password}); e != nil {
		t.Fatal(e)
	}
	verifier, e := authority.NewVerifier(authoritypg.New(f.db), f.config)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = verifier.Authenticate(context.Background(), f.otherToken); e == nil {
		t.Fatal("password reset did not revoke prior account-version proof")
	}
	record := authorityUUID(t)
	credit := a.CreditInput{Record: record, ReasonType: "owned.admin", Reason: "Synthetic authorized credit", Delta: 5}
	if _, e = f.repo.MutateAccount(f.ctx, f.actor, f.other, a.Mutation{Operation: "credit.grant", Credit: &credit}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.MutateAccount(f.ctx, f.actor, f.other, a.Mutation{Operation: "credit.grant", Credit: &credit}); e != nil {
		t.Fatal("credit replay", e)
	}
	credit.Delta = 500
	if _, e = f.repo.MutateAccount(f.ctx, f.actor, f.other, a.Mutation{Operation: "credit.grant", Credit: &credit}); e == nil {
		t.Fatal("credit provenance mismatch accepted")
	}
	var n int
	if e = f.db.QueryRow(`SELECT count(*) FROM owned_account_credit_records WHERE account_id=$1 AND source_record=$2`, f.other, record).Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
}

type pgProviderObserver struct {
	calls  *atomic.Int64
	before func()
}

func (p pgProviderObserver) Observe(ctx context.Context, subject string) (activities.Content, bool, error) {
	p.calls.Add(1)
	if p.before != nil {
		p.before()
	}
	kind, title := "gaming", "Synthetic verified game"
	return activities.Content{Type: &kind, Title: &title}, true, nil
}
func TestPGProviderPresenceConsentWorkerExpiryRevocationAndClaims(t *testing.T) {
	f := newPresenceProgressionFixture(t)
	connectionCalls := &atomic.Int64{}
	linksRepo := pgConnections(t, f, pgVerifiedProvider{identifier: "76561198000000002", calls: connectionCalls})
	state, e := linksRepo.Start(f.ctx, f.actor, "steam")
	if e != nil {
		t.Fatal(e)
	}
	link, e := linksRepo.Complete(f.ctx, f.actor, "steam", links.Completion{State: state.State, Code: "verified"})
	if e != nil {
		t.Fatal(e)
	}
	calls := &atomic.Int64{}
	r := activitypg.New(f.db, f.guard).WithSteamProvider(pgProviderObserver{calls: calls})
	visible := activities.Private
	req := activities.ProviderRequest{Provider: "steam", ConnectionID: link.ID, Visibility: &visible, LeaseMinutes: 5, ConsentMinutes: 1}
	act, e := r.ObserveProvider(f.ctx, f.actor, req)
	if e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE owned_provider_presence_consents SET next_at=clock_timestamp()-interval '1 second' WHERE connection_id=$1`, link.ID)
	// Two independently constructed workers share a claim/rate row, producing once.
	r2 := activitypg.New(f.db, f.guard).WithSteamProvider(pgProviderObserver{calls: calls})
	var wg sync.WaitGroup
	var applied atomic.Int64
	errs := make(chan error, 2)
	for _, repo := range []*activitypg.Repository{r, r2} {
		wg.Add(1)
		go func(repo *activitypg.Repository) {
			defer wg.Done()
			n, e := repo.ProcessProviders(f.ctx, 1)
			applied.Add(int64(n))
			errs <- e
		}(repo)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if applied.Load() != 1 || calls.Load() != 2 {
		t.Fatal("overlapping provider observations", applied.Load(), calls.Load())
	}
	var expiry, consentExpiry time.Time
	if e = f.db.QueryRow(`SELECT a.lease_expires_at,c.expires_at FROM owned_presence_activities a JOIN owned_provider_presence_consents c ON c.activity_id=a.id WHERE a.id=$1`, act.ID).Scan(&expiry, &consentExpiry); e != nil || expiry.After(consentExpiry) {
		t.Fatal("lease outlived consent", expiry, consentExpiry, e)
	}
	query(t, f.db, `UPDATE owned_provider_presence_rate SET next_at=clock_timestamp()-interval '1 second' WHERE provider='steam'`)
	query(t, f.db, `UPDATE owned_provider_presence_consents SET next_at=clock_timestamp()-interval '1 second' WHERE connection_id=$1`, link.ID)
	r = activitypg.New(f.db, f.guard).WithSteamProvider(pgProviderObserver{calls: calls, before: func() {
		query(t, f.db, `UPDATE owned_account_connections SET revoked_at=clock_timestamp() WHERE id=$1`, link.ID)
	}})
	if n, e := r.ProcessProviders(f.ctx, 1); e != nil || n != 0 {
		t.Fatal("revoked link produced presence", n, e)
	}
	var revision int
	if e = f.db.QueryRow(`SELECT revision FROM owned_presence_activities WHERE id=$1`, act.ID).Scan(&revision); e != nil || revision != 3 {
		t.Fatal("revoked callback mutated admitted activity", revision, e)
	}
	query(t, f.db, `UPDATE owned_provider_presence_consents SET expires_at=created_at+interval '1 microsecond',revoked_at=NULL,next_at=clock_timestamp()-interval '1 second' WHERE connection_id=$1`, link.ID)
	before := calls.Load()
	if n, e := r.ProcessProviders(f.ctx, 1); e != nil || n != 0 || calls.Load() != before {
		t.Fatal("expired consent polled", n, e, calls.Load())
	}
}

func pgSourceGrant(t *testing.T, f presenceProgressionFixture, points int64) string {
	t.Helper()
	grant := authorityUUID(t)
	query(t, f.db, `INSERT INTO owned_progression_grants(id,account_id,kind,identifier,title,reward_token,source_event_id,period_key,reward,created_at,updated_at)VALUES($1,$2,'achievement',$3,'Synthetic earned grant',$3,$3,'',$4::jsonb,clock_timestamp(),clock_timestamp())`, grant, f.actor, "synthetic-grant-"+grant, []byte(`{"source_points":`+jsonInt(points)+`}`))
	return grant
}
func jsonInt(v int64) string { b, _ := json.Marshal(v); return string(b) }
func pgSourceWallet(t *testing.T, f presenceProgressionFixture, actor string, amount string) string {
	t.Helper()
	id := authorityUUID(t)
	query(t, f.db, `INSERT INTO wallet_v1.wallets(id,account_id,name,is_primary,created_at,updated_at)VALUES($1,$2,'Synthetic progression wallet',true,clock_timestamp(),clock_timestamp())`, id, actor)
	query(t, f.db, `INSERT INTO wallet_v1.pockets(id,wallet_id,currency,amount,created_at,updated_at)VALUES($1,$2,'synthetic-earned-points',$3,clock_timestamp(),clock_timestamp())`, authorityUUID(t), id, amount)
	return id
}
func pgPaySource(ctx context.Context, db *sql.DB, p *progressionpg.FundedSourcePoints, actor, grant string, points int64, commit bool) error {
	tx, e := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var id string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, actor).Scan(&id); e != nil {
		return e
	}
	var now time.Time
	if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
		return e
	}
	if e = p.PaySourcePointsInTransaction(ctx, tx, actor, grant, nil, points, now); e != nil {
		return e
	}
	if commit {
		return tx.Commit()
	}
	return tx.Rollback()
}
func pgPointBalance(t *testing.T, db *sql.DB, id string) wallet.Amount {
	t.Helper()
	var amount wallet.Amount
	if e := db.QueryRow(`SELECT amount::text FROM wallet_v1.pockets WHERE wallet_id=$1 AND currency='synthetic-earned-points'`, id).Scan(&amount); e != nil {
		t.Fatal(e)
	}
	return amount
}
func TestPGProgressionFundedSourcePointsConservationReplayAndRollback(t *testing.T) {
	f := newPresenceProgressionFixture(t)
	treasury := ppOther(t, f)
	payer := pgSourceWallet(t, f, treasury, "100")
	payee := pgSourceWallet(t, f, f.actor, "0")
	grant := pgSourceGrant(t, f, 7)
	p, e := progressionpg.NewFundedSourcePoints(progressionpg.SourcePointConfig{TreasuryAccountID: treasury, TreasuryWalletID: payer, Currency: "synthetic-earned-points", MaxPerGrant: 10, MaxPerDay: 100})
	if e != nil {
		t.Fatal(e)
	}
	if e = pgPaySource(f.ctx, f.db, p, f.actor, grant, 7, false); e != nil {
		t.Fatal(e)
	}
	if pgPointBalance(t, f.db, payer).String() != "100.000" || pgPointBalance(t, f.db, payee).Sign() != 0 {
		t.Fatal("rollback leaked earned money")
	}
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- pgPaySource(f.ctx, f.db, p, f.actor, grant, 7, true) }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if pgPointBalance(t, f.db, payer).String() != "93.000" || pgPointBalance(t, f.db, payee).String() != "7.000" {
		t.Fatal("money minted or duplicate grant")
	}
	if e = pgPaySource(f.ctx, f.db, p, f.actor, grant, 8, true); e == nil {
		t.Fatal("caller forged source-point reward")
	}
	grant = pgSourceGrant(t, f, 9)
	query(t, f.db, `UPDATE wallet_v1.pockets SET amount=0 WHERE wallet_id=$1`, payer)
	if e = pgPaySource(f.ctx, f.db, p, f.actor, grant, 9, true); e == nil {
		t.Fatal("unfunded reward succeeded")
	}
	if pgPointBalance(t, f.db, payee).String() != "7.000" {
		t.Fatal("failed funding credited recipient")
	}
	query(t, f.db, `UPDATE wallet_v1.pockets SET amount=100 WHERE wallet_id=$1`, payer)
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, treasury)
	if e = pgPaySource(f.ctx, f.db, p, f.actor, grant, 9, true); e == nil {
		t.Fatal("disabled treasury funded reward")
	}
}
func TestPGProgressionSourcePointsWaitRechecksScheduledEligibility(t *testing.T) {
	f := newPresenceProgressionFixture(t)
	treasury := ppOther(t, f)
	payer := pgSourceWallet(t, f, treasury, "100")
	payee := pgSourceWallet(t, f, f.actor, "0")
	grant := pgSourceGrant(t, f, 7)
	p, e := progressionpg.NewFundedSourcePoints(progressionpg.SourcePointConfig{TreasuryAccountID: treasury, TreasuryWalletID: payer, Currency: "synthetic-earned-points", MaxPerGrant: 10, MaxPerDay: 100})
	if e != nil {
		t.Fatal(e)
	}
	af := atomicAuthorityFixture{db: f.db, ctx: f.ctx}
	blocker, pid := atomicBlocker(t, af, "wallet_v1.wallets", payee)
	result := make(chan error, 1)
	go func() { result <- pgPaySource(f.ctx, f.db, p, f.actor, grant, 7, true) }()
	waitAtomicBlocked(t, af, pid, "wallet_v1.wallets")
	// Already-authored scheduled policy changes without taking the held account lock.
	query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind,starts_at)VALUES($1,'disable_account',clock_timestamp())`, f.actor)
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-result; e == nil {
		t.Fatal("post-wait punishment accepted")
	}
	if pgPointBalance(t, f.db, payer).String() != "100.000" || pgPointBalance(t, f.db, payee).Sign() != 0 {
		t.Fatal("post-wait failure changed balances")
	}
}
func TestPGRealmCanonicalSourceExperienceTenureAndMemberFilters(t *testing.T) {
	f := newRealmMemberFixture(t)
	member, memberCtx := addRealmMember(t, f)
	f.memberConfig.EnableDailyTenure = false
	f = configureRealmMember(t, f)
	query(t, f.db, `UPDATE owned_realm_members SET joined_at=clock_timestamp()-interval '2 days' WHERE realm_id=$1 AND account_id=$2`, f.realm.ID, member)
	pub, post := authorityUUID(t), authorityUUID(t)
	name := "synthetic-realm-xp-" + pub
	query(t, f.db, `INSERT INTO owned_publishers(id,name,nick,type,account_id,created_at,updated_at)VALUES($1,$2,$2,0,$3,now(),now())`, pub, name, member)
	query(t, f.db, `INSERT INTO owned_text_posts(id,publisher_id,realm_id,content,visibility,type,published_at,created_at,updated_at)VALUES($1,$2,$3,'Synthetic canonical realm post',0,0,now(),now(),now())`, post, pub, f.realm.ID)
	query(t, f.db, `INSERT INTO owned_text_post_audit(actor_id,post_id,action,occurred_at)VALUES($1,$2,'create',now())`, member, post)
	if _, e := f.members.ProcessDue(f.ctx, 1000); e != nil {
		t.Fatal(e)
	}
	if _, e := f.members.ProcessDue(f.ctx, 1000); e != nil {
		t.Fatal(e)
	}
	var delta int64
	if e := f.db.QueryRow(`SELECT sum(delta)::bigint FROM owned_realm_source_experience WHERE realm_id=$1 AND account_id=$2`, f.realm.ID, member).Scan(&delta); e != nil || delta != 20 {
		t.Fatal("duplicate or missing post XP", delta, e)
	}
	list, total, e := f.social.MembersFiltered(memberCtx, member, f.realm.ID, social.MemberFilter{Take: 20, OrderBy: "experience_desc", WithStatus: true})
	if e != nil || total != 2 || len(list) != 2 || list[0].AccountID != member {
		t.Fatal(list, total, e)
	}
	f.memberConfig.EnableDailyTenure = true
	f = configureRealmMember(t, f)
	if _, e = f.members.ProcessDue(f.ctx, 1000); e != nil {
		t.Fatal(e)
	}
	if _, e = f.members.ProcessDue(f.ctx, 1000); e != nil {
		t.Fatal(e)
	}
	if e = f.db.QueryRow(`SELECT sum(delta)::bigint FROM owned_realm_tenure_experience WHERE realm_id=$1 AND account_id=$2`, f.realm.ID, member).Scan(&delta); e != nil || delta != 5 {
		t.Fatal("automatic tenure replay", delta, e)
	}
}

type pgPushDeviceProvider struct {
	mu        sync.Mutex
	data      map[string]string
	token     string
	before    func()
	wake      func(context.Context, string) error
	wakeCalls map[string]int
}

func (p *pgPushDeviceProvider) SendDevice(ctx context.Context, token string, data map[string]string) error {
	p.mu.Lock()
	p.data = map[string]string{}
	for k, v := range data {
		p.data[k] = v
	}
	p.token = token
	if data["kind"] == "notifications.wakeup" {
		if p.wakeCalls == nil {
			p.wakeCalls = map[string]int{}
		}
		p.wakeCalls[token]++
	}
	p.mu.Unlock()
	if data["kind"] == "notifications.wakeup" && p.wake != nil {
		return p.wake(ctx, token)
	}
	if p.before != nil {
		p.before()
	}
	return nil
}
func (p *pgPushDeviceProvider) proof() notifications.DeviceProof {
	p.mu.Lock()
	defer p.mu.Unlock()
	return notifications.DeviceProof{ChallengeID: p.data["challenge_id"], Code: p.data["challenge"]}
}
func TestPGNotificationDevicePossessionEncryptionReplayAndRestart(t *testing.T) {
	f := newPresenceProgressionFixture(t)
	query(t, f.db, notificationpg.DevicesSchema)
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: notifications.DevicePermission}, {Pattern: "progression.*"}}, nil); e != nil {
		t.Fatal(e)
	}
	provider := &pgPushDeviceProvider{}
	cfg := notificationpg.DeviceConfig{EncryptionKey: make([]byte, 32), Provider: provider}
	store, e := notificationpg.NewDevices(f.db, f.guard, cfg, f.progression)
	if e != nil {
		t.Fatal(e)
	}
	id := authorityUUID(t)
	token := "synthetic-FCM-token-" + authorityUUID(t)
	challenge, e := store.StartDevice(f.ctx, f.actor, notifications.DeviceRequest{DeviceID: id, Label: "Synthetic device", Token: token, ConsentHours: 1})
	if e != nil {
		t.Fatal(e)
	}
	// API never discloses the device-only challenge or at-rest ciphertext/plain token.
	raw, _ := json.Marshal(challenge)
	if strings.Contains(string(raw), provider.proof().Code) || strings.Contains(string(raw), token) {
		t.Fatal("device challenge leaked")
	}
	if _, e = store.ConfirmDevice(f.ctx, f.actor, notifications.DeviceProof{ChallengeID: challenge.ID, Code: strings.Repeat("a", 43)}); e == nil {
		t.Fatal("client metadata substituted for possession")
	}
	store, e = notificationpg.NewDevices(database(t), f.guard, cfg, f.progression)
	if e != nil {
		t.Fatal(e)
	}
	// Guard deliberately rejects a different pool; restart uses the same configured
	// authority pool with fresh process/store objects, retaining ciphertext and proof.
	if _, e = store.ConfirmDevice(f.ctx, f.actor, provider.proof()); e == nil {
		t.Fatal("cross-pool proof accepted")
	}
	store, e = notificationpg.NewDevices(f.db, f.guard, cfg, progressionpg.NewWithAuthority(f.db, f.guard))
	if e != nil {
		t.Fatal(e)
	}
	dev, e := store.ConfirmDevice(f.ctx, f.actor, provider.proof())
	if e != nil || dev.ID != id {
		t.Fatal(dev, e)
	}
	if _, e = store.ConfirmDevice(f.ctx, f.actor, provider.proof()); e == nil {
		t.Fatal("single-use device proof replay")
	}
	list, e := store.Devices(f.ctx, f.actor)
	if e != nil || len(list) != 1 {
		t.Fatal(list, e)
	}
	raw, _ = json.Marshal(list)
	if strings.Contains(string(raw), token) || strings.Contains(string(raw), "sealed_token") {
		t.Fatal("device token in projection")
	}
	var sealed []byte
	if e = f.db.QueryRow(`SELECT sealed_token FROM notification_v1.devices WHERE id=$1`, id).Scan(&sealed); e != nil || strings.Contains(string(sealed), token) {
		t.Fatal("token unencrypted at rest", e)
	}
	if n := ppScalar(t, f, `SELECT count(*) FROM owned_progression_events WHERE account_id=$1 AND kind='account.push_enable'`, f.actor); n != 1 {
		t.Fatal("missing canonical push reward event", n)
	}
	// Two new instances consume one possession proof only once.
	challenge, e = store.StartDevice(f.ctx, f.actor, notifications.DeviceRequest{DeviceID: id, Label: "Renewed", Token: token, ConsentHours: 1})
	if e != nil {
		t.Fatal(e)
	}
	proof := provider.proof()
	var accepted atomic.Int64
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, _ := notificationpg.NewDevices(f.db, f.guard, cfg, f.progression)
			if _, e := s.ConfirmDevice(f.ctx, f.actor, proof); e == nil {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatal("multiinstance proof replay", accepted.Load())
	}
	policy := notificationpg.New(f.db, f.guard, "synthetic")
	store.WithDeliveryPolicy(policy)
	wake := pgDeviceDelivery(t, f, policy)
	if e = store.Send(f.ctx, wake); e != nil {
		t.Fatal(e)
	}
	provider.mu.Lock()
	publicData, _ := json.Marshal(provider.data)
	provider.mu.Unlock()
	if strings.Contains(string(publicData), "Private text") || strings.Contains(string(publicData), token) {
		t.Fatal("push escaped canonical inbox content policy")
	}
	if e = store.RevokeDevice(f.ctx, f.actor, id); e != nil {
		t.Fatal(e)
	}
	delivery := pgDeviceDelivery(t, f, policy)
	if e = store.Send(f.ctx, delivery); !errors.Is(e, notifications.ErrDeliveryUnconfigured) {
		t.Fatal("revoked device received push", e)
	}
}
func TestPGNotificationDeviceProofExpiryDisableAndCrossOwner(t *testing.T) {
	f := newPresenceProgressionFixture(t)
	query(t, f.db, notificationpg.DevicesSchema)
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: notifications.DevicePermission}, {Pattern: "progression.*"}}, nil); e != nil {
		t.Fatal(e)
	}
	provider := &pgPushDeviceProvider{}
	cfg := notificationpg.DeviceConfig{EncryptionKey: make([]byte, 32), Provider: provider}
	store, _ := notificationpg.NewDevices(f.db, f.guard, cfg, f.progression)
	id := authorityUUID(t)
	token := "synthetic-device-owned-" + authorityUUID(t)
	_, e := store.StartDevice(f.ctx, f.actor, notifications.DeviceRequest{DeviceID: id, Token: token, ConsentHours: 1})
	if e != nil {
		t.Fatal(e)
	}
	proof := provider.proof()
	query(t, f.db, `UPDATE notification_v1.device_challenges SET created_at=clock_timestamp()-interval '2 minutes',expires_at=clock_timestamp()-interval '1 minute' WHERE id=$1`, proof.ChallengeID)
	if _, e = store.ConfirmDevice(f.ctx, f.actor, proof); e == nil {
		t.Fatal("expired possession proof accepted")
	}
	_, e = store.StartDevice(f.ctx, f.actor, notifications.DeviceRequest{DeviceID: id, Token: token, ConsentHours: 1})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = store.ConfirmDevice(f.ctx, f.actor, provider.proof()); e != nil {
		t.Fatal(e)
	}
	other := ppOther(t, f)
	if e = authoritypg.New(f.db).ReplacePolicy(f.ctx, other, []authority.Rule{{Pattern: notifications.DevicePermission}}, nil); e != nil {
		t.Fatal(e)
	}
	p, e := f.verifier.Authenticate(f.ctx, pgIssue(t, f.issuer, other).Token)
	if e != nil {
		t.Fatal(e)
	}
	otherCtx := f.guard.Bind(context.Background(), p)
	if _, e = store.StartDevice(otherCtx, other, notifications.DeviceRequest{DeviceID: authorityUUID(t), Token: token, ConsentHours: 1}); !errors.Is(e, notifications.ErrConflict) {
		t.Fatal("other owner took a verified token", e)
	}
	provider.before = func() { query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor) }
	if _, e = store.StartDevice(f.ctx, f.actor, notifications.DeviceRequest{DeviceID: authorityUUID(t), Token: "another-synthetic-token", ConsentHours: 1}); e == nil {
		t.Fatal("post-provider disabled actor retained registration proof")
	}
	var pending int
	if e = f.db.QueryRow(`SELECT count(*) FROM notification_v1.device_challenges WHERE account_id=$1 AND sent_at IS NULL AND consumed_at IS NULL`, f.actor).Scan(&pending); e != nil || pending != 1 {
		t.Fatal(pending, e)
	}
}

func TestPGProgressionBirthdayBackdateEntitlementAndSeedSync(t *testing.T) {
	f := newCommerceFixture(t)
	r := progressionpg.NewWithAuthority(f.db, f.guard).WithCheckInPerks(f.r)
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "progression.*"}, {Pattern: "subscriptions.*"}, {Pattern: "orders.*"}, {Pattern: "wallets.*"}}, nil); e != nil {
		t.Fatal(e)
	}
	if e := r.WithCheckInPolicy(progressionpg.CheckInPolicy{BackdateWindowDays: 7, PerkProducts: []string{f.plan}, BirthdayExperience: 123}); e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	birthday := time.Date(1990, now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	query(t, f.db, `UPDATE owned_authority_accounts SET created_at=clock_timestamp()-interval '10 days' WHERE id=$1`, f.actor)
	query(t, f.db, `INSERT INTO owned_accounts_profiles(account_id,id,time_zone,birthday,updated_at)VALUES($1,$2,'UTC',$3,clock_timestamp())ON CONFLICT(account_id)DO UPDATE SET birthday=EXCLUDED.birthday`, f.actor, authorityUUID(t), birthday)
	yesterday := now.AddDate(0, 0, -1)
	if _, e := r.BackdatedCheckIn(f.ctx, f.actor, yesterday); !errors.Is(e, progression.ErrForbidden) {
		t.Fatal("unpaid backdating accepted", e)
	}
	sub, e := f.r.CreateSubscription(f.ctx, f.actor, commerce.CreateSubscription{Identifier: f.plan, PaymentMethod: "solian.wallet", PaymentDetails: commerce.PaymentDetails{Currency: "synthetic-commerce", PayerWalletID: &f.wallet.ID}}, "owned-backdate-perk", false)
	if e != nil {
		t.Fatal(e)
	}
	order, e := f.r.SubscriptionOrder(f.ctx, f.actor, sub.Identifier)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.PayOrder(f.ctx, f.actor, order.ID, &f.wallet.ID); e != nil {
		t.Fatal(e)
	}
	a, e := r.BackdatedCheckIn(f.ctx, f.actor, yesterday)
	if e != nil || a.BackdatedFrom == nil {
		t.Fatal(a, e)
	}
	b, e := r.BackdatedCheckIn(f.ctx, f.actor, yesterday)
	if e != nil || b.ID != a.ID {
		t.Fatal("backdate replay", b, e)
	}
	if _, e = r.CheckIn(f.ctx, f.actor); e != nil {
		t.Fatal(e)
	}
	if _, e = r.CheckIn(f.ctx, f.actor); e != nil {
		t.Fatal(e)
	}
	var xp int64
	if e = f.db.QueryRow(`SELECT sum(delta)::bigint FROM owned_progression_experience WHERE account_id=$1 AND reason_type IN('check-in','birthday')`, f.actor).Scan(&xp); e != nil || xp != 323 {
		t.Fatal("birthday/check-in reward replay", xp, e)
	}
	custom := progression.Definition{Identifier: "synthetic-unmanaged-" + authorityUUID(t), Title: "Owner-authored definition", IsEnabled: true, IsProgressEnabled: true, TargetCount: 1, Trigger: progression.Trigger{Actions: []string{"account.active"}, Mode: "count"}, Reward: progression.Reward{Experience: 1}}
	if _, e = r.SaveDefinition(f.ctx, f.actor, "achievement", "", custom, true); e != nil {
		t.Fatal(e)
	}
	if n, e := r.SyncSeeds(f.ctx, f.actor); e != nil || n == 0 {
		t.Fatal(n, e)
	}
	var title string
	if e = f.db.QueryRow(`SELECT definition->>'title' FROM owned_progression_definitions WHERE kind='achievement'AND identifier=$1`, custom.Identifier).Scan(&title); e != nil || title != custom.Title {
		t.Fatal("seed synchronization overwrote owner definition", title, e)
	}
}

func TestPGProgressionFirstPrimaryWalletFundingAtomicity(t *testing.T) {
	f := newPresenceProgressionFixture(t)
	treasury := ppOther(t, f)
	payer := pgSourceWallet(t, f, treasury, "0")
	grant := pgSourceGrant(t, f, 7)
	p, e := progressionpg.NewFundedSourcePoints(progressionpg.SourcePointConfig{TreasuryAccountID: treasury, TreasuryWalletID: payer, Currency: "synthetic-earned-points", MaxPerGrant: 10, MaxPerDay: 100})
	if e != nil {
		t.Fatal(e)
	}
	if e = pgPaySource(f.ctx, f.db, p, f.actor, grant, 7, true); e == nil {
		t.Fatal("empty treasury funded first wallet")
	}
	if n := ppScalar(t, f, `SELECT count(*) FROM wallet_v1.wallets WHERE account_id=$1`, f.actor); n != 0 {
		t.Fatal("failed reward leaked new wallet", n)
	}
	query(t, f.db, `UPDATE wallet_v1.pockets SET amount=100 WHERE wallet_id=$1`, payer)
	otherGrant := pgSourceGrant(t, f, 8)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, g := range []struct {
		id     string
		points int64
	}{{grant, 7}, {otherGrant, 8}} {
		wg.Add(1)
		go func(g struct {
			id     string
			points int64
		}) {
			defer wg.Done()
			errs <- pgPaySource(f.ctx, f.db, p, f.actor, g.id, g.points, true)
		}(g)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if n := ppScalar(t, f, `SELECT count(*) FROM wallet_v1.wallets WHERE account_id=$1 AND is_primary`, f.actor); n != 1 {
		t.Fatal("concurrent rewards split canonical primary", n)
	}
	var payee string
	if e = f.db.QueryRow(`SELECT id::text FROM wallet_v1.wallets WHERE account_id=$1 AND is_primary`, f.actor).Scan(&payee); e != nil {
		t.Fatal(e)
	}
	if pgPointBalance(t, f.db, payer).String() != "85.000" || pgPointBalance(t, f.db, payee).String() != "15.000" {
		t.Fatal("first-wallet payout did not conserve money")
	}
}

func TestPGCanonicalFriendAcceptanceTimingAndDailyAchievements(t *testing.T) {
	f := newPresenceProgressionFixture(t)
	query(t, f.db, socialpg.Schema)
	store := authoritypg.New(f.db)
	if e := store.ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "relationships.*"}, {Pattern: "progression.*"}}, nil); e != nil {
		t.Fatal(e)
	}
	r := socialpg.NewWithAuthority(f.db, f.guard)
	for i := range 5 {
		other := ppOther(t, f)
		if e := store.ReplacePolicy(f.ctx, other, []authority.Rule{{Pattern: "relationships.*"}}, nil); e != nil {
			t.Fatal(e)
		}
		p, e := f.verifier.Authenticate(f.ctx, pgIssue(t, f.issuer, other).Token)
		if e != nil {
			t.Fatal(e)
		}
		request, e := r.ChangeRelationship(f.guard.Bind(context.Background(), p), other, f.actor, social.RelationshipChange{Action: social.RequestFriend})
		if e != nil {
			t.Fatal(e)
		}
		if i == 0 {
			query(t, f.db, `UPDATE owned_social_requests SET created_at=clock_timestamp()-interval '2 minutes'WHERE id=$1`, request.ID)
		}
		rel, e := r.ChangeRelationship(f.ctx, f.actor, other, social.RelationshipChange{Action: social.AcceptFriend})
		if e != nil {
			t.Fatal(e)
		}
		if e = f.progression.RecordCanonicalEvent(f.ctx, f.actor, "relationship.friend_accept", request.ID); e != nil {
			t.Fatal(e)
		}
		if i == 0 && ppScalar(t, f, `SELECT count(*)FROM owned_progression_grants WHERE account_id=$1 AND identifier='hidden-speed-friend'`, f.actor) != 0 {
			t.Fatal("slow request received speed achievement")
		}
		if e = f.progression.RecordCanonicalEvent(f.ctx, f.actor, "relationship.friend_established", rel.ID); e != nil {
			t.Fatal(e)
		}
		if e = f.progression.RecordCanonicalEvent(f.ctx, f.actor, "relationship.friend_accept", request.ID); e != nil {
			t.Fatal("canonical receipt replay", e)
		}
	}
	for _, id := range []string{"hidden-speed-friend", "hidden-social-butterfly-day"} {
		if n := ppScalar(t, f, `SELECT count(*)FROM owned_progression_grants WHERE account_id=$1 AND identifier=$2`, f.actor, id); n != 1 {
			t.Fatal(id, n)
		}
	}
	if _, e := f.progression.ProcessDue(f.ctx, 1000); e != nil {
		t.Fatal(e)
	}
	if n := ppScalar(t, f, `SELECT count(*)FROM owned_progression_grants WHERE account_id=$1 AND identifier='hidden-speed-friend'`, f.actor); n != 1 {
		t.Fatal("worker replay duplicated earned grant", n)
	}
	if n := ppScalar(t, f, `SELECT count(*)FROM owned_social_friend_accepts WHERE account_id=$1`, f.actor); n != 5 {
		t.Fatal("acceptance lost server provenance", n)
	}
}

func TestPGProviderPollOriginalPermissionCheckedBeforeExternalRead(t *testing.T) {
	f := newPresenceProgressionFixture(t)
	linksRepo := pgConnections(t, f, pgVerifiedProvider{identifier: "76561198000000003", calls: &atomic.Int64{}})
	state, e := linksRepo.Start(f.ctx, f.actor, "steam")
	if e != nil {
		t.Fatal(e)
	}
	link, e := linksRepo.Complete(f.ctx, f.actor, "steam", links.Completion{State: state.State, Code: "verified"})
	if e != nil {
		t.Fatal(e)
	}
	calls := &atomic.Int64{}
	r := activitypg.New(f.db, f.guard).WithSteamProvider(pgProviderObserver{calls: calls})
	visibility := activities.Private
	if _, e = r.ObserveProvider(f.ctx, f.actor, activities.ProviderRequest{Provider: "steam", ConnectionID: link.ID, Visibility: &visibility, LeaseMinutes: 5, ConsentMinutes: 30}); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE owned_provider_presence_consents SET next_at=clock_timestamp()-interval '1 second'WHERE connection_id=$1`, link.ID)
	if e = authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "account.connections.manage"}}, nil); e != nil {
		t.Fatal(e)
	}
	if n, e := r.ProcessProviders(f.ctx, 1); e != nil || n != 0 || calls.Load() != 1 {
		t.Fatal("revoked original scope accessed provider", n, e, calls.Load())
	}
	if n := ppScalar(t, f, `SELECT count(*)FROM owned_provider_presence_consents WHERE connection_id=$1 AND revoked_at IS NOT NULL AND claim_id IS NULL`, link.ID); n != 1 {
		t.Fatal("invalid consent retained poll claim", n)
	}
}

func TestPGAccountActionRequestContextCanonicalAtomicAndPrivate(t *testing.T) {
	f := newActivityFixture(t)
	query(t, f.db, accounthistorypg.RequestContextSchema)
	r := fixtureRawJSONRequest("PATCH", "/api/accounts/me", f.token, `{"bio":"private-body-not-in-audit"}`)
	r.RemoteAddr = "203.0.113.12:45120"
	r.Header.Set("X-Forwarded-For", "198.51.100.77")
	r.Header.Set("User-Agent", "SyntheticBrowser/1\r\nunsafe\x00")
	w := httptest.NewRecorder()
	f.app.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	repo := historyRepository(f)
	logs, n, e := repo.Actions(f.ctx, f.actor, history.Filter{Take: 20, Action: "accounts.profile.update"})
	if e != nil || n != 1 || len(logs) != 1 {
		t.Fatal(logs, n, e)
	}
	verifier, e := authority.NewVerifier(authoritypg.New(f.db), f.config)
	if e != nil {
		t.Fatal(e)
	}
	original, e := verifier.Authenticate(f.ctx, f.token)
	if e != nil {
		t.Fatal(e)
	}
	v := logs[0]
	if v.SessionID == nil || *v.SessionID != original.Session.ID || v.IPAddress == nil || *v.IPAddress != "203.0.113.12" || v.UserAgent == nil || *v.UserAgent != "SyntheticBrowser/1unsafe" || v.Meta["user_agent_untrusted"] != true {
		t.Fatal(v)
	}
	raw, _ := json.Marshal(v)
	if strings.Contains(string(raw), f.token) || strings.Contains(string(raw), "private-body") || strings.Contains(string(raw), "198.51.100.77") {
		t.Fatal("credential/body/forwarded header escaped audit", string(raw))
	}
	backendCall(t, f.app, "GET", "/api/accounts/"+f.actor+"/actions", f.otherToken, "", 404)
	tx, e := f.db.BeginTx(f.ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	var id int64
	if e = tx.QueryRow(`INSERT INTO owned_accounts_audit(actor_id,action,target_id,occurred_at)VALUES($1,'synthetic.rollback',$1,clock_timestamp())RETURNING id`, f.actor).Scan(&id); e != nil {
		t.Fatal(e)
	}
	if e = auditcapture.Record(auditcapture.Bind(f.ctx, r, original), tx, f.actor, "owned_accounts_audit", id); e != nil {
		t.Fatal(e)
	}
	if e = tx.Rollback(); e != nil {
		t.Fatal(e)
	}
	if n := ppScalar(t, presenceProgressionFixture{db: f.db, ctx: f.ctx}, `SELECT count(*)FROM owned_account_action_context WHERE source='owned_accounts_audit'AND source_id=$1`, fmt.Sprint(id)); n != 0 {
		t.Fatal("rollback retained request audit", n)
	}
	if _, e = f.db.Exec(`UPDATE owned_account_action_context SET ip_address='192.0.2.88'WHERE account_id=$1`, f.actor); e == nil {
		t.Fatal("immutable transport facts rewritten")
	}
}

type pgCheckInCaptcha struct {
	peer, token string
	before      func()
	err         error
}

func (c *pgCheckInCaptcha) Verify(ctx context.Context, token, peer string) error {
	c.token, c.peer = token, peer
	if c.before != nil {
		c.before()
	}
	return c.err
}
func TestPGCheckInCaptchaOriginalAuthorityAfterProvider(t *testing.T) {
	f := newBackendFixture(t)
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	captcha := &pgCheckInCaptcha{err: signin.ErrInvalid}
	app, e := application.NewPostgres(f.db, nil, application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, ProgressionCaptcha: captcha})
	if e != nil {
		t.Fatal(e)
	}
	backendCall(t, app, "POST", "/api/accounts/me/check-in", f.token, `null`, 400)
	backendCall(t, app, "POST", "/api/accounts/me/check-in", f.token, `"rejected-proof"`, 403)
	if n := ppScalar(t, presenceProgressionFixture{db: f.db, ctx: context.Background()}, `SELECT count(*)FROM owned_progression_checkins WHERE account_id=$1`, f.actor); n != 0 {
		t.Fatal("rejected CAPTCHA wrote check-in", n)
	}
	captcha.err = nil
	r := fixtureRawJSONRequest("POST", "/api/accounts/me/check-in", f.token, `"verified-proof"`)
	r.RemoteAddr = "203.0.113.9:41100"
	r.Header.Set("X-Forwarded-For", "198.51.100.9")
	w := httptest.NewRecorder()
	app.ServeHTTP(w, r)
	if w.Code != 200 || captcha.peer != "203.0.113.9" || captcha.token != "verified-proof" {
		t.Fatal(w.Code, w.Body.String(), captcha)
	}
	captcha.before = func() {
		query(t, f.db, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp()WHERE account_id=$1`, f.other)
	}
	backendCall(t, app, "POST", "/api/accounts/me/check-in", f.otherToken, `"after-revocation"`, 401)
	if n := ppScalar(t, presenceProgressionFixture{db: f.db, ctx: context.Background()}, `SELECT count(*)FROM owned_progression_checkins WHERE account_id=$1`, f.other); n != 0 {
		t.Fatal("post-provider stale authority wrote check-in", n)
	}
}

func TestPGNightOwlReminderDurableHourDedupAndAdminProducer(t *testing.T) {
	f := newAccountAdminFixture(t)
	query(t, f.db, notificationpg.WellbeingSchema)
	repo := notificationpg.New(f.db, f.guard, notifications.DefaultApp).WithNightOwlReminders(true)
	preferences, e := accountsettingspg.New(f.db, f.guard, notifications.DefaultApp)
	if e != nil {
		t.Fatal(e)
	}
	repo.WithPreferencePolicy(preferences)
	zone := ""
	for offset := -14; offset <= 12; offset++ {
		name := fmt.Sprintf("Etc/GMT%+d", offset)
		if offset == 0 {
			name = "Etc/GMT"
		}
		loc, e := time.LoadLocation(name)
		if e == nil && time.Now().In(loc).Hour() < 3 {
			zone = name
			break
		}
	}
	if zone == "" {
		t.Fatal("cannot choose a synthetic local-night timezone")
	}
	query(t, f.db, `INSERT INTO owned_accounts_profiles(account_id,id,time_zone,updated_at)VALUES($1,$2,$3,clock_timestamp())ON CONFLICT(account_id)DO UPDATE SET time_zone=EXCLUDED.time_zone`, f.other, authorityUUID(t), zone)
	query(t, f.db, `INSERT INTO owned_presence_last_seen(account_id,last_seen_at)VALUES($1,clock_timestamp()-interval '1 second')ON CONFLICT(account_id)DO UPDATE SET last_seen_at=EXCLUDED.last_seen_at`, f.other)
	var n atomic.Int64
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := notificationpg.New(f.db, f.guard, notifications.DefaultApp).WithNightOwlReminders(true)
			v, e := r.ProcessNightOwls(f.ctx, 1000)
			n.Add(int64(v))
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if n.Load() != 1 {
		t.Fatal("hourly reminder duplicated across workers", n.Load())
	}
	if n, e := repo.ProcessNightOwls(f.ctx, 1000); e != nil || n != 0 {
		t.Fatal("restart replayed local-hour reminder", n, e)
	}
	if n := ppScalar(t, presenceProgressionFixture{db: f.db, ctx: f.ctx}, `SELECT count(*)FROM notification_v1.night_owl_receipts WHERE account_id=$1`, f.other); n != 1 {
		t.Fatal(n)
	}
	query(t, f.db, `INSERT INTO owned_account_notification_preferences(account_id,app_id,topic,preference,updated_at)VALUES($1,$2,'accounts.wellbeing.sleep','reject',clock_timestamp())ON CONFLICT(account_id,app_id,topic)DO UPDATE SET preference='reject'`, f.other, notifications.DefaultApp)
	if _, e := repo.ProcessPending(f.ctx, 100); e != nil {
		t.Fatal(e)
	}
	if n := ppScalar(t, presenceProgressionFixture{db: f.db, ctx: f.ctx}, `SELECT count(*)FROM notification_v1.inbox WHERE account_id=$1 AND topic='accounts.wellbeing.sleep'`, f.other); n != 0 {
		t.Fatal("ignored current wellbeing notification preference", n)
	}
	f.repo.WithNightOwlSender(repo)
	id, e := f.repo.SendNightOwlReminder(f.ctx, f.actor, f.other, 2)
	if e != nil || id == "" {
		t.Fatal(id, e)
	}
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.other)
	if _, e = f.repo.SendNightOwlReminder(f.ctx, f.actor, f.other, 1); e == nil {
		t.Fatal("disabled target queued reminder")
	}
}

func TestPGAdminSteamScanSchedulesOnlyOriginalOwnerConsent(t *testing.T) {
	f := newAccountAdminFixture(t)
	query(t, f.db, connectionpg.Schema)
	verifier, e := authority.NewVerifier(authoritypg.New(f.db), f.config)
	if e != nil {
		t.Fatal(e)
	}
	owner, e := verifier.Authenticate(f.ctx, f.otherToken)
	if e != nil {
		t.Fatal(e)
	}
	ownerCtx := f.guard.Bind(context.Background(), owner)
	connections, e := connectionpg.New(f.db, f.guard, connectionpg.Config{Providers: []links.Provider{pgVerifiedProvider{identifier: "76561198000000004", calls: &atomic.Int64{}}}, EncryptionKey: make([]byte, 32)})
	if e != nil {
		t.Fatal(e)
	}
	start, e := connections.Start(ownerCtx, f.other, "steam")
	if e != nil {
		t.Fatal(e)
	}
	link, e := connections.Complete(ownerCtx, f.other, "steam", links.Completion{State: start.State, Code: "verified"})
	if e != nil {
		t.Fatal(e)
	}
	calls := &atomic.Int64{}
	activitiesRepo := activitypg.New(f.db, f.guard).WithSteamProvider(pgProviderObserver{calls: calls})
	visibility := activities.Private
	if _, e = activitiesRepo.ObserveProvider(ownerCtx, f.other, activities.ProviderRequest{Provider: "steam", ConnectionID: link.ID, Visibility: &visibility, LeaseMinutes: 5, ConsentMinutes: 30}); e != nil {
		t.Fatal(e)
	}
	f.repo.WithPresenceScanner(activitiesRepo)
	if n, e := f.repo.ScheduleSteamPresence(f.ctx, f.actor, a.ProviderScan{AccountRef: f.other}); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	if n, e := activitiesRepo.ProcessProviders(context.Background(), 1); e != nil || n != 1 || calls.Load() != 2 {
		t.Fatal(n, e, calls.Load())
	}
	if n, e := f.repo.ScheduleSteamPresence(f.ctx, f.actor, a.ProviderScan{ProviderIdentifier: "76561198099999999"}); e != nil || n != 0 || calls.Load() != 2 {
		t.Fatal("arbitrary Steam identifier caused provider access", n, e, calls.Load())
	}
	query(t, f.db, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp()WHERE id=$1`, owner.Session.ID)
	if n, e := f.repo.ScheduleSteamPresence(f.ctx, f.actor, a.ProviderScan{AccountRef: f.other}); e != nil || n != 0 {
		t.Fatal("admin substituted owner admission", n, e)
	}
	if e = authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "accounts.view"}}, nil); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.ScheduleSteamPresence(f.ctx, f.actor, a.ProviderScan{}); e == nil {
		t.Fatal("missing explicit admin scan grant accepted")
	}
}

func TestPGCanonicalIdentityMediaProviderAndTOTPRewardSources(t *testing.T) {
	f := newProfileMediaFixture(t)
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor.ID, []authority.Rule{{Pattern: "accounts.manage"}, {Pattern: "media.files.*"}, {Pattern: "account.connections.manage"}, {Pattern: "progression.*"}}, nil); e != nil {
		t.Fatal(e)
	}
	picture := profileUpload(t, f.app, f.token, "image/png", profilePNG(t))
	backendJSONCall(t, f.app, "PATCH", "/api/accounts/me/profile", f.token, map[string]any{"picture_id": picture, "nick": "Synthetic complete profile", "bio": "Authored profile", "pronouns": "they/them", "location": "Synthetic location", "birthday": "1980-01-01T00:00:00Z"}, 200)
	guard := authoritypg.NewTransactionGuard(authoritypg.New(f.db), f.verifier)
	p, e := f.verifier.Authenticate(f.ctx, f.token)
	if e != nil {
		t.Fatal(e)
	}
	ctx := guard.Bind(f.ctx, p)
	connections, e := connectionpg.New(f.db, guard, connectionpg.Config{Providers: []links.Provider{pgVerifiedProvider{identifier: "76561198000000005", calls: &atomic.Int64{}}}, EncryptionKey: make([]byte, 32)})
	if e != nil {
		t.Fatal(e)
	}
	state, e := connections.Start(ctx, f.actor.ID, "steam")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = connections.Complete(ctx, f.actor.ID, "steam", links.Completion{State: state.State, Code: "verified"}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.CreateFactor(f.ctx, f.token, 5, ""); e != nil {
		t.Fatal(e)
	}
	factor, e := f.service.CreateFactor(f.ctx, f.token, 3, "")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.EnableFactor(f.ctx, f.token, factor.ID, syntheticTOTP(t, factor.CreatedResponse["uri"], f.now.Load())); e != nil {
		t.Fatal(e)
	}
	// Credential epoch changes deliberately invalidate the enrollment bearer;
	// maintenance consumes committed owned facts without fabricating that proof.
	if _, e = f.verifier.Authenticate(f.ctx, f.token); !errors.Is(e, authority.ErrStale) {
		t.Fatal("enrollment kept stale proof active", e)
	}
	r := progressionpg.NewWithAuthority(f.db, guard)
	if _, e = r.ProcessDue(context.Background(), 1000); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"first-avatar", "first-connection", "profile-complete", "first-2fa"} {
		var n int
		if e = f.db.QueryRow(`SELECT count(*)FROM owned_progression_grants WHERE account_id=$1 AND identifier=$2`, f.actor.ID, id).Scan(&n); e != nil || n != 1 {
			t.Fatal(id, n, e)
		}
	}
	if _, e = r.ProcessDue(context.Background(), 1000); e != nil {
		t.Fatal(e)
	}
	var n int
	if e = f.db.QueryRow(`SELECT count(*)FROM owned_progression_grants WHERE account_id=$1 AND identifier='first-2fa'`, f.actor.ID).Scan(&n); e != nil || n != 1 {
		t.Fatal("2FA reward replay", n, e)
	}
	// Payment PINs and recovery records are not independently enrolled login 2FA.
	pin := authorityUUID(t)
	query(t, f.db, `INSERT INTO owned_authority_factors(id,account_id,type,secret,created_at,enabled_at)VALUES($1,$2,4,NULL,clock_timestamp(),clock_timestamp())`, pin, f.other.ID)
	if _, e = r.ProcessDue(context.Background(), 1000); e != nil {
		t.Fatal(e)
	}
	if e = f.db.QueryRow(`SELECT count(*)FROM owned_progression_grants WHERE account_id=$1 AND identifier='first-2fa'`, f.other.ID).Scan(&n); e != nil || n != 0 {
		t.Fatal("PIN manufactured a 2FA achievement", n, e)
	}
}
