# Current owner eligibility for a new internal checkout

An ordinary one-time app order could be created while its publisher owner was
active and paid by a different active buyer after that owner was disabled. The
existing app ancestry checks considered the publisher, project and app rows;
payment proof and wallet checks considered the buyer and live wallets. None of
those paths checked the merchant owner's current account state. The new hold
could therefore commit even while the app credential paths rejected fulfillment
and status changes because of the disabled owner. Unpaid-order expiry does not
automatically refund an already paid hold.

This is an owned safety-policy correction. It aligns admission of a new internal
wallet charge with the existing canonical app owner's disabled-account and
active `disable_account` punishment rules. It does not claim that the licensed
legacy OrderController required this precise policy. The retained authored
commerce controllers and callers remain identified by
`ports/wallet/commerce/source/manifest.json`; no new external, generated or
excluded source was used. Existing product price, stock reservation, destination,
buyer authority and payment proof contracts remain the authority for checkout.

## Fresh observations and lock order

The first owner eligibility observation runs after retaining publisher, project
and app locks for an unpaid order. The second runs after wallet and payment-proof
work, immediately before committing the new charge. Both query canonical account
state and active punishment intervals using the database clock. The repository
starts its own READ COMMITTED transaction; the second SELECT gets a new statement
snapshot and does not reuse the earlier owner snapshot.

No new locks are acquired. The existing buyer-account, payment-policy,
app-ancestry, order/contact and wallet lock order remains. Adding a merchant-owner
account lock after the buyer would invert two accounts in some cross-account
flows; changing only checkout to sorted locks would also leave other existing
app paths with a different account order. The bounded correction avoids creating
that inversion.

A disable committed before the relevant fresh observation is denied and rolls
back the entire new checkout, including the hold, transaction, destination,
payment-proof consumption, order binding and events. A disable concurrent after
the final eligibility observation may serialize after the payment. This is an
explicit observation boundary, **not** strict disable-before-payment-commit
ordering. Stronger ordering requires a separately normalized synchronization
contract across all participating account/app paths.

## Existing payment results

The helper is called only by `PayOrder` while admitting a new internal wallet
charge. The already-paid/finished replay branch returns before these new checks.
Existing app status authorization, refund and finish settlement are unchanged;
the same held transaction and immutable beneficiary remain authoritative.

`ApplyProvider` does not call `PayOrder`, `lockOrderApp`, the new owner helper, or
`CommerceHold`. It handles already verified provider receipts and subscription
duration through its existing separate transaction. Provider verification,
receipt idempotency and entitlement processing are unchanged. The new policy
does not reject, discard or reinterpret a purchase already captured externally.
No external checkout, callback, payment, shipment or provider operation was run.

## Verification

Two additional before-phase PostgreSQL declarations exercise different buyer and
merchant accounts with the real internal payment-policy/proof adapter. Cases
cover an owner disabled before checkout, active/future/expired punishment,
healthy payment, paid replay after disable, and unchanged refund conservation
after the existing app authority is restored. Concurrent cases observe app and
wallet lock waits, commit owner disabling or activate only the fixture's scheduled
punishment before releasing the wait, then assert that no new charge, hold,
snapshot, paid event or consumed proof survives. No global clock is advanced.

These tests require the authorized disposable PostgreSQL CI environment. Local
ordinary/race runs compile and skip them without a DSN. The correction adds no
schema or persisted state shape; the shipping saved restart fixture continues to
check the original paid destination and economics. No new after-phase marker is
needed. Register the two declarations and independently review group assignment:

- `TestPGCheckoutOwnerEligibilityAndPaidReplay`
- `TestPGCheckoutOwnerDisableAfterWaitRollsBack`

Canonical organization publishers resolve their owner through the live owned realm. Missing, inconsistent, deleted or disabled direct/realm owner chains deny new internal payment; a null direct owner never implies eligibility. Realm ownership transfer is observed freshly and cannot replace the order’s captured payee wallet. These checks retain the same no-new-lock linearization bound.
