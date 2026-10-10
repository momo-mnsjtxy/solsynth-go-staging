// SPDX-License-Identifier: AGPL-3.0-only
package auditcapture

import (
	"context"
	"net/http/httptest"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCaptureUsesTransportPeerAndBoundsUntrustedAgent(t *testing.T) {
	p := authority.Principal{Account: authority.Account{ID: "20000000-0000-4000-8000-000000000001"}, Session: authority.Session{ID: "20000000-0000-4000-8000-000000000002"}}
	r := httptest.NewRequest("PATCH", "/api/accounts/me?private-query=secret", strings.NewReader("private-body"))
	r.RemoteAddr = "[::ffff:203.0.113.8]:40000"
	r.Header.Set("X-Forwarded-For", "198.51.100.88")
	r.Header.Set("Authorization", "Bearer private-credential")
	r.Header.Set("User-Agent", strings.Repeat("界", 600)+"\r\n\x00")
	v := Bind(context.Background(), r, p).Value(key{}).(request)
	if v.peer != "203.0.113.8" || v.session != p.Session.ID || utf8.RuneCountInString(v.agent) != 512 || strings.ContainsAny(v.agent, "\r\n\x00") {
		t.Fatal(v)
	}
	if strings.Contains(v.peer+v.agent+v.actor+v.session, "secret") || strings.Contains(v.agent, "private-") {
		t.Fatal("non-audit request fields retained")
	}
	if e := Record(Bind(context.Background(), r, p), nil, "20000000-0000-4000-8000-000000000003", "owned_accounts_audit", 1); e != nil {
		t.Fatal("foreign actor inherited HTTP facts", e)
	}
	r.RemoteAddr = "invalid-transport-peer"
	if v := Bind(context.Background(), r, p).Value(key{}).(request); v.peer != "" {
		t.Fatal("invented a source address", v)
	}
}
