// SPDX-License-Identifier: AGPL-3.0-only
package federationpg

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	f "solsynth.local/go-migration/ports/federation"
)

func TestCleanupTransportBindingIsStableAndComplete(t *testing.T) {
	base := f.Config{Origin: "https://local.example.test", AllowedOrigins: []string{"https://z.example.test", "https://a.example.test"}, MaxAttempts: 3, DeliveryTimeout: 5 * time.Second}
	r := &Repository{config: base}
	want := r.cleanupPolicyBinding()
	if len(want) != 64 || r.config.AllowedOrigins[0] != "https://z.example.test" {
		t.Fatal("binding changed configuration or omitted digest", want, r.config)
	}
	for _, mutation := range []struct {
		name string
		edit func(*f.Config)
		same bool
	}{
		{"origin-order", func(c *f.Config) { c.AllowedOrigins = []string{"https://a.example.test", "https://z.example.test"} }, true},
		{"local-origin", func(c *f.Config) { c.Origin = "https://other.example.test" }, false},
		{"egress", func(c *f.Config) { c.AllowedOrigins = []string{"https://a.example.test"} }, false},
		{"attempt-cap", func(c *f.Config) { c.MaxAttempts++ }, false},
		{"timeout", func(c *f.Config) { c.DeliveryTimeout += time.Second }, false},
		{"clock", func(c *f.Config) { c.Now = func() time.Time { return time.Unix(1, 0) } }, true},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			c := base
			mutation.edit(&c)
			if got := (&Repository{config: c}).cleanupPolicyBinding(); (got == want) != mutation.same {
				t.Fatal("transport binding mismatch", got, want)
			}
		})
	}
}

func TestCleanupBodyIsStableContentFreeReceiptProjection(t *testing.T) {
	actor, object := "https://local.example.test/activitypub/actors/owner", "https://local.example.test/activitypub/objects/receipt-object"
	body := cleanupBody(actor, object, 42)
	if !bytes.Equal(body, cleanupBody(actor, object, 42)) || bytes.Equal(body, cleanupBody(actor, object, 43)) {
		t.Fatal("cleanup receipt activity identity is unstable or shared")
	}
	var got map[string]any
	if e := json.Unmarshal(body, &got); e != nil {
		t.Fatal(e)
	}
	if len(got) != 7 || got["@context"] != f.Context || got["id"] != object+"/cleanup/42" || got["type"] != "Delete" || got["actor"] != actor {
		t.Fatal("cleanup activity contains fields beyond receipt projection", got)
	}
	obj, ok := got["object"].(map[string]any)
	if !ok || len(obj) != 2 || obj["id"] != object || obj["type"] != "Tombstone" {
		t.Fatal("cleanup object contains content", obj)
	}
	to, toOK := got["to"].([]any)
	cc, ccOK := got["cc"].([]any)
	if !toOK || len(to) != 1 || to[0] != f.Public || !ccOK || len(cc) != 1 || cc[0] != actor+"/followers" {
		t.Fatal("cleanup audience contains unrelated identities", got)
	}
}
