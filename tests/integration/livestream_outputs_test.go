// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/livestreampg"
	"solsynth.local/go-migration/internal/mediastore"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/social"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
)

type streamOutputProvider struct {
	*streamProvider
	outMu                                        sync.Mutex
	outputs                                      map[string]live.ProviderOutput
	starts, stops                                int
	failBefore, failAfter, failStop, pendingStop bool
	policy                                       string
}

func (*streamOutputProvider) OutputContract() string { return live.EgressContract }
func (p *streamOutputProvider) OutputPolicyFingerprint(ids []string, kind string) ([]byte, error) {
	p.outMu.Lock()
	defer p.outMu.Unlock()
	if len(ids) != 1 || kind == "hls" && ids[0] != "synthetic" || kind == "egress" && ids[0] != "relay" && ids[0] != "archive" || kind != "hls" && kind != "egress" {
		return nil, live.ErrInvalid
	}
	h := sha256.Sum256([]byte(kind + ":" + ids[0] + ":" + p.policy))
	return h[:], nil
}
func (p *streamOutputProvider) OutputUsesRelay(ids []string, kind string) (bool, error) {
	if _, e := p.OutputPolicyFingerprint(ids, kind); e != nil {
		return false, e
	}
	return ids[0] == "relay", nil
}
func (p *streamOutputProvider) StartOutput(_ context.Context, v live.OutputStart) (live.ProviderOutput, error) {
	p.outMu.Lock()
	defer p.outMu.Unlock()
	p.starts++
	if p.failBefore {
		return live.ProviderOutput{}, live.ErrProvider
	}
	out := live.ProviderOutput{ID: fmt.Sprintf("synthetic-output-%d", p.starts), Room: v.Room, Status: 1}
	if v.Kind == "hls" {
		out.Playlist = "streaming/" + v.Destinations[0] + "/" + v.ID + "/" + v.Playlist
	}
	p.outputs[out.ID] = out
	if p.failAfter {
		return live.ProviderOutput{}, live.ErrProvider
	}
	return out, nil
}
func (p *streamOutputProvider) ListOutputs(_ context.Context, room string) ([]live.ProviderOutput, error) {
	p.outMu.Lock()
	defer p.outMu.Unlock()
	out := []live.ProviderOutput{}
	for _, v := range p.outputs {
		if v.Room == room {
			out = append(out, v)
		}
	}
	return out, nil
}
func (p *streamOutputProvider) StopOutput(_ context.Context, room, id string) (live.ProviderOutput, error) {
	p.outMu.Lock()
	defer p.outMu.Unlock()
	p.stops++
	if p.failStop {
		return live.ProviderOutput{}, live.ErrProvider
	}
	v, ok := p.outputs[id]
	if !ok {
		return live.ProviderOutput{ID: id, Room: room, Absent: true}, nil
	}
	if p.pendingStop {
		v.Status = 2
		p.outputs[id] = v
		return v, nil
	}
	delete(p.outputs, id)
	v.Status = 3
	return v, nil
}

type outputFixture struct {
	streamFixture
	out   *streamOutputProvider
	store *mediastore.Filesystem
	root  string
}

