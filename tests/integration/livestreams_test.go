// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/livestreampg"
	"solsynth.local/go-migration/internal/mediapg"
	"solsynth.local/go-migration/internal/mediastore"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/messager/calls"
	"solsynth.local/go-migration/ports/passport/accounts"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
)

// Synthetic provider only. Its host binding uniquely isolates this fixture's
// durable queue while retaining the real adapter's state/acknowledgement contract.
type streamProvider struct {
	callProvider
	endpoint                                                           string
	ingresses                                                          map[string]live.Ingress
	ingressCreates                                                     int
	failIngressBeforeCreate, failIngressAfterCreate, failIngressDelete bool
	grants                                                             []live.Grant
}

func (p *streamProvider) Endpoint() string { return p.endpoint }
func (p *streamProvider) StreamParticipants(ctx context.Context, room string) ([]calls.Participant, error) {
	return p.callProvider.Participants(ctx, room)
}
func (p *streamProvider) CreateIngress(_ context.Context, r live.IngressRequest) (live.Ingress, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ingressCreates++
	if p.failIngressBeforeCreate {
		return live.Ingress{}, live.ErrProvider
	}
	typ := 0
	if r.WHIP {
		typ = 1
	}
	v := live.Ingress{ID: fmt.Sprintf("synthetic-ingress-%d", p.ingressCreates), Name: r.Name, Room: r.Room, Identity: r.Identity, URL: "rtmps://synthetic.invalid/live", Key: "synthetic-only", InputType: typ}
	if typ == 1 {
		v.URL = "https://synthetic.invalid/whip"
	}
	if p.ingresses == nil {
		p.ingresses = map[string]live.Ingress{}
	}
	p.ingresses[v.ID] = v
	if p.failIngressAfterCreate {
		return live.Ingress{}, live.ErrProvider
	}
	return v, nil
}
func (p *streamProvider) ListIngress(_ context.Context, room string) ([]live.Ingress, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := []live.Ingress{}
	for _, v := range p.ingresses {
		if v.Room == room {
			out = append(out, v)
		}
	}
	return out, nil
}
func (p *streamProvider) DeleteIngress(_ context.Context, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failIngressDelete {
		return live.ErrProvider
	}
	delete(p.ingresses, id)
	return nil
}
func (p *streamProvider) StreamToken(_ context.Context, g live.Grant) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.grants = append(p.grants, g)
	return "synthetic-only:" + g.Identity, nil
}

type streamFixture struct {
	callFixture
	repo                                *livestreampg.Repository
	accounts                            *accountpg.Repository
	media                               *mediapg.Repository
	social                              *socialpg.Repository
	provider                            *streamProvider
	pub                                 accounts.Publisher
	ctx, ownerCtx, editorCtx, viewerCtx context.Context
	handler                             http.Handler
}

