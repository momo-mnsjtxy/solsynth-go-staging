// SPDX-License-Identifier: AGPL-3.0-only
package commercepg

import (
	"database/sql"
	"encoding/json"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"testing"
	"time"
)

func TestSubscriptionProjectionPrecisionAndMismatch(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.UTC)
	id := "11111111-1111-4111-8111-111111111111"
	account := "22222222-2222-4222-8222-222222222222"
	payer := "33333333-3333-4333-8333-333333333333"
	s := c.Subscription{ID: id, AccountID: account, Identifier: "synthetic", Status: c.SubscriptionActive, BegunAt: now, EndedAt: now.Add(time.Hour), PaymentDetails: c.PaymentDetails{PayerWalletID: &payer}}
	doc, _ := json.Marshal(s)
	v := subscriptionProjection{ID: id, AccountID: account, Identifier: s.Identifier, Status: s.Status, BegunAt: now.Truncate(time.Microsecond), EndedAt: s.EndedAt.Truncate(time.Microsecond), Payer: sql.NullString{String: payer, Valid: true}, Document: doc}
	if _, reason := v.validate(); reason != "" {
		t.Fatal("PostgreSQL microsecond precision rejected", reason)
	}
	for _, kind := range []string{"account", "id", "identifier", "status", "begun", "ended", "renewal", "payer", "null_payer", "malformed"} {
		t.Run(kind, func(t *testing.T) {
			q := v
			switch kind {
			case "account":
				q.AccountID = id
			case "id":
				q.ID = account
			case "identifier":
				q.Identifier = "other"
			case "status":
				q.Status = c.SubscriptionCancelled
			case "begun":
				q.BegunAt = q.BegunAt.Add(time.Microsecond)
			case "ended":
				q.EndedAt = q.EndedAt.Add(time.Microsecond)
			case "renewal":
				q.RenewalAt = &now
			case "payer":
				q.Payer.String = id
			case "null_payer":
				q.Payer.Valid = false
				q.Payer.String = ""
			case "malformed":
				q.Document = []byte(`[]`)
			}
			if _, reason := q.validate(); reason == "" {
				t.Fatal("corrupt projection accepted")
			}
		})
	}
}