func outputConfig() livestreampg.OutputConfig {
	return livestreampg.OutputConfig{MaxOutputsPerStream: 20, MaxSegments: 10, MaxPlaylistBytes: 64 << 10, MaxSegmentBytes: 4 << 20, OperationTimeout: time.Second, HLSNamespaces: []string{"synthetic"}}
}
func newOutputFixture(t *testing.T) outputFixture {
	t.Helper()
	f := outputFixture{streamFixture: newStreamFixture(t)}
	query(t, f.db, livestreampg.OutputsSchema)
	query(t, f.db, `UPDATE owned_authority_accounts SET is_superuser=true WHERE id=$1::uuid`, f.owner)
	p, e := f.verifier.Authenticate(f.ctx, f.token)
	if e != nil {
		t.Fatal(e)
	}
	f.ownerCtx = f.guard.Bind(f.ctx, p)
	f.root = t.TempDir()
	if e = os.Chmod(f.root, 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.MkdirAll(filepath.Join(f.root, "streaming", "synthetic"), 0700); e != nil {
		t.Fatal(e)
	}
	f.store, e = mediastore.NewFilesystem(f.root, 4<<20)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { f.store.Close() })
	f.out = &streamOutputProvider{streamProvider: f.provider, outputs: map[string]live.ProviderOutput{}, policy: "synthetic-v1"}
	f.repo, e = livestreampg.New(f.db, f.guard, f.accounts, f.media, f.social, f.out, streamConfig())
	if e != nil {
		t.Fatal(e)
	}
	if e = f.repo.WithOutputs(outputConfig(), f.store); e != nil {
		t.Fatal(e)
	}
	f.handler = (&live.Service{Repository: f.repo, Authority: f.verifier, Bind: f.guard.Bind}).Handler()
	if e = f.repo.Ready(f.ctx); e != nil {
		t.Fatal(e)
	}
	return f
}
func outputStream(t *testing.T, f outputFixture, key string) live.Stream {
	t.Helper()
	s := streamCreate(t, f.streamFixture, key)
	streamNative(t, f.streamFixture, s.ID)
	return s
}
func hlsStart(t *testing.T, f outputFixture, id, key string) live.OutputResponse {
	t.Helper()
	v, e := f.repo.StartHLS(f.ownerCtx, f.owner, id, live.HLSRequest{Key: key, DestinationID: "synthetic"})
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func outputPhase(t *testing.T, f outputFixture, id string) string {
	t.Helper()
	var phase string
	if e := f.db.QueryRow(`SELECT phase FROM owned_livestream_outputs WHERE id=$1::uuid`, id).Scan(&phase); e != nil {
		t.Fatal(e)
	}
	return phase
}
func outputDue(t *testing.T, f outputFixture, sid string) error {
	t.Helper()
	query(t, f.db, `UPDATE owned_livestream_outputs SET next_attempt_at=clock_timestamp()-interval '1 microsecond' WHERE stream_id=$1::uuid AND phase<>'done'`, sid)
	_, e := f.repo.ProcessOutputsDue(f.ctx, 100)
	return e
}
func outputFiles(t *testing.T, f outputFixture, id string) {
	t.Helper()
	dir := filepath.Join(f.root, "streaming", "synthetic", id)
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	playlist := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:6\n#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:6,\nsegment_0.ts\n"
	if e := os.WriteFile(filepath.Join(dir, "playlist.m3u8"), []byte(playlist), 0600); e != nil {
		t.Fatal(e)
	}
	segment := make([]byte, 188)
	segment[0] = 0x47
	if e := os.WriteFile(filepath.Join(dir, "segment_0.ts"), segment, 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "unreferenced.ts"), segment, 0600); e != nil {
		t.Fatal(e)
	}
}

func TestPGLiveStreamHLSHTTPPlaybackAndCurrentPrivacy(t *testing.T) {
	f := newOutputFixture(t)
	s := outputStream(t, f, "hls-http")
	base := "/api/livestreams/" + s.ID
	req := fixtureJSONRequest(t, "POST", base+"/hls", f.token, map[string]any{"destination_id": "synthetic"})
	req.Header.Set("Idempotency-Key", "synthetic-hls-http")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	v := backendDecode[live.OutputResponse](t, w)
	if v.EgressID == "" || v.State != "active" || v.PlaylistURL == nil {
		t.Fatal(v)
	}
	outputFiles(t, f, v.OutputID)
	w = streamGetCall(t, f.streamFixture, *v.PlaylistURL, f.thirdToken, 200)
	if w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" || strings.Contains(w.Body.String(), "streaming/synthetic") || !strings.Contains(w.Body.String(), v.OutputID+"/segment_0.ts") {
		t.Fatal("unsafe playlist projection")
	}
	segment := base + "/hls/" + v.OutputID + "/segment_0.ts"
	w = streamGetCall(t, f.streamFixture, segment, f.thirdToken, 200)
	if w.Body.Len() != 188 || w.Header().Get("Content-Type") != "video/mp2t" {
		t.Fatal("segment framing")
	}
	streamGetCall(t, f.streamFixture, *v.PlaylistURL, "", 401)
	streamGetCall(t, f.streamFixture, base+"/hls/"+v.OutputID+"/unreferenced.ts", f.token, 404)
	if _, e := f.social.ChangeRelationship(f.viewerCtx, f.third, f.owner, social.RelationshipChange{Action: social.Block}); e != nil {
		t.Fatal(e)
	}
	streamGetCall(t, f.streamFixture, segment, f.thirdToken, 403)
	if _, e := f.repo.OpenHLS(f.ownerCtx, f.owner, s.ID, v.OutputID, "segment_0.ts"); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(f.root, "streaming", "synthetic", v.OutputID, "playlist.m3u8"), []byte("#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"https://external.invalid/key\"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	streamGetCall(t, f.streamFixture, *v.PlaylistURL, f.token, 503)
	if e := f.repo.StopOutput(f.ownerCtx, f.owner, s.ID, "hls"); e != nil {
		t.Fatal(e)
	}
	streamGetCall(t, f.streamFixture, *v.PlaylistURL, f.token, 404)
}

