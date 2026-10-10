// SPDX-License-Identifier: AGPL-3.0-only
package federationpg

import (
	"strings"
	"testing"
	"time"

	f "solsynth.local/go-migration/ports/federation"
)

func TestBroaderSchemaContainsRuntimeState(t *testing.T) {
	for _, table := range []string{"owned_federation_realms", "owned_federation_realm_deliveries", "owned_federation_featured_posts", "owned_federation_moderation_rules", "owned_federation_moves"} {
		if !strings.Contains(BroaderSchema, table) {
			t.Fatal("missing schema", table)
		}
	}
	if strings.Contains(strings.ToLower(BroaderSchema), "private_key") {
		t.Fatal("private key material entered federation schema")
	}
}

func TestModerationValidationAndPriorityMatch(t *testing.T) {
	good := f.ModerationRule{Name: "block example", Kind: "domain", Action: "suspend", Pattern: "remote.example.test", Enabled: true}
	if err := validRule(good); err != nil {
		t.Fatal(err)
	}
	if !moderationMatch(good, "https://remote.example.test/users/alice", "") {
		t.Fatal("domain rule did not match")
	}
	if moderationMatch(good, "https://malicious-remote.example.test/users/alice", "") {
		t.Fatal("domain rule matched a non-label suffix")
	}
	if !moderationMatch(good, "https://sub.remote.example.test/users/alice", "") {
		t.Fatal("domain rule did not match a subdomain label")
	}
	badDomain := good
	badDomain.Pattern = "Remote.Example.Test"
	if validRule(badDomain) == nil {
		t.Fatal("non-canonical domain rule accepted")
	}
	good.Kind, good.Regex, good.Pattern = "keyword", true, `(?i)fraud\s+offer`
	if err := validRule(good); err != nil || !moderationMatch(good, "https://remote.example.test/users/alice", "A FRAUD offer") {
		t.Fatal(err)
	}
	good.Pattern = strings.Repeat("x", 513)
	if validRule(good) == nil {
		t.Fatal("unbounded regex accepted")
	}
}

func TestRemoteAttachmentPolicy(t *testing.T) {
	r := &Repository{config: f.Config{Origin: "https://local.example.test", AllowedOrigins: []string{"https://remote.example.test"}, MaxAttempts: 3, DeliveryTimeout: time.Second}}
	good := []f.Attachment{{Type: "Image", MediaType: "image/png", URL: "https://remote.example.test/media/1", Name: "preview"}}
	if err := r.validateAttachments("https://remote.example.test/users/alice", good); err != nil {
		t.Fatal(err)
	}
	bad := append([]f.Attachment{}, good...)
	bad[0].URL = "https://127.0.0.1/private"
	if err := r.validateAttachments("https://remote.example.test/users/alice", bad); err == nil {
		t.Fatal("cross-origin media accepted")
	}
	bad = append([]f.Attachment{}, good...)
	bad[0].URL = "https://remote.example.test/media/1#credential"
	if err := r.validateAttachments("https://remote.example.test/users/alice", bad); err == nil {
		t.Fatal("fragment-bearing media accepted")
	}
	bad[0].URL = "https://remote.example.test/media/1?token=secret"
	if err := r.validateAttachments("https://remote.example.test/users/alice", bad); err == nil {
		t.Fatal("credential-bearing media accepted")
	}
}
