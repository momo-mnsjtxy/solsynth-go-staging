// SPDX-License-Identifier: AGPL-3.0-only
package rooms

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/messager"
	"strings"
	"testing"
)

func TestAuthoredVoiceMultipartNamesAndBounds(t *testing.T) {
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="sample.m4a"`)
	h.Set("Content-Type", "audio/m4a")
	part, _ := w.CreatePart(h)
	_, _ = io.WriteString(part, "synthetic audio bytes")
	_ = w.WriteField("client_message_id", "synthetic-client")
	_ = w.WriteField("durationMs", "1200")
	_ = w.WriteField("repliedMessageId", "00000000-0000-4000-8000-000000000001")
	w.Close()
	r := httptest.NewRequest("POST", "/", body)
	r.Header.Set("Content-Type", w.FormDataContentType())
	p, e := readVoiceMultipart(r)
	if e != nil || p.ClientMessageID == nil || *p.ClientMessageID != "synthetic-client" || p.DurationMS == nil || *p.DurationMS != 1200 || string(p.Data) != "synthetic audio bytes" {
		t.Fatal(p, e)
	}
	for _, mutation := range []func(*VoiceRequest){func(p *VoiceRequest) { p.MIMEType = "text/html" }, func(p *VoiceRequest) { p.MIMEType = "audio/ogg; remote=https://elsewhere" }, func(p *VoiceRequest) { p.Name = "../voice" }, func(p *VoiceRequest) { p.Data = nil }, func(p *VoiceRequest) { p.Data = make([]byte, VoiceMaxBytes+1) }, func(p *VoiceRequest) { x := int32(-1); p.DurationMS = &x }, func(p *VoiceRequest) { x := "https://elsewhere"; p.ForwardedMessageID = &x }} {
		bad := p
		mutation(&bad)
		if _, e = ValidateVoice(bad); e == nil {
			t.Fatal("accepted invalid voice")
		}
	}
}
func TestVoiceMultipartDuplicateAliasAndUnexpectedField(t *testing.T) {
	for _, fields := range [][]string{{"durationMs", "duration_ms"}, {"client_message_id", "clientMessageId"}, {"source_url"}} {
		body := &bytes.Buffer{}
		w := multipart.NewWriter(body)
		for _, f := range fields {
			_ = w.WriteField(f, "123")
		}
		w.Close()
		r := httptest.NewRequest("POST", "/", body)
		r.Header.Set("Content-Type", w.FormDataContentType())
		if _, e := readVoiceMultipart(r); e == nil {
			t.Fatal(fields)
		}
	}
}
func TestPlaceholderValidationNoUnboundedOrNonfiniteState(t *testing.T) {
	id := "00000000-0000-4000-8000-000000000001"
	s := strings.Repeat("界", 4097)
	for _, p := range []PlaceholderUpdate{{MessageID: id, ContentChunk: &s}, {MessageID: id, Progress: transientPtr(math.NaN())}, {MessageID: id, Progress: transientPtr(math.Inf(1))}, {MessageID: "bad"}} {
		if ValidatePlaceholderUpdate(p) == nil {
			t.Fatal(p)
		}
	}
	if ValidatePlaceholderKind("streaming") != nil || ValidatePlaceholderKind("uploading") != nil || ValidatePlaceholderKind("unknown") == nil {
		t.Fatal("kinds")
	}
	if ValidatePlaceholderUpdate(PlaceholderUpdate{MessageID: id, Progress: transientPtr(3.0)}) != nil {
		t.Fatal("source clamps finite progress")
	}
	if ValidatePlaceholderFinalize(PlaceholderFinalize{MessageID: id, AttachmentsID: &[]string{id, id}}) == nil {
		t.Fatal("duplicate attachment")
	}
}
func transientPtr[T any](v T) *T { return &v }

type transientReadRepo struct {
	Repository
	calls int
}

func (r *transientReadRepo) OpenVoice(context.Context, string, string, string) (VoiceClip, io.ReadSeekCloser, error) {
	r.calls++
	return VoiceClip{}, nil, ErrNotFound
}
func (r *transientReadRepo) SendVoice(context.Context, string, string, VoiceRequest) (messager.MessageProjection, error) {
	return messager.MessageProjection{}, ErrUnavailable
}

type revokedTransientIdentity struct{ fakeAuthority }

func (revokedTransientIdentity) RequestIdentityPresent(*http.Request) bool { return true }
func (revokedTransientIdentity) AuthenticateRequest(*http.Request) (authority.Principal, error) {
	return authority.Principal{}, authority.ErrInvalid
}
func TestVoiceReadCannotDowngradeBoundRevokedIdentity(t *testing.T) {
	repo := &transientReadRepo{}
	h := (&Service{Repository: repo, Authority: &revokedTransientIdentity{}}).Handler()
	w := call(h, "GET", "/api/chat/"+testRoom+"/voice/"+testActor, "", "")
	if w.Code != 401 || repo.calls != 0 {
		t.Fatal(w.Code, repo.calls)
	}
	h = (&Service{Repository: repo, Authority: &fakeAuthority{}}).Handler()
	w = call(h, "GET", "/api/chat/"+testRoom+"/voice/"+testActor, "", "")
	if w.Code != 404 || repo.calls != 1 {
		t.Fatal(w.Code, repo.calls)
	}
}

type transientFailureRepo struct {
	Repository
	err error
}

func (r transientFailureRepo) OpenVoice(context.Context, string, string, string) (VoiceClip, io.ReadSeekCloser, error) {
	return VoiceClip{}, nil, r.err
}
func (r transientFailureRepo) SendVoice(context.Context, string, string, VoiceRequest) (messager.MessageProjection, error) {
	return messager.MessageProjection{}, r.err
}

func TestVoiceHTTPCanonicalStorageRefusals(t *testing.T) {
	for _, failure := range []*media.Failure{media.ErrQuota, media.ErrQuotaMissing, media.ErrTooLarge, media.ErrConflict, media.ErrPending, media.ErrGone, media.ErrNotFound} {
		for _, method := range []string{"GET", "POST"} {
			t.Run(method+"/"+failure.Code, func(t *testing.T) {
				body := &bytes.Buffer{}
				w := multipart.NewWriter(body)
				h := textproto.MIMEHeader{}
				h.Set("Content-Disposition", `form-data; name="file"; filename="sample.m4a"`)
				h.Set("Content-Type", "audio/m4a")
				part, _ := w.CreatePart(h)
				_, _ = io.WriteString(part, "synthetic audio bytes")
				w.Close()
				path := "/api/chat/" + testRoom + "/messages/voice"
				if method == "GET" {
					path = "/api/chat/" + testRoom + "/voice/" + testActor
				}
				r := httptest.NewRequest(method, path, body)
				r.Header.Set("Authorization", "Bearer fixture")
				r.Header.Set("Content-Type", w.FormDataContentType())
				out := httptest.NewRecorder()
				repo := transientFailureRepo{err: fmt.Errorf("canonical storage: %w", failure)}
				(&Service{Repository: repo, Authority: &fakeAuthority{}}).Handler().ServeHTTP(out, r)
				if out.Code != failure.Status || !strings.Contains(out.Body.String(), failure.Code) || strings.Contains(out.Body.String(), "canonical storage") {
					t.Fatal(out.Code, out.Body.String())
				}
			})
		}
	}
	repo := transientFailureRepo{err: fmt.Errorf("private storage diagnostic")}
	out := call((&Service{Repository: repo, Authority: &fakeAuthority{}}).Handler(), "GET", "/api/chat/"+testRoom+"/voice/"+testActor, "", "Bearer fixture")
	if out.Code != 503 || strings.Contains(out.Body.String(), "private storage diagnostic") {
		t.Fatal(out.Code, out.Body.String())
	}
}