func TestPGLiveStreamOutputsRequireFixedDestinationsAndAdminEditor(t *testing.T) {
	f := newOutputFixture(t)
	s := outputStream(t, f, "output-policy")
	if _, e := f.repo.StartHLS(f.editorCtx, f.other, s.ID, live.HLSRequest{DestinationID: "synthetic"}); !errors.Is(e, live.ErrForbidden) {
		t.Fatal("non-admin editor started egress", e)
	}
	query(t, f.db, `UPDATE owned_authority_accounts SET is_superuser=true WHERE id=$1::uuid`, f.third)
	p, e := f.verifier.Authenticate(f.ctx, f.thirdToken)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.StartHLS(f.guard.Bind(f.ctx, p), f.third, s.ID, live.HLSRequest{DestinationID: "synthetic"}); e == nil {
		t.Fatal("non-editor admin started egress")
	}
	path := "/api/livestreams/" + s.ID
	backendJSONCall(t, f.handler, "POST", path+"/egress", f.token, map[string]any{"rtmp_urls": []string{"rtmps://external.invalid/live/key"}}, 400)
	backendJSONCall(t, f.handler, "POST", path+"/egress", f.token, map[string]any{"file_path": "/tmp/out.mp4"}, 400)
	backendJSONCall(t, f.handler, "POST", path+"/hls", f.token, map[string]any{"destination_id": "synthetic", "playlist_name": "../bad.m3u8"}, 400)
	backendJSONCall(t, f.handler, "POST", path+"/hls", f.token, map[string]any{"destination_id": "synthetic", "segment_count": 4}, 400)
	backendJSONCall(t, f.handler, "POST", path+"/hls", f.token, map[string]any{"destination_id": "synthetic", "hls_base_url": "https://external.invalid"}, 400)
	if f.out.starts != 0 {
		t.Fatal("rejected output reached provider")
	}
	private := streamCreate(t, f.streamFixture, "private-output")
	visibility := live.Private
	if _, e = f.repo.Update(f.ownerCtx, f.owner, private.ID, live.Patch{Visibility: &visibility}); e != nil {
		t.Fatal(e)
	}
	streamNative(t, f.streamFixture, private.ID)
	if _, e = f.repo.StartEgress(f.ownerCtx, f.owner, private.ID, live.EgressRequest{DestinationIDs: []string{"relay"}}); !errors.Is(e, live.ErrForbidden) {
		t.Fatal("private stream relayed externally", e)
	}
	v := hlsStart(t, f, private.ID, "private-hls")
	outputFiles(t, f, v.OutputID)
	streamGetCall(t, f.streamFixture, *v.PlaylistURL, f.otherToken, 200)
	streamGetCall(t, f.streamFixture, *v.PlaylistURL, f.thirdToken, 404)
	fixturePublisherShadowban(t, f.db, f.pub.ID, "other", time.Now().UTC())
	if _, e = f.repo.StartEgress(f.ownerCtx, f.owner, s.ID, live.EgressRequest{DestinationIDs: []string{"relay"}}); !errors.Is(e, live.ErrForbidden) {
		t.Fatal("editor exception bypassed public relay visibility", e)
	}
}

