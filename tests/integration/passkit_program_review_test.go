// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/passkitpg"
	"solsynth.local/go-migration/internal/testpasskit"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/passkit"
	c "solsynth.local/go-migration/ports/wallet/commerce"
)

func TestPGPasskitInternalProgramEligibility(t *testing.T) {
	passRepo := func(f commerceFixture, group string) *passkitpg.Repository {
		t.Helper()
		if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), f.actor, []authority.Rule{{Pattern: "orders.*"}, {Pattern: "subscriptions.*"}, {Pattern: "wallets.*"}, {Pattern: passkit.ProvisionPermission}}, nil); e != nil {
			t.Fatal(e)
		}
		signing := testpasskit.New(t)
		generator, e := passkit.NewGenerator(signing.Config)
		if e != nil {
			t.Fatal(e)
		}
		repo, e := passkitpg.New(f.db, f.guard, passkitpg.Config{Generator: generator, ProgramGroup: group})
		if e != nil {
			t.Fatal(e)
		}
		return repo
	}
	assertLabel := func(f commerceFixture, repo *passkitpg.Repository, want string) {
		t.Helper()
		bundle, e := repo.Member(f.ctx, f.actor)
		if e != nil {
			t.Fatal(e)
		}
		fields := passJSON(t, bundle.Data)["generic"].(map[string]any)["auxiliaryFields"].([]any)
		if len(fields) != 1 || fields[0].(map[string]any)["value"] != want {
			t.Fatal("pass program label disagrees with supported current membership", fields, want)
		}
	}
	buy := func(f commerceFixture, definition string) c.Subscription {
		t.Helper()
		s, e := f.r.CreateSubscription(f.ctx, f.actor, c.CreateSubscription{Identifier: definition}, "pass-program", false)
		if e != nil {
			t.Fatal(e)
		}
		o, e := f.r.SubscriptionOrder(f.ctx, f.actor, s.ID)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); e != nil {
			t.Fatal(e)
		}
		s, e = f.r.Subscription(f.ctx, f.actor, s.ID)
		if e != nil || !s.IsActive {
			t.Fatal("canonical billing fixture must be active", s, e)
		}
		return s
	}
	firstParty := newCommerceFixture(t)
	internalPass := passRepo(firstParty, firstParty.plan)
	sub := buy(firstParty, firstParty.plan)
	assertLabel(firstParty, internalPass, "Synthetic plan")
	query(t, firstParty.db, `UPDATE commerce_v1.subscriptions SET document=jsonb_set(document,'{is_testing}','true'::jsonb) WHERE id=$1`, sub.ID)
	if got, e := firstParty.r.Subscription(firstParty.ctx, firstParty.actor, sub.ID); e != nil || got.IsActive {
		t.Fatal("canonical testing entitlement must be inactive", got, e)
	}
	assertLabel(firstParty, internalPass, "Standard")
	query(t, firstParty.db, `UPDATE commerce_v1.subscriptions SET document=jsonb_set(jsonb_set(jsonb_set(document,'{is_testing}','false'::jsonb),'{begun_at}',to_jsonb($2::timestamptz)),'{ended_at}',to_jsonb($3::timestamptz)),begun_at=$2,ended_at=$3 WHERE id=$1`, sub.ID, time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))
	if group, e := firstParty.r.SubscriptionGroup(firstParty.ctx, firstParty.actor, firstParty.plan); e != nil || group.Current != nil || group.Next == nil {
		t.Fatal("canonical future cycle must not be current", group, e)
	}
	assertLabel(firstParty, internalPass, "Standard")
	query(t, firstParty.db, `UPDATE commerce_v1.subscriptions SET document=jsonb_set(jsonb_set(document,'{begun_at}',to_jsonb($2::timestamptz)),'{ended_at}',to_jsonb($3::timestamptz)),begun_at=$2,ended_at=$3 WHERE id=$1`, sub.ID, time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour))
	if got, e := firstParty.r.Subscription(firstParty.ctx, firstParty.actor, sub.ID); e != nil || got.IsActive {
		t.Fatal("canonical expired cycle must be inactive", got, e)
	}
	assertLabel(firstParty, internalPass, "Standard")

	app := newRecurringProductFixture(t)
	definition := recurringDefinition(t, app)
	appPass := passRepo(app, definition.GroupIdentifier)
	appSub := buy(app, definition.Identifier)
	// App-product programs are unsupported even before revocation. They cannot
	// claim membership after canonical consent makes the purchase ineligible.
	assertLabel(app, appPass, "Standard")
	query(t, app.db, `UPDATE owned_app_consents SET revoked_at=clock_timestamp(),version=version+1 WHERE account_id=$1 AND app_id=$2`, app.actor, app.app)
	if got, e := app.r.Subscription(app.ctx, app.actor, appSub.ID); e != nil || got.IsActive {
		t.Fatal("canonical app consent revocation was not observed", got, e)
	}
	assertLabel(app, appPass, "Standard")
}
