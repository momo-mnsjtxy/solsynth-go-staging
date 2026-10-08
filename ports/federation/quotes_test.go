// SPDX-License-Identifier: AGPL-3.0-only
package federation

import (
	"bytes"
	"context"
	"encoding/json"
	"math/big"
	"net/http/httptest"
	"testing"
	"time"
)

func TestQuoteVerificationRetainsSignedInstrumentAndAge(t *testing.T) {
	c, k := keyFixture(t)
	a := Activity{ID: k.remote.Actor + "/requests/1", Type: "QuoteRequest", Actor: k.remote.Actor, Object: json.RawMessage(`"https://local.example.test/activitypub/objects/00000000-0000-4000-8000-000000000001"`), Instrument: json.RawMessage(`{"type":"Note","id":"https://remote.example.test/notes/1"}`)}
	body, _ := json.Marshal(a)
	r := httptest.NewRequest("POST", c.Origin+"/activitypub/actors/alice/inbox", bytes.NewReader(body))
	if e := Sign(r, body, k.remote.KeyID, k.key, c.Clock()); e != nil {
		t.Fatal(e)
	}
	v, e := Verify(context.Background(), c, k, r, body)
	if e != nil {
		t.Fatal(e)
	}
	if !v.Fresh(c.Clock().Add(5*time.Minute)) || v.Fresh(c.Clock().Add(5*time.Minute+time.Nanosecond)) || v.Fresh(c.Clock().Add(-6*time.Minute)) || (Verified{}).Fresh(c.Clock()) {
		t.Fatal("incorrect verification lifetime")
	}
	copy := v.Activity()
	copy.Instrument[0] = '['
	copy.Object[0] = '['
	got := v.Activity()
	if !bytes.Equal(got.Instrument, a.Instrument) || !bytes.Equal(got.Object, a.Object) {
		t.Fatal("caller mutated verified quote binding")
	}
	altered := bytes.Replace(body, []byte("notes/1"), []byte("notes/2"), 1)
	if _, e = Verify(context.Background(), c, k, r, altered); e == nil {
		t.Fatal("signature did not cover quote instrument")
	}
}

func TestVerifiedRemoteKeySnapshotIsImmutable(t *testing.T) {
	c, k := keyFixture(t)
	a := Activity{ID: k.remote.Actor + "/requests/immutable-key", Type: "QuoteRequest", Actor: k.remote.Actor, Object: json.RawMessage(`"https://local.example.test/activitypub/objects/00000000-0000-4000-8000-000000000001"`), Instrument: json.RawMessage(`"https://remote.example.test/notes/1"`)}
	body, _ := json.Marshal(a)
	request := httptest.NewRequest("POST", c.Origin+"/activitypub/actors/alice/inbox", bytes.NewReader(body))
	if e := Sign(request, body, k.remote.KeyID, k.key, c.Clock()); e != nil {
		t.Fatal(e)
	}
	proof, e := Verify(context.Background(), c, k, request, body)
	if e != nil {
		t.Fatal(e)
	}
	expected := new(big.Int).Set(proof.Remote().PublicKey.N)
	exposed := proof.Remote()
	exposed.PublicKey.N.SetInt64(3)
	if proof.Remote().PublicKey.N.Cmp(expected) != 0 {
		t.Fatal("caller mutated verified signing-key binding")
	}
	k.remote.PublicKey.N.SetInt64(5)
	if proof.Remote().PublicKey.N.Cmp(expected) != 0 {
		t.Fatal("provider mutation rewrote verified key")
	}
}