func TestPGLiveStreamOutputsUncertainStartNeverRecreates(t *testing.T) {
	f := newOutputFixture(t)
	s := outputStream(t, f, "uncertain-hls")
	f.out.failAfter = true
	q := live.HLSRequest{Key: "uncertain", DestinationID: "synthetic"}
	if _, e := f.repo.StartHLS(f.ownerCtx, f.owner, s.ID, q); !errors.Is(e, live.ErrProvider) {
		t.Fatal(e)
	}
	if _, e := f.repo.StartHLS(f.ownerCtx, f.owner, s.ID, q); !errors.Is(e, live.ErrOutputUncertain) {
		t.Fatal(e)
	}
	if f.out.starts != 1 {
		t.Fatal("uncertain HLS was reissued")
	}
	if e := outputDue(t, f, s.ID); e != nil {
		t.Fatal(e)
	}
	v, e := f.repo.GetOutput(f.ownerCtx, f.owner, s.ID, "hls")
	if e != nil || v.State != "active" {
		t.Fatal(v, e)
	}
	if e = f.repo.StopOutput(f.ownerCtx, f.owner, s.ID, "hls"); e != nil {
		t.Fatal(e)
	}
	r := outputStream(t, f, "uncertain-rtmp")
	request := live.EgressRequest{Key: "rtmp-uncertain", DestinationIDs: []string{"relay"}}
	if _, e = f.repo.StartEgress(f.ownerCtx, f.owner, r.ID, request); !errors.Is(e, live.ErrProvider) {
		t.Fatal(e)
	}
	if e = outputDue(t, f, r.ID); e != nil {
		t.Fatal(e)
	}
	v, e = f.repo.GetOutput(f.ownerCtx, f.owner, r.ID, "egress")
	if e != nil || v.State != "uncertain" || v.EgressID != "" {
		t.Fatal("unproven RTMP association", v, e)
	}
	if e = f.repo.StopOutput(f.ownerCtx, f.owner, r.ID, "egress"); !errors.Is(e, live.ErrOutputUncertain) {
		t.Fatal("unknown outcome falsely stopped", e)
	}
	if _, e = f.repo.StartEgress(f.ownerCtx, f.owner, r.ID, request); !errors.Is(e, live.ErrOutputUncertain) {
		t.Fatal(e)
	}
	if f.out.starts != 2 {
		t.Fatal("unknown RTMP was retried")
	}
}

func TestPGLiveStreamOutputsActivationRollbackRetainsCleanup(t *testing.T) {
	f := newOutputFixture(t)
	s := outputStream(t, f, "output-rollback")
	constraint := "synthetic_output_fail_" + strings.ReplaceAll(f.owner, "-", "")
	query(t, f.db, `ALTER TABLE owned_livestream_audit ADD CONSTRAINT `+constraint+` CHECK(NOT(actor_id='`+f.owner+`'::uuid AND action='livestreams.hls.observed'))NOT VALID`)
	t.Cleanup(func() { _, _ = f.db.Exec(`ALTER TABLE owned_livestream_audit DROP CONSTRAINT IF EXISTS ` + constraint) })
	if _, e := f.repo.StartHLS(f.ownerCtx, f.owner, s.ID, live.HLSRequest{Key: "rollback", DestinationID: "synthetic"}); e == nil {
		t.Fatal("activation audit failure committed")
	}
	v, e := f.repo.GetOutput(f.ownerCtx, f.owner, s.ID, "hls")
	if e != nil || v.State != "stopping" || v.EgressID == "" {
		t.Fatal("observed provider ID lost on rollback", v, e)
	}
	if f.out.starts != 1 {
		t.Fatal(f.out.starts)
	}
	query(t, f.db, `ALTER TABLE owned_livestream_audit DROP CONSTRAINT `+constraint)
	if e = outputDue(t, f, s.ID); e != nil {
		t.Fatal(e)
	}
	if outputPhase(t, f, v.OutputID) != "done" || f.out.stops != 1 {
		t.Fatal("retained cleanup did not complete")
	}
}

