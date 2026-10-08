// SPDX-License-Identifier: AGPL-3.0-only
package rooms

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMLSOpaqueShapeAndPlaintextRejection(t *testing.T) {
	opaque := base64.StdEncoding.EncodeToString([]byte("SYNTHETIC OPAQUE BYTES"))
	meta := &EncryptionMeta{Ciphertext: opaque, Header: opaque, Scheme: "chat.mls.v2", Epoch: 0}
	if _, e := ValidateText(TextRequest{EncryptionMeta: meta}, false); e != nil {
		t.Fatal(e)
	}
	for _, p := range []TextRequest{{EncryptionMeta: meta, Content: "leaked"}, {EncryptionMeta: meta, ContentSet: true}, {EncryptionMeta: meta, Embeds: &[]map[string]any{}}, {EncryptionMeta: meta, Meta: json.RawMessage(`{"plaintext_envelope":"leaked"}`)}} {
		if _, e := ValidateText(p, false); e == nil {
			t.Fatalf("plaintext accepted: %#v", p)
		}
	}
	for _, bad := range []string{"", "{}", "not base64", opaque + "\n"} {
		if ValidMLSOpaque(bad, 100) {
			t.Fatalf("accepted %q", bad)
		}
	}
	if !ValidMLSIdentifier("room:88000000-0000-4000-8000-000000000001") || !ValidMLSIdentifier("MTIzNDU2Nzg5MDA_") || ValidMLSIdentifier("../../outsider") {
		t.Fatal("identifier validation")
	}
	if MLSWelcome != 5 || MLSCommit != 4 || MLSApplication != 6 || MLSProposal != 7 || MLSEncryptionMode != 3 {
		t.Fatal("authored mappings drifted")
	}
}
func TestMLSJSONRejectsDuplicateAndUnknownFields(t *testing.T) {
	for _, body := range []string{`{"epoch":0,"epoch":1}`, `{"meta":{"bootstrap_device_id":"a","bootstrap_device_id":"b"}}`, `{"payloads":[{"recipient_device_id":"a","secret":"leaked"}]}`, `{"plaintext_envelope":"leaked"}`, `null`, `{} {}`} {
		r := httptest.NewRequest("POST", "/e2ee/mls/groups/g/bootstrap", strings.NewReader(body))
		w := httptest.NewRecorder()
		var p MLSRequest
		if e := decodeMLS(w, r, &p, "epoch", "meta", "payloads"); e == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}
func TestMLSActualRoutesAndGenerationPreconditions(t *testing.T) {
	for _, tc := range []struct{ method, path, op string }{
		{"PUT", "/e2ee/mls/devices/me/kps", "publish"}, {"GET", "/e2ee/mls/keys/a/devices", "claim"}, {"POST", "/e2ee/mls/groups/g/bootstrap", "bootstrap"}, {"POST", "/e2ee/mls/groups/g/commit/fanout", "commit-fanout"}, {"POST", "/e2ee/mls/messages/fanout", "application"}, {"POST", "/api/chat/a/devices/me/joined", "joined"}, {"POST", "/e2ee/mls/groups/g/reset", "reset"},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		op, _, method, _ := mlsRoute(r)
		if op != tc.op || method != tc.method {
			t.Fatal(tc, op, method)
		}
	}
	r := httptest.NewRequest("POST", "/api/chat/a/messages", nil)
	r.Header.Set("X-MLS-State-Version", "2")
	p, e := mlsWriteContext(r)
	if e != nil || p.Generation == nil || *p.Generation != 2 {
		t.Fatal(p, e)
	}
	r.Header.Add("X-MLS-State-Version", "3")
	if _, e = mlsWriteContext(r); e == nil {
		t.Fatal("duplicate generation accepted")
	}
}

func TestMLSWireEpochCannotBeOmittedOrNull(t *testing.T) {
	for _, raw := range []string{`{"scheme":"chat.mls.v2","ciphertext":"YQ==","header":"YQ=="}`, `{"scheme":"chat.mls.v2","epoch":null,"ciphertext":"YQ==","header":"YQ=="}`, `{"scheme":"chat.mls.v2","epoch":0,"ciphertext":"YQ==","header":"YQ==","private_signer_key":"secret"}`} {
		var p EncryptionMeta
		if json.Unmarshal([]byte(raw), &p) == nil {
			t.Fatal("ambiguous/unknown encryption fields accepted", raw)
		}
	}
}
