// SPDX-License-Identifier: AGPL-3.0-only
package livestreams

import (
	"context"
	"net/http"
	"net/http/httptest"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"testing"
	"time"
)

const actor = "a1000000-0000-4000-8000-000000000001"
const stream = "b1000000-0000-4000-8000-000000000001"
const output = "c1000000-0000-4000-8000-000000000001"
const purchase = "d1000000-0000-4000-8000-000000000001"

type fakeAuthority struct{}

func (fakeAuthority) AuthenticateRequest(r *http.Request) (authority.Principal, error) {
	if r.Header.Get("Authorization") != "Bearer synthetic" {
		return authority.Principal{}, authority.ErrInvalid
	}
	return authority.Principal{Account: authority.Account{ID: actor}}, nil
}
func (fakeAuthority) AuthenticatePrincipal(_ context.Context, p authority.Principal) (authority.Principal, error) {
	return p, nil
}
func (fakeAuthority) AuthorizePrincipal(_ context.Context, p authority.Principal, _ ...string) (authority.Principal, error) {
	return p, nil
}

type fakeRepo struct {
	Repository
	called     int
	publisher  string
	publishing bool
	who        string
}

func (r *fakeRepo) Create(_ context.Context, who, pub, key string, p Patch) (Stream, error) {
	r.called++
	r.publisher = pub
	r.who = who
	return Stream{ID: stream}, nil
}
func (r *fakeRepo) Token(_ context.Context, who, id string, publish, tool bool) (TokenResponse, error) {
	r.called++
	r.publishing = publish
	r.who = who
	return TokenResponse{Identity: "opaque"}, nil
}
func request(h http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestLiveHTTPStrictInputAndIdentity(t *testing.T) {
	repo := &fakeRepo{}
	h := (&Service{Repository: repo, Authority: fakeAuthority{}}).Handler()
	for _, body := range []string{`null`, `{"Title":"wrong case"}`, `{"title":"a","title":"b"}`, `{"metadata":{"x":1,"x":2}}`, `{"title":"a"} {}`, `{"ingress_id":"supplied"}`} {
		if w := request(h, "POST", "/api/livestreams", body, "Bearer synthetic"); w.Code != 400 || repo.called != 0 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w := request(h, "POST", "/api/livestreams?pub=synthetic-publisher", `{"title":"Synthetic"}`, "Bearer synthetic"); w.Code != 200 || repo.publisher != "synthetic-publisher" || repo.who != actor || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w, repo)
	}
	before := repo.called
	for _, query := range []string{"streamer=1", "streamer=true&streamer=false", "identity=someone", "token=secret"} {
		if w := request(h, "GET", "/api/livestreams/"+stream+"/token?"+query, "", "Bearer synthetic"); w.Code != 400 || repo.called != before {
			t.Fatal(w.Code, query)
		}
	}
	if w := request(h, "GET", "/api/livestreams/"+stream+"/token", "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := request(h, "GET", "/api/livestreams/"+stream+"/token?streamer=false", "", "Bearer synthetic"); w.Code != 200 || repo.publishing {
		t.Fatal(w, repo)
	}
}
func TestLiveHTTPRejectsSpoofedActorAndUnsupportedEffects(t *testing.T) {
	repo := &fakeRepo{}
	h := (&Service{Repository: repo, Authority: fakeAuthority{}}).Handler()
	r := httptest.NewRequest("POST", "/api/livestreams", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer synthetic")
	r.Header.Set("X-Account-ID", actor)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 || repo.called != 0 {
		t.Fatal(w)
	}
	for _, route := range []string{"awards", "chat", "egress", "hls"} {
		status := 501
		if route == "egress" || route == "hls" {
			status = 503
		}
		if w = request(h, "POST", "/api/livestreams/"+stream+"/"+route, `{}`, "Bearer synthetic"); w.Code != status || repo.called != 0 {
			t.Fatal(w)
		}
	}
}

type featureRepo struct {
	*fakeRepo
	archiveKey, archiveOutput, refundReason, operationKind string
	downloading                                            bool
}

func (r *featureRepo) GetOutput(context.Context, string, string, string) (OutputResponse, error) {
	return OutputResponse{}, nil
}
func (r *featureRepo) StartEgress(context.Context, string, string, EgressRequest) (OutputResponse, error) {
	return OutputResponse{}, nil
}
func (r *featureRepo) StartHLS(context.Context, string, string, HLSRequest) (OutputResponse, error) {
	return OutputResponse{}, nil
}
func (r *featureRepo) StopOutput(context.Context, string, string, string) error { return nil }
func (r *featureRepo) OpenHLS(_ context.Context, who, sid, oid, asset string) (Playback, error) {
	if who != actor || sid != stream || oid != output || asset != "index.m3u8" {
		return Playback{}, ErrInvalid
	}
	return Playback{Body: []byte("archive"), MIME: "application/vnd.apple.mpegurl", ModifiedAt: time.Unix(1, 0)}, nil
}
func (r *featureRepo) RequestArchiveImport(_ context.Context, who, sid, oid, key string) (ArchiveImport, error) {
	if who != actor || sid != stream {
		return ArchiveImport{}, ErrInvalid
	}
	r.archiveKey, r.archiveOutput = key, oid
	return ArchiveImport{ID: purchase, StreamID: sid, OutputID: oid, State: "pending"}, nil
}
func (r *featureRepo) GetArchiveImport(context.Context, string, string, string, string) (ArchiveImport, error) {
	return ArchiveImport{}, nil
}
func (r *featureRepo) CreateAwardOrder(context.Context, string, string, string, AwardRequest) (AwardOrderResponse, error) {
	return AwardOrderResponse{}, nil
}
func (r *featureRepo) ListAwards(context.Context, string, string, int, int, bool) ([]Award, int, error) {
	return []Award{}, 0, nil
}
func (r *featureRepo) AwardLeaderboard(context.Context, string, string, int) ([]AwardLeader, error) {
	return []AwardLeader{}, nil
}
func (r *featureRepo) RequestAwardRefund(_ context.Context, who, sid, id string, q AwardRefundRequest) (AwardRefund, error) {
	if who != actor || sid != stream || id != purchase {
		return AwardRefund{}, ErrInvalid
	}
	r.refundReason = q.Reason
	return AwardRefund{PurchaseID: id, State: "refund_pending", Reason: q.Reason}, nil
}
func (r *featureRepo) InspectOperations(_ context.Context, who, kind string, limit, offset int) ([]Operation, int, error) {
	if who != actor || limit != 7 || offset != 2 {
		return nil, 0, ErrInvalid
	}
	r.operationKind = kind
	return []Operation{{Kind: kind, ID: output, StreamID: stream, State: "pending"}}, 1, nil
}
func (r *featureRepo) RedriveOperation(_ context.Context, who, kind, id string) (Operation, error) {
	if who != actor || kind != "archive" || id != output {
		return Operation{}, ErrInvalid
	}
	r.operationKind = kind
	return Operation{Kind: kind, ID: id, StreamID: stream, State: "pending"}, nil
}

func TestLiveHTTPOperationsArchiveAndRefundContracts(t *testing.T) {
	repo := &featureRepo{fakeRepo: &fakeRepo{}}
	h := (&Service{Repository: repo, Authority: fakeAuthority{}}).Handler()

	r := httptest.NewRequest("POST", "/api/livestreams/"+stream+"/hls/"+output+"/import", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer synthetic")
	r.Header.Set("Idempotency-Key", "archive-one")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || repo.archiveKey != "archive-one" || repo.archiveOutput != output {
		t.Fatal(w.Code, w.Body.String(), repo.archiveKey, repo.archiveOutput)
	}

	w = request(h, "GET", "/api/livestreams/"+stream+"/hls/"+output+"/index.m3u8?download=true", "", "Bearer synthetic")
	if w.Code != 200 || w.Body.String() != "archive" || w.Header().Get("Content-Disposition") != `attachment; filename="index.m3u8"` {
		t.Fatal(w.Code, w.Body.String(), w.Header())
	}
	if w = request(h, "GET", "/api/livestreams/operations?kind=archive&limit=7&offset=2", "", "Bearer synthetic"); w.Code != 200 || w.Header().Get("X-Total") != "1" || repo.operationKind != "archive" {
		t.Fatal(w.Code, w.Body.String(), w.Header())
	}
	if w = request(h, "POST", "/api/livestreams/operations/archive/"+output+"/redrive", "", "Bearer synthetic"); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = request(h, "POST", "/api/livestreams/"+stream+"/awards/"+purchase+"/refund", `{"reason":"operator reconciliation"}`, "Bearer synthetic"); w.Code != 200 || repo.refundReason != "operator reconciliation" {
		t.Fatal(w.Code, w.Body.String(), repo.refundReason)
	}
}

func TestArchiveImportRequiresExactlyOneIdempotencyKey(t *testing.T) {
	repo := &featureRepo{fakeRepo: &fakeRepo{}}
	h := (&Service{Repository: repo, Authority: fakeAuthority{}}).Handler()
	for _, values := range [][]string{nil, {"one", "two"}} {
		r := httptest.NewRequest("POST", "/api/livestreams/"+stream+"/hls/"+output+"/import", strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer synthetic")
		for _, value := range values {
			r.Header.Add("Idempotency-Key", value)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 || repo.archiveKey != "" {
			t.Fatal(values, w.Code, repo.archiveKey)
		}
	}
}