func TestPGLiveStreamOutputsFreshPolicyAndDurableStopRetry(t *testing.T) {
	f := newOutputFixture(t)
	pair := pgIssue(t, f.issuer, f.owner)
	p, e := f.verifier.Authenticate(f.ctx, pair.Token)
	if e != nil {
		t.Fatal(e)
	}
	f.ownerCtx = f.guard.Bind(f.ctx, p)
	s := outputStream(t, f, "output-revocation")
	v := hlsStart(t, f, s.ID, "fresh-policy")
	rotated, e := f.issuer.Refresh(f.ctx, pair.RefreshToken, 0, "")
	if e != nil {
		t.Fatal(e)
	}
	if e = outputDue(t, f, s.ID); e != nil || outputPhase(t, f, v.OutputID) != "active" {
		t.Fatal("ordinary refresh revoked output", e)
	}
	p, e = f.verifier.Authenticate(f.ctx, rotated.Token)
	if e != nil {
		t.Fatal(e)
	}
	f.ownerCtx = f.guard.Bind(f.ctx, p)
	f.out.failStop = true
	query(t, f.db, `UPDATE owned_authority_accounts SET is_superuser=false WHERE id=$1::uuid`, f.owner)
	if e := outputDue(t, f, s.ID); !errors.Is(e, live.ErrProvider) {
		t.Fatal(e)
	}
	if outputPhase(t, f, v.OutputID) != "stopping" {
		t.Fatal("revoked output was retained as active")
	}
	// Restoring authority cannot revive an admission already pending removal.
	query(t, f.db, `UPDATE owned_authority_accounts SET is_superuser=true WHERE id=$1::uuid`, f.owner)
	f.out.failStop = false
	f.out.pendingStop = true
	if e := outputDue(t, f, s.ID); e != nil {
		t.Fatal(e)
	}
	if outputPhase(t, f, v.OutputID) != "stopping" {
		t.Fatal("provider ENDING was falsely completed")
	}
	f.out.pendingStop = false
	if e := outputDue(t, f, s.ID); e != nil {
		t.Fatal(e)
	}
	if outputPhase(t, f, v.OutputID) != "done" || f.out.starts != 1 {
		t.Fatal("retry restarted recording")
	}
	var phase string
	if e := f.db.QueryRow(`SELECT phase FROM owned_livestream_runs WHERE stream_id=$1::uuid`, s.ID).Scan(&phase); e != nil || phase != "active" {
		t.Fatal("output revocation ended unrelated valid broadcast", phase, e)
	}
	v = hlsStart(t, f, s.ID, "rotated-policy")
	f.out.policy = "synthetic-v2"
	if e := outputDue(t, f, s.ID); e != nil {
		t.Fatal(e)
	}
	if outputPhase(t, f, v.OutputID) != "done" {
		t.Fatal("old destination policy remained admitted")
	}
	relay, e := f.repo.StartEgress(f.ownerCtx, f.owner, s.ID, live.EgressRequest{Key: "shadowban", DestinationIDs: []string{"relay"}})
	if e != nil {
		t.Fatal(e)
	}
	fixturePublisherShadowban(t, f.db, f.pub.ID, "other", time.Now().UTC())
	if e = outputDue(t, f, s.ID); e != nil {
		t.Fatal(e)
	}
	if outputPhase(t, f, relay.OutputID) != "done" {
		t.Fatal("relay ignored canonical public publisher visibility")
	}
}