func streamConfig() livestreampg.Config {
	return livestreampg.Config{TokenTTL: time.Minute, MaxRunDuration: time.Hour, IdleGrace: 5 * time.Minute, MaxStreamsPerPublisher: 20}
}
func newStreamFixture(t *testing.T) streamFixture {
	t.Helper()
	f := streamFixture{callFixture: newCallFixture(t)}
	for _, s := range []string{accountpg.Schema, socialpg.Schema, livestreampg.Schema} {
		query(t, f.db, s)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	f.ctx = ctx
	bind := func(token string) context.Context {
		p, e := f.verifier.Authenticate(ctx, token)
		if e != nil {
			t.Fatal(e)
		}
		return f.guard.Bind(ctx, p)
	}
	f.ownerCtx, f.editorCtx, f.viewerCtx = bind(f.token), bind(f.otherToken), bind(f.thirdToken)
	f.social = socialpg.NewWithAuthority(f.db, f.guard)
	f.accounts = accountpg.NewWithAuthority(f.db, f.guard).WithRealms(f.social)
	query(t, f.db, `INSERT INTO owned_publisher_quota(account_id,total,starts_at)VALUES($1,10,clock_timestamp()-interval '1 second')ON CONFLICT(account_id)DO UPDATE SET total=10,starts_at=EXCLUDED.starts_at`, f.owner)
	name := "synthetic-live-" + strings.ReplaceAll(f.owner, "-", "")
	nick := "Synthetic live publisher"
	var e error
	f.pub, e = f.accounts.CreatePublisher(f.ownerCtx, f.owner, accounts.PublisherPatch{Name: &name, Nick: &nick})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.accounts.ChangeMember(f.ownerCtx, f.owner, f.pub.ID, accounts.MemberChange{Action: accounts.InviteMember, AccountID: f.other, Role: accounts.Editor}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.accounts.ChangeMember(f.editorCtx, f.other, f.pub.ID, accounts.MemberChange{Action: accounts.AcceptInvite}); e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	if e = os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	store, e := mediastore.NewFilesystem(root, 1<<20)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { store.Close() })
	f.media, e = mediapg.New(f.db, store, f.guard, mediapg.Config{MaxFileBytes: 1 << 20, PendingTTL: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	for _, actor := range []string{f.owner, f.other} {
		query(t, f.db, `INSERT INTO owned_media_quotas(account_id,limit_bytes)VALUES($1,1048576)ON CONFLICT(account_id)DO NOTHING`, actor)
	}
	f.provider = &streamProvider{endpoint: "wss://synthetic-live-" + f.owner + ".invalid"}
	f.repo, e = livestreampg.New(f.db, f.guard, f.accounts, f.media, f.social, f.provider, streamConfig())
	if e != nil {
		t.Fatal(e)
	}
	f.handler = (&live.Service{Repository: f.repo, Authority: f.verifier, Bind: f.guard.Bind}).Handler()
	if e = f.repo.Ready(ctx); e != nil {
		t.Fatal(e)
	}
	return f
}
func streamCreate(t *testing.T, f streamFixture, key string) live.Stream {
	t.Helper()
	title := "Synthetic live stream"
	v, e := f.repo.Create(f.ownerCtx, f.owner, f.pub.Name, key, live.Patch{Title: &title})
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func streamNative(t *testing.T, f streamFixture, id string) live.StartResponse {
	t.Helper()
	yes := true
	v, e := f.repo.Start(f.ownerCtx, f.owner, id, live.StartRequest{NoIngress: &yes})
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func streamDue(t *testing.T, f streamFixture, id string) {
	t.Helper()
	query(t, f.db, `UPDATE owned_livestream_runs SET next_attempt_at=clock_timestamp()-interval '1 microsecond' WHERE stream_id=$1`, id)
	if _, e := f.repo.ProcessDue(f.ctx, 100); e != nil {
		t.Fatal(e)
	}
}
func streamGetCall(t *testing.T, f streamFixture, path, token string, status int) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, fixtureNoBodyRequest("GET", path, token))
	if w.Code != status {
		t.Fatalf("GET %s status%d: %s", path, w.Code, w.Body.String())
	}
	return w
}

func TestPGLiveStreamLifecycleTokensAndPrivateVisibility(t *testing.T) {
	f := newStreamFixture(t)
	w := backendJSONCall(t, f.handler, "POST", "/api/livestreams?pub="+f.pub.Name, f.token, map[string]any{"title": "Synthetic HTTP stream"}, 200)
	var s live.Stream
	if e := json.Unmarshal(w.Body.Bytes(), &s); e != nil {
		t.Fatal(e)
	}
	if s.Status != live.Pending || s.PublisherID != f.pub.ID || s.IngressStreamKey != nil {
		t.Fatal(s)
	}
	first := streamNative(t, f, s.ID)
	// Native activation intentionally stores NULL ingress_id. Assert the typed
	// timestamp arithmetic and every recorded ownership/version field directly.
	var phase, owner, actor, session, permission string
	var ingress *string
	var generation, ownerVersion, actorVersion, epoch int64
	var updated, due, expires time.Time
	if e := f.db.QueryRow(`SELECT phase,ingress_id,generation,owner_id::text,owner_version,actor_id::text,account_version,session_id::text,session_epoch,permission,updated_at,next_attempt_at,expires_at FROM owned_livestream_runs WHERE stream_id=$1::uuid`, s.ID).Scan(&phase, &ingress, &generation, &owner, &ownerVersion, &actor, &actorVersion, &session, &epoch, &permission, &updated, &due, &expires); e != nil {
		t.Fatal("native activation persisted run", e)
	}
	if phase != "active" || ingress != nil || generation != 1 || owner != f.owner || actor != f.owner || ownerVersion < 1 || actorVersion < 1 || epoch < 1 || !authority.ValidID(session) || permission != "livestreams.start" || due.Sub(updated) != 10*time.Second || !expires.After(updated) {
		t.Fatal("native activation run fields", phase, ingress, generation, ownerVersion, actorVersion, epoch, due.Sub(updated))
	}

	listed, e := f.repo.List(f.ctx, "", "", 100, 0)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, v := range listed {
		found = found || v.ID == s.ID
	}
	if !found {
		t.Fatal("active public stream missing from discovery")
	}
	streamGetCall(t, f, "/api/livestreams/"+s.ID+"/token?streamer=true", f.thirdToken, 403)
	w = streamGetCall(t, f, "/api/livestreams/"+s.ID+"/token?streamer=false", f.thirdToken, 200)
	var token live.TokenResponse
	if e := json.Unmarshal(w.Body.Bytes(), &token); e != nil {
		t.Fatal(e)
	}
	if token.IsStreamer || token.Identity == f.third || token.RoomName != first.RoomName || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(token)
	}
	f.provider.mu.Lock()
	if len(f.provider.grants) != 1 || f.provider.grants[0].Publish {
		t.Fatal("viewer received publish capability")
	}
	f.provider.mu.Unlock()
	if e := f.repo.End(f.ownerCtx, f.owner, s.ID, false); e != nil {
		t.Fatal(e)
	}
	if e := f.repo.End(f.ownerCtx, f.owner, s.ID, false); e != nil {
		t.Fatal(e)
	}
	private := live.Private
	if _, e := f.repo.Update(f.ownerCtx, f.owner, s.ID, live.Patch{Visibility: &private}); e != nil {
		t.Fatal(e)
	}
	streamGetCall(t, f, "/api/livestreams/"+s.ID, "", 404)
	streamGetCall(t, f, "/api/livestreams/"+s.ID, f.thirdToken, 404)
	streamGetCall(t, f, "/api/livestreams/"+s.ID, f.otherToken, 200)
	listed, e = f.repo.List(f.ctx, "", f.pub.ID, 20, 0)
	if e != nil || len(listed) != 0 {
		t.Fatal("private publisher list leaked", listed, e)
	}
	listed, e = f.repo.List(f.ownerCtx, f.owner, f.pub.ID, 20, 0)
	if e != nil || len(listed) != 1 || listed[0].ID != s.ID {
		t.Fatal("editor publisher list", listed, e)
	}
	second := streamNative(t, f, s.ID)
	if second.RoomName == first.RoomName {
		t.Fatal("restarted stream reused cached-token room")
	}
	if e := f.repo.End(f.ownerCtx, f.owner, s.ID, true); e != nil {
		t.Fatal(e)
	}
	streamGetCall(t, f, "/api/livestreams/"+s.ID, f.token, 404)
}

func TestPGLiveStreamIngressUncertainCreateAndDurableCleanup(t *testing.T) {
	f := newStreamFixture(t)
	s := streamCreate(t, f, "uncertain")
	f.provider.failIngressAfterCreate = true
	if _, e := f.repo.Start(f.ownerCtx, f.owner, s.ID, live.StartRequest{}); !errors.Is(e, live.ErrProvider) {
		t.Fatal(e)
	}
	var phase string
	if e := f.db.QueryRow(`SELECT phase FROM owned_livestream_runs WHERE stream_id=$1`, s.ID).Scan(&phase); e != nil || phase != "attempted" {
		t.Fatal(phase, e)
	}
	f.provider.failIngressAfterCreate = false
	started, e := f.repo.Start(f.ownerCtx, f.owner, s.ID, live.StartRequest{})
	if e != nil || started.StreamKey == nil || f.provider.ingressCreates != 1 {
		t.Fatal(started, e, f.provider.ingressCreates)
	}
	public, e := f.repo.Get(f.ctx, "", s.ID)
	if e != nil || public.IngressStreamKey != nil || public.IngressID != nil {
		t.Fatal("public ingress disclosure", e)
	}
	f.provider.failIngressDelete = true
	if e = f.repo.End(f.ownerCtx, f.owner, s.ID, false); !errors.Is(e, live.ErrProvider) {
		t.Fatal(e)
	}
	if e = f.db.QueryRow(`SELECT phase FROM owned_livestream_runs WHERE stream_id=$1`, s.ID).Scan(&phase); e != nil || phase != "ending" {
		t.Fatal("cleanup intent lost", phase, e)
	}
	f.provider.failIngressDelete = false
	streamDue(t, f, s.ID)
	if e = f.db.QueryRow(`SELECT phase FROM owned_livestream_runs WHERE stream_id=$1`, s.ID).Scan(&phase); e != nil || phase != "done" {
		t.Fatal(phase, e)
	}
	if len(f.provider.ingresses) != 0 {
		t.Fatal("ingress survived acknowledged cleanup")
	}
}

func TestPGLiveStreamCapturedPermissionAndPublisherRevocation(t *testing.T) {
	f := newStreamFixture(t)
	s := streamCreate(t, f, "revocation")
	yes := true
	if _, e := f.repo.Start(f.editorCtx, f.other, s.ID, live.StartRequest{NoIngress: &yes}); e != nil {
		t.Fatal(e)
	}
	viewer, e := f.repo.Token(f.viewerCtx, f.third, s.ID, false, false)
	if e != nil {
		t.Fatal(e)
	}
	if e = authoritypg.New(f.db).ReplacePolicy(f.ctx, f.third, []authority.Rule{{Pattern: "*"}, {Pattern: "livestreams.view", Deny: true}}, nil); e != nil {
		t.Fatal(e)
	}
	streamDue(t, f, s.ID)
	var state string
	if e = f.db.QueryRow(`SELECT state FROM owned_livestream_admissions WHERE identity=$1`, viewer.Identity).Scan(&state); e != nil || state != "removed" {
		t.Fatal(state, e)
	}
	if _, e = f.repo.Token(f.viewerCtx, f.third, s.ID, false, false); !errors.Is(e, authority.ErrForbidden) {
		t.Fatal("withdrawn view grant accepted", e)
	}
	if _, e = f.accounts.ChangeMember(f.ownerCtx, f.owner, f.pub.ID, accounts.MemberChange{Action: accounts.RemoveMember, AccountID: f.other}); e != nil {
		t.Fatal(e)
	}
	streamDue(t, f, s.ID)
	if e = f.db.QueryRow(`SELECT operation_state FROM owned_livestreams WHERE id=$1`, s.ID).Scan(&state); e != nil || state != "ready" {
		t.Fatal("publisher revocation did not end stream", state, e)
	}
	g := newStreamFixture(t)
	owned := streamCreate(t, g, "owner-version")
	if _, e = g.repo.Start(g.editorCtx, g.other, owned.ID, live.StartRequest{NoIngress: &yes}); e != nil {
		t.Fatal(e)
	}
	query(t, g.db, `UPDATE owned_authority_accounts SET version=version+1 WHERE id=$1`, g.owner)
	streamDue(t, g, owned.ID)
	if e = g.db.QueryRow(`SELECT operation_state FROM owned_livestreams WHERE id=$1`, owned.ID).Scan(&state); e != nil || state != "ready" {
		t.Fatal("owner version did not revoke existing ingress authority", state, e)
	}
}

func TestPGLiveStreamUnknownIngressOutcomeFailsClosed(t *testing.T) {
	f := newStreamFixture(t)
	s := streamCreate(t, f, "unknown-provider-outcome")
	f.provider.failIngressBeforeCreate = true
	if _, e := f.repo.Start(f.ownerCtx, f.owner, s.ID, live.StartRequest{}); !errors.Is(e, live.ErrProvider) {
		t.Fatal(e)
	}
	f.provider.failIngressBeforeCreate = false
	if out, e := f.repo.Start(f.ownerCtx, f.owner, s.ID, live.StartRequest{}); !errors.Is(e, live.ErrConflict) || out.StreamKey != nil || f.provider.ingressCreates != 1 {
		t.Fatal("uncertain create blindly retried", e, f.provider.ingressCreates)
	}
	if e := f.repo.End(f.ownerCtx, f.owner, s.ID, false); !errors.Is(e, live.ErrConflict) {
		t.Fatal("unobserved ingress falsely acknowledged", e)
	}
	var phase string
	if e := f.db.QueryRow(`SELECT phase FROM owned_livestream_runs WHERE stream_id=$1`, s.ID).Scan(&phase); e != nil || phase != "ending" {
		t.Fatal(phase, e)
	}
}

func TestPGLiveStreamConcurrentIdempotencyAndProviderRollback(t *testing.T) {
	f := newStreamFixture(t)
	var wg sync.WaitGroup
	ids := make(chan string, 6)
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			title := "Synthetic concurrent"
			v, e := f.repo.Create(f.ownerCtx, f.owner, f.pub.Name, "concurrent", live.Patch{Title: &title})
			ids <- v.ID
			errs <- e
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	id := ""
	for got := range ids {
		if id == "" {
			id = got
		}
		if got != id {
			t.Fatal("duplicate idempotent streams")
		}
	}
	constraint := "synthetic_live_fail_" + strings.ReplaceAll(f.owner, "-", "")
	query(t, f.db, `ALTER TABLE owned_livestream_audit ADD CONSTRAINT `+constraint+` CHECK(NOT(actor_id='`+f.owner+`'::uuid AND action='livestreams.start'))NOT VALID`)
	t.Cleanup(func() { _, _ = f.db.Exec(`ALTER TABLE owned_livestream_audit DROP CONSTRAINT IF EXISTS ` + constraint) })
	if _, e := f.repo.Start(f.ownerCtx, f.owner, id, live.StartRequest{}); e == nil {
		t.Fatal("injected activation commit succeeded")
	}
	query(t, f.db, `ALTER TABLE owned_livestream_audit DROP CONSTRAINT `+constraint)
	if _, e := f.repo.Start(f.ownerCtx, f.owner, id, live.StartRequest{}); e != nil || f.provider.ingressCreates != 1 {
		t.Fatal("provider effect did not reconcile after rollback", e, f.provider.ingressCreates)
	}
	if e := f.repo.End(f.ownerCtx, f.owner, id, true); e != nil {
		t.Fatal(e)
	}
}

func TestPGLiveStreamCanonicalThumbnailAndUnconfiguredProvider(t *testing.T) {
	f := newStreamFixture(t)
	body := "Synthetic image bytes"
	upload := media.Upload{Name: "synthetic.png", MIMEType: "image/png", Size: int64(len(body)), SHA256: mediaDigest(body), IdempotencyKey: "thumbnail", Public: true}
	file, _, e := f.media.Upload(f.ownerCtx, f.owner, upload, strings.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	s := streamCreate(t, f, "thumbnail")
	s, e = f.repo.Thumbnail(f.ownerCtx, f.owner, s.ID, &file.ID)
	if e != nil || s.Thumbnail == nil || s.Thumbnail.SHA256 != mediaDigest(body) || s.Thumbnail.Size != int64(len(body)) {
		t.Fatal(s, e)
	}
	if _, e = f.repo.Thumbnail(f.editorCtx, f.other, s.ID, &file.ID); !errors.Is(e, live.ErrForbidden) {
		t.Fatal("editor linked another actor's file", e)
	}
	if _, e = f.media.Delete(f.ownerCtx, f.owner, file.ID); e != nil {
		t.Fatal(e)
	}
	s, e = f.repo.Get(f.ctx, "", s.ID)
	if e != nil || s.Thumbnail != nil {
		t.Fatal("stale public thumbnail survived deletion", e)
	}
	closed, e := livestreampg.New(f.db, f.guard, f.accounts, f.media, f.social, nil, streamConfig())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = closed.Create(f.ownerCtx, f.owner, f.pub.Name, "unconfigured", live.Patch{}); !errors.Is(e, live.ErrUnconfigured) {
		t.Fatal(e)
	}
}

func TestPGLiveStreamDisableDuringLockWait(t *testing.T) {
	f := newStreamFixture(t)
	s := streamCreate(t, f, "disable")
	base := atomicAuthorityFixture{db: f.db, ctx: f.ctx, actor: f.owner}
	blocker, pid := atomicBlocker(t, base, "owned_authority_accounts", f.owner)
	if _, e := blocker.ExecContext(f.ctx, `UPDATE owned_authority_accounts SET disabled=true,version=version+1 WHERE id=$1`, f.owner); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := f.repo.Start(f.ownerCtx, f.owner, s.ID, live.StartRequest{}); done <- e }()
	waitAtomicBlocked(t, base, pid, "owned_authority_accounts")
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; !errors.Is(e, authority.ErrDisabled) && !errors.Is(e, authority.ErrStale) {
		t.Fatal(e)
	}
	if f.provider.ingressCreates != 0 || f.provider.created != 0 {
		t.Fatal("provider called after disabled account wait")
	}
}

func TestPGLiveStreamParentExpiryAndIdleCleanup(t *testing.T) {
	f := newStreamFixture(t)
	parentPair := pgIssue(t, f.issuer, f.owner)
	parent, e := f.verifier.Authenticate(f.ctx, parentPair.Token)
	if e != nil {
		t.Fatal(e)
	}
	child, e := f.verifier.Authenticate(f.ctx, f.token)
	if e != nil {
		t.Fatal(e)
	}
	s := streamCreate(t, f, "parent")
	streamNative(t, f, s.ID)
	// Trusted synthetic legacy-parent binding on an already recorded admission;
	// no new stream/token is authorized through this eligibility-only seam.
	query(t, f.db, `UPDATE owned_authority_sessions SET parent_id=$2 WHERE id=$1`, child.Session.ID, parent.Session.ID)
	query(t, f.db, `UPDATE owned_authority_sessions SET expires_at=clock_timestamp()-interval '1 microsecond' WHERE id=$1`, parent.Session.ID)
	streamDue(t, f, s.ID)
	var state string
	if e = f.db.QueryRow(`SELECT operation_state FROM owned_livestreams WHERE id=$1`, s.ID).Scan(&state); e != nil || state != "ready" {
		t.Fatal(state, e)
	}
	query(t, f.db, `UPDATE owned_authority_sessions SET parent_id=NULL WHERE id=$1`, child.Session.ID)
	idle := streamCreate(t, f, "idle")
	if _, e = f.repo.Start(f.ownerCtx, f.owner, idle.ID, live.StartRequest{}); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE owned_livestream_runs SET created_at=clock_timestamp()-interval '6 minutes' WHERE stream_id=$1`, idle.ID)
	query(t, f.db, `UPDATE owned_livestreams SET started_at=clock_timestamp()-interval '6 minutes' WHERE id=$1`, idle.ID)
	streamDue(t, f, idle.ID)
	if e = f.db.QueryRow(`SELECT operation_state FROM owned_livestreams WHERE id=$1`, idle.ID).Scan(&state); e != nil || state != "ready" {
		t.Fatal("idle ingress cleanup", state, e)
	}
}

func TestPGLiveStreamCleanupRestartPersistence(t *testing.T) {
	database(t)
	path := filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "synthetic-livestream-restart.json")
	type savedState struct {
		ID, Endpoint string
		Ingress      live.Ingress
	}
	var saved savedState
	if os.Getenv("PG_TEST_PHASE") == "before" {
		f := newStreamFixture(t)
		s := streamCreate(t, f, "restart")
		if _, e := f.repo.Start(f.ownerCtx, f.owner, s.ID, live.StartRequest{}); e != nil {
			t.Fatal(e)
		}
		saved.ID = s.ID
		saved.Endpoint = f.provider.Endpoint()
		for _, ing := range f.provider.ingresses {
			saved.Ingress = ing
		}
		saved.Ingress.Key = ""
		f.provider.failIngressDelete = true
		if e := f.repo.End(f.ownerCtx, f.owner, s.ID, false); !errors.Is(e, live.ErrProvider) {
			t.Fatal(e)
		}
		raw, _ := json.Marshal(saved)
		if e := os.WriteFile(path, raw, 0600); e != nil {
			t.Fatal(e)
		}
		return
	}
	if os.Getenv("PG_TEST_PHASE") != "after" {
		t.Fatal("PG_TEST_PHASE must be before or after")
	}
	raw, e := os.ReadFile(path)
	if e != nil || json.Unmarshal(raw, &saved) != nil {
		t.Fatal("missing synthetic restart state", e)
	}
	f := newStreamFixture(t)
	f.provider.endpoint = saved.Endpoint
	saved.Ingress.Key = "synthetic-only"
	f.provider.ingresses = map[string]live.Ingress{saved.Ingress.ID: saved.Ingress}
	query(t, f.callFixture.db, `UPDATE owned_livestream_runs SET next_attempt_at=clock_timestamp()-interval '1 microsecond' WHERE stream_id=$1`, saved.ID)
	if _, e = f.repo.ProcessDue(f.ctx, 100); e != nil {
		t.Fatal(e)
	}
	var phase string
	if e = f.callFixture.db.QueryRow(`SELECT phase FROM owned_livestream_runs WHERE stream_id=$1`, saved.ID).Scan(&phase); e != nil || phase != "done" {
		t.Fatal("restart cleanup did not complete", phase, e)
	}
	v, e := f.repo.Get(f.ctx, "", saved.ID)
	if e != nil || v.OperationState != "ready" || v.Status != live.Ended || v.IngressStreamKey != nil {
		t.Fatal(v, e)
	}
}
