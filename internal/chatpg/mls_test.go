// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"encoding/base64"
	"encoding/json"
	"solsynth.local/go-migration/ports/messager/rooms"
	"testing"
)

func TestMLSGenerationNeverInfersResetFreshness(t *testing.T) {
	one, two := int64(1), int64(2)
	for _, tc := range []struct {
		s  int64
		v  *int64
		ok bool
	}{{1, nil, true}, {1, &one, true}, {1, &two, false}, {2, nil, false}, {2, &one, false}, {2, &two, true}} {
		e := mlsGeneration(rooms.MLSRequest{Generation: tc.v}, rooms.MLSState{StateVersion: tc.s})
		if (e == nil) != tc.ok {
			t.Fatal(tc, e)
		}
	}
}
func TestMLSApplicationMapRequiresOpaqueEnvelope(t *testing.T) {
	opaque := base64.StdEncoding.EncodeToString([]byte("SYNTHETIC ENVELOPE"))
	p := map[string]any{"type": "text", "client_message_id": "synthetic-id", "encryption_meta": map[string]any{"scheme": "chat.mls.v2", "epoch": 0, "ciphertext": opaque, "header": opaque, "signature": nil}, "meta": map[string]any{"attachments_id": []string{}, "replied_message_id": nil, "forwarded_message_id": nil, "survey_id": nil, "fund_id": nil}}
	raw, _ := json.Marshal(p)
	if _, _, e := mlsApplication(raw); e != nil {
		t.Fatal(e)
	}
	for _, key := range []string{"plaintext_envelope", "content", "private_signer_key", "group_secret"} {
		p[key] = "MUST NOT STORE"
		raw, _ = json.Marshal(p)
		if _, _, e := mlsApplication(raw); e == nil {
			t.Fatal("accepted", key)
		}
		delete(p, key)
	}
}