func TestPGLiveStreamOutputsConcurrentIdempotencyAndCapacity(t *testing.T) {
	f := newOutputFixture(t)
	s := outputStream(t, f, "output-concurrent")
	const n = 6
	results := make(chan live.OutputResponse, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := f.repo.StartHLS(f.ownerCtx, f.owner, s.ID, live.HLSRequest{Key: "concurrent", DestinationID: "synthetic"})
			results <- v
			errs <- e
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil && !errors.Is(e, live.ErrOutputUncertain) {
			t.Fatal(e)
		}
	}
	if f.out.starts != 1 {
		t.Fatal("duplicate remote start", f.out.starts)
	}
	v, e := f.repo.StartHLS(f.ownerCtx, f.owner, s.ID, live.HLSRequest{Key: "concurrent", DestinationID: "synthetic"})
	if e != nil || v.State != "active" {
		t.Fatal(v, e)
	}
	for result := range results {
		if result.OutputID != "" && result.OutputID != v.OutputID {
			t.Fatal("duplicate intent")
		}
	}
	if _, e = f.repo.StartHLS(f.ownerCtx, f.owner, s.ID, live.HLSRequest{Key: "different-key", DestinationID: "synthetic"}); !errors.Is(e, live.ErrConflict) {
		t.Fatal("new key silently reused active intent", e)
	}
	if e = f.repo.StopOutput(f.ownerCtx, f.owner, s.ID, "hls"); e != nil {
		t.Fatal(e)
	}
	if replay, e := f.repo.StartHLS(f.ownerCtx, f.owner, s.ID, live.HLSRequest{Key: "concurrent", DestinationID: "synthetic"}); e != nil || replay.OutputID != v.OutputID || replay.State != "done" {
		t.Fatal("completed key created output", replay, e)
	}
	cfg := outputConfig()
	cfg.MaxOutputsPerStream = 1
	if e = f.repo.WithOutputs(cfg, f.store); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.StartHLS(f.ownerCtx, f.owner, s.ID, live.HLSRequest{Key: "capacity", DestinationID: "synthetic"}); !errors.Is(e, live.ErrConflict) {
		t.Fatal("capacity ignored", e)
	}
	opposing := outputStream(t, f, "opposing-editors")
	query(t, f.db, `UPDATE owned_authority_accounts SET is_superuser=true WHERE id=$1::uuid`, f.other)
	p, e := f.verifier.Authenticate(f.ctx, f.otherToken)
	if e != nil {
		t.Fatal(e)
	}
	editor := f.guard.Bind(f.ctx, p)
	race := make(chan error, 2)
	go func() {
		_, e := f.repo.StartHLS(f.ownerCtx, f.owner, opposing.ID, live.HLSRequest{Key: "owner", DestinationID: "synthetic"})
		race <- e
	}()
	go func() {
		_, e := f.repo.StartHLS(editor, f.other, opposing.ID, live.HLSRequest{Key: "editor", DestinationID: "synthetic"})
		race <- e
	}()
	for i := 0; i < 2; i++ {
		e := <-race
		if e != nil && !errors.Is(e, live.ErrConflict) && !errors.Is(e, live.ErrOutputUncertain) {
			t.Fatal("opposing editor lock order", e)
		}
	}
	if f.out.starts != 2 {
		t.Fatal("opposing editors duplicated remote start", f.out.starts)
	}
}

func TestPGLiveStreamOutputsDisableDuringAuthorityWait(t *testing.T) {
	f := newOutputFixture(t)
	s := outputStream(t, f, "output-disabled-wait")
	base := atomicAuthorityFixture{db: f.db, ctx: f.ctx, actor: f.owner}
	blocker, pid := atomicBlocker(t, base, "owned_authority_accounts", f.owner)
	if _, e := blocker.ExecContext(f.ctx, `UPDATE owned_authority_accounts SET disabled=true,version=version+1 WHERE id=$1::uuid`, f.owner); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, e := f.repo.StartHLS(f.ownerCtx, f.owner, s.ID, live.HLSRequest{DestinationID: "synthetic"})
		done <- e
	}()
	waitAtomicBlocked(t, base, pid, "owned_authority_accounts")
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; !errors.Is(e, authority.ErrDisabled) && !errors.Is(e, authority.ErrStale) {
		t.Fatal(e)
	}
	if f.out.starts != 0 {
		t.Fatal("disabled actor reached provider")
	}
	if countCalls(t, f.callFixture, `SELECT count(*) FROM owned_livestream_outputs WHERE stream_id=$1::uuid`, s.ID) != 0 {
		t.Fatal("disabled actor left an intent")
	}
}

func TestPGLiveStreamOutputsEndCleanupIndependentAndUnconfigured(t *testing.T) {
	f := newOutputFixture(t)
	s := outputStream(t, f, "output-independent")
	v := hlsStart(t, f, s.ID, "independent")
	f.provider.failDelete = true
	if e := f.repo.End(f.ownerCtx, f.owner, s.ID, false); !errors.Is(e, live.ErrProvider) {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE owned_livestream_runs SET next_attempt_at=clock_timestamp()-interval '1 microsecond' WHERE stream_id=$1::uuid`, s.ID)
	query(t, f.db, `UPDATE owned_livestream_outputs SET next_attempt_at=clock_timestamp()-interval '1 microsecond' WHERE id=$1::uuid`, v.OutputID)
	if _, e := f.repo.ProcessDue(f.ctx, 100); !errors.Is(e, live.ErrProvider) {
		t.Fatal(e)
	}
	if outputPhase(t, f, v.OutputID) != "done" {
		t.Fatal("room cleanup failure hid output stop")
	}
	f.provider.failDelete = false
	streamDue(t, f.streamFixture, s.ID)
	streamNative(t, f.streamFixture, s.ID)
	f.out.failStop = true
	v = hlsStart(t, f, s.ID, "pending-unconfigured")
	if e := f.repo.End(f.ownerCtx, f.owner, s.ID, false); e != nil {
		t.Fatal(e)
	}
	plain, e := livestreampg.New(f.db, f.guard, f.accounts, f.media, f.social, f.provider, streamConfig())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = plain.StartHLS(f.ownerCtx, f.owner, s.ID, live.HLSRequest{DestinationID: "synthetic"}); !errors.Is(e, live.ErrOutputUnavailable) {
		t.Fatal("unconfigured HLS accepted", e)
	}
	yes := true
	if _, e = plain.Start(f.ownerCtx, f.owner, s.ID, live.StartRequest{NoIngress: &yes}); !errors.Is(e, live.ErrConflict) {
		t.Fatal("disabled output module bypassed prior cleanup obligation", e)
	}
	privacy := live.Private
	if _, e = plain.Update(f.ownerCtx, f.owner, s.ID, live.Patch{Visibility: &privacy}); !errors.Is(e, live.ErrConflict) {
		t.Fatal("privacy changed before prior output was closed", e)
	}
	f.out.failStop = false
	if e = outputDue(t, f, s.ID); e != nil {
		t.Fatal(e)
	}
	if outputPhase(t, f, v.OutputID) != "done" {
		t.Fatal("pending output not discharged")
	}
}

func TestPGLiveStreamOutputsRestartPersistence(t *testing.T) {
	database(t)
	path := filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "synthetic-livestream-output-restart.json")
	type state struct {
		Stream, Output, Endpoint string
		Provider                 live.ProviderOutput
	}
	var saved state
	if os.Getenv("PG_TEST_PHASE") == "before" {
		f := newOutputFixture(t)
		s := outputStream(t, f, "outputs-restart")
		v := hlsStart(t, f, s.ID, "restart")
		f.out.failStop = true
		if e := f.repo.StopOutput(f.ownerCtx, f.owner, s.ID, "hls"); !errors.Is(e, live.ErrProvider) {
			t.Fatal(e)
		}
		saved = state{Stream: s.ID, Output: v.OutputID, Endpoint: f.provider.Endpoint(), Provider: f.out.outputs[v.EgressID]}
		raw, e := json.Marshal(saved)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, raw, 0600); e != nil {
			t.Fatal(e)
		}
		return
	}
	if os.Getenv("PG_TEST_PHASE") != "after" {
		t.Fatal("PG_TEST_PHASE must be before or after")
	}
	raw, e := os.ReadFile(path)
	if e != nil || json.Unmarshal(raw, &saved) != nil {
		t.Fatal("missing synthetic output restart state", e)
	}
	f := newOutputFixture(t)
	f.provider.endpoint = saved.Endpoint
	f.out.outputs[saved.Provider.ID] = saved.Provider
	if outputPhase(t, f, saved.Output) != "stopping" {
		t.Fatal("cleanup intent did not survive restart")
	}
	if e = outputDue(t, f, saved.Stream); e != nil {
		t.Fatal(e)
	}
	if outputPhase(t, f, saved.Output) != "done" || f.out.starts != 0 || f.out.stops != 1 {
		t.Fatal("restart lost cleanup or recreated output")
	}
	// Existing recorded admissions use the split durable generation after restart.
	if e = authoritypg.New(f.db).ContinuityReady(f.ctx); e != nil {
		t.Fatal(e)
	}
}
