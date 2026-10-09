# Settled livestream awards and backed payouts

This slice adds the four authored award routes, canonical order payment and receipt-backed distribution. It builds on root `916046be18d50dbe6808f12ab380b97f84669ecb`, the separately frozen initial livestream runtime correction, frozen chat delivery and its two-file review. The parent fixture correction removes redundant non-idempotent media DDL. Those prerequisites remain independently frozen. The exact comparison baseline is `/workspace/shared/solsynth-go-livestream-awards-base`.

## Source contract and owned behavior

The complete retained DysonNetwork controller/service and award models are pinned to `4ee2d0cebb9da8284a92483fee6dce795bf6947c`. The controller lines 753–901 provide GET awards, active awards, leaderboard and POST awards, whose sole response is an order ID. The full authored `LiveStreamService.cs` lines 496–603 provide paid confirmation, Positive attitude, two seconds of highlight per point and a 90% creator share at stream end. Exact model bytes and license provenance are in the earlier `ports/sphere/livestreams/operations-source/manifest.json`; this slice imports no additional Shared, generated, protocol or unlicensed source.

The source's nil-payer system credit is deliberately replaced by a funded canonical reserve. An award requires an actual committed original payment and a unique receipt. The original donation credits a configured treasury wallet, then reserves exactly 90% from that credit in the same transaction. The remaining 10% is available in that treasury. End processing releases the reserved amount to the captured beneficiary. No new ledger, external payment callback, free award, nil-payer credit, synthetic entitlement or new credential is introduced.

The scope name `livestreams.awards` is owned policy encoding; the excluded permission constant's literal was not imported. New award orders and payment fulfillment require this permission, current `orders.pay` at payment, the original same-verifier opaque proof, root Type-0 session, current canonical account/publisher/realm visibility and blocking, an active current run and its current provider binding. Existing canonical wallet payment/PIN checks remain authoritative. This first release supports individual publishers with an existing canonical account wallet, matching the source distribution service. Realm publishers remain unavailable for awards.

Routes under `/api/livestreams`:

| Route | Contract |
| --- | --- |
| `POST /{id}/awards` | Authenticated JSON numeric `amount`, nullable `message` up to 4096 characters; optional single Idempotency-Key up to 128 bytes. Returns `order_id` only. |
| `GET /{id}/awards` | Current stream visibility; limit 1–100/default 20, offset 0–100000; newest paid awards first; X-Total. |
| `GET /{id}/awards/active` | Current visibility; up to 100 paid highlights ordered by amount, with X-Total. Highlight ends at paid creation time plus trunc(amount×2) seconds. |
| `GET /{id}/awards/leaderboard` | Current visibility; limit 1–100/default 10; rank, account_id, total_amount, award_count and current canonical public account. |

Award DTOs include all known authored JSON fields: id, amount, attitude, message, live_stream_id, account_id, sender_name and the established creation/update/deletion timestamp projection. The authored Sender/highlight helper properties have JsonIgnore, so they are not invented as award response fields. Leaderboard account projection uses the exact existing public `lookupAccount` scanner, including current disabled/deleted/verification behavior. It never fabricates an authenticated viewer or adds private authority/contact fields.

The canonical wallet uses exact thousandths. Award input must be a positive JSON number whose full value is exactly representable and whose 90% share is also exactly representable: multiples of 0.010 points, including 0.01. Extra nonzero precision and quoted numbers are rejected rather than silently truncated. Configured maximum is bounded by 43200 points, keeping source highlight duration within 24 hours. For example 10.50 points reserves 9.450 and retains 1.050 ; 0.01 reserves 0.009 and retains 0.001. The source's integer conversion gives the latter a zero-second highlight.

Per-account/per-stream order caps and total per-stream caps include unpaid and expired orders; there is no implicit deletion of payment/idempotency history. Reusing a key returns the original order only under the same current binding and request. Current account-owner version, beneficiary wallet, stream generation or provider changes may reject an unpaid order; no funds move in that rejection. Once a stream has award orders, new orders cannot silently switch its points currency or treasury binding.

Only fulfilled purchases contribute to public awards, highlights, leaderboards or stream total_award_score. Amount fields use exact canonical decimal JSON. distributed_award_amount follows the authored gross-award accounting meaning: it increases by the original amount whose 90% share was actually released, not by the net transfer itself. Discovery uses the source score×10 plus viewer count ordering when awards are enabled.

## Canonical payment and locking

`commercepg.WithLiveStreamAwards` installs a typed fixed-product participant. PayOrder calls PrepareLiveStreamAward before CommerceHold and FulfillLiveStreamAward after CommerceSettle, then records Finished and commits. The existing name-card participant remains unchanged. Preparation holds the original caller's fresh permission and the captured stream/owner/treasury/wallet binding. Fulfillment independently verifies original stored order, actor, product, currency, amount, managed confirmed transaction and payee before creating an award receipt.

PayOrder already holds the payer account and order. Every secondary account, publisher/stream and complete three-wallet lock uses NOWAIT, so opposing actors cannot retain inverted lock waits. A 55P03 becomes typed commerce.ErrRetry and HTTP 409 ORDER_RETRY_REQUIRED. The entire transaction rolls back; no partial paid order, donor debit, treasury credit, reserve, award, receipt or audit survives. Ordinary source order and final authority checks remain in place.

The fixed reserve creates a real internal commerce order for `livestreams.award.distribution`, with its own canonical managed wallet hold from treasury to beneficiary. `walletpg.ReserveAwardShare` verifies the original confirmed transaction, stored purchase, exact 90% arithmetic, treasury owner and both fixed internal products. A unique index on the original order reference permits only one distribution order per settled award. This supplements unique purchase/order/original-transaction/reserve-transaction receipts. No request metadata can select or redirect a payee.

The independently developed payment-factor slice overlaps only commerce repository/PayOrder/HTTP composition. Preserve its LockPayment call immediately after opening the guarded transaction and its CheckPayment immediately before every commit, including the paid-idempotent path. The award prepare/fulfill hooks stay between them. An expired/revoked payment proof after a wallet/award wait must roll back all award effects as well. Existing trusted direct-domain fixtures do not install that optional adapter; the composed backend must install its canonical payment adapter.

Refunds and chargebacks for paid awards are unsupported in this slice. The original paid order is Finished; generic developer status APIs cannot mutate an internal order, and ordinary wallet transition/expiry APIs cannot change a managed transaction. A settled award cannot be refunded independently of its reserve. A future refund protocol must atomically reconcile original payment, treasury fee, held/released share and award/score before allowing reversal. This slice exposes no such shortcut and never credits both a refund and creator payout.

## Durable payout policy

The captured beneficiary is the current individual publisher owner and canonical wallet at settlement, revalidated against the order's captured owner/version/wallet. A later publisher transfer cannot redirect an already-earned receipt. Payout checks that the captured account remains active and owns that same wallet. Account version changes alone do not confiscate an already-earned entitlement. A pending unpaid order across ownership/version changes is denied.

When the recorded run is logically ending or done, ProcessAwardsDue releases its existing managed reserve through canonical CommerceSettle and atomically marks the distribution order Finished, receipt released and audit/outbox records. End/delete can therefore trigger payouts even if provider cleanup remains pending. No RTC request, token, signing key or external transfer is involved in distribution.

Processing locks the complete current ancestry and captured account set before publisher/stream/purchase/receipt/wallet state. Batches are 1–100. Failures retain state held, the original binding and the backed held balance; attempt counter and next_attempt_at persist with backoff capped at 3600 seconds. There is no terminal abandonment of an earned monetary obligation. Inspectable reason codes are beneficiary_unavailable, treasury_unavailable, concurrent_change and release_pending. released_at remains null until the canonical credit and receipt commit together. Audit/commit failure rolls the release back and retries the same receipt.

Disabled/deleted captured beneficiary accounts or wallets keep funds held; they do not cause a phantom payout, fallback wallet or reroute to a later publisher owner. A valid restoration of that same canonical binding allows retry. Permanent loss or a requested refund requires a separately reviewed reconciliation path; no manual redirection/refund endpoint is supplied here.

The worker selects only its configured treasury account/wallet/currency binding. Retain a matching maintenance worker until all obligations drain before rotating or removing that binding. A new treasury configuration does not gain authority to spend old treasury reserves. This is independent of provider rotation or provider credentials.

The ordinary Repository.ProcessDue runs ready award obligations first, then independently attempts lifecycle/chat work and combines failures with errors.Join. An unavailable or failed provider can still produce its explicit error while an earned payout succeeds. ProcessAwardsDue and AwardsReady are separately callable without a provider.

## Host construction and explicit enablement

Ordinary stream mode retains the existing canonical same-pool account/social/media repositories, guard/verifier and configured provider. After constructing the commerce and livestream repositories, before serving:

```go
err = streamRepo.WithAwards(livestreampg.AwardConfig{
    PointsCurrency: configuredPointsCurrency,
    TreasuryAccountID: configuredTreasuryAccountID,
    TreasuryWalletID: configuredTreasuryWalletID,
    MaxAmount: configuredMaximumExactPoints,
    MaxOrdersPerAccountStream: 100,
    MaxAwardsPerStream: 10000,
    OrderTTL: time.Hour,
}, commerceRepo)
commerceRepo.WithLiveStreamAwards(streamRepo)
```

Suggested operator flags are disabled-by-default `--enable-livestream-awards`, an explicit canonical points-currency mapping, existing treasury account/wallet IDs and bounded amount/order policy. The constructor never creates or funds these identities or wallets. Ordinary enabled streaming still requires a configured provider. Ready includes AwardsReady when enabled; that also verifies the configured live treasury binding and valid unique distribution-source index. The existing livestream handler dispatches awards; existing canonical order/PIN routes complete payment. New orders/payments fail unavailable when the original provider binding is absent or rotated.

For payout-only mode, the narrow constructor needs only the existing pool, canonical commerce repository and retained treasury binding:

```go
maintenance, err := livestreampg.NewAwardMaintenance(db, commerceRepo,
    livestreampg.AwardMaintenanceConfig{
        PointsCurrency: retainedPointsCurrency,
        TreasuryAccountID: retainedTreasuryAccountID,
        TreasuryWalletID: retainedTreasuryWalletID,
    })
err = maintenance.Ready(ctx)
count, err := maintenance.ProcessDue(ctx, 100)
```

This object exposes only Ready and ProcessDue; it implements neither the stream repository nor the award-order repository. It needs no provider, signing material, first-party session, placeholder stream timing policy, media adapter or new grant. It only validates and retires existing stored settled-payment/hold/receipt lineage under fresh canonical account/wallet locks. The same commerce repository can be constructed with its ordinary canonical guard and payment configuration; maintenance itself creates no new payment.

The host owner may expose an explicit `--livestream-awards-maintenance-only` mode that installs these readiness/jobs while leaving new stream/admission/award-order HTTP APIs unavailable. Do not accidentally wire a full nil-provider stream repository as a substitute. These host/CLI changes are outside this patch and remain the parent's integration responsibility.

## Schema and preserved seams

Apply `internal/livestreampg/awards_schema.sql` after authority/accounts, canonical wallet, commerce and initial livestream schema. Chat is a code prerequisite for the shared account projection interface; award schema has no chat-table dependency. Two new tables are owned_livestream_award_purchases and owned_livestream_award_receipts. Indexes cover bounded order count/history/due payouts and `commerce_v1.owned_livestream_award_distribution_source`, a unique partial expression index on fixed internal distribution orders. Existing wallet/commerce tables and conservation checks are reused; no ledger column or financial history is rewritten.

New public-account projection is in its own file and delegates exactly to lookupAccount. Current account scanner/admin/alias/PIN work must remain intact. Shared commerce changes are limited to the optional typed participant field, prepare/fulfill calls, a fixed-product helper file and the typed retry response. New wallet helper code is in its own file and composes the existing hold/settle primitives. All domain request mutations retain fresh original-proof transaction guards; maintenance validates existing receipts rather than constructing a Principal.

## Validation and required guarded PostgreSQL cases

Local full package tests, focused race/vet and integration race compilation are run without local PostgreSQL. Parent CI owns actual guarded disposable database execution and restart. New fixtures check the preinstalled canonical wallet/commerce schemas and do not reapply their non-idempotent DDL. All additional awards DDL is idempotent. They use the same verifier/guard/pool for original proofs, typed JSON helpers, explicit synthetic wallets/balances and clock-derived timestamps. Provider actions remain synthetic. There are no live provider/storage requests, real user accounts, new production credentials, actual payments or publication in this task.

Required before restart:

- TestPGLiveStreamAwardsPaidLifecycleAndConservation
- TestPGLiveStreamAwardsFulfillmentRollbackAndReplay
- TestPGLiveStreamAwardsFreshAuthorityAndNOWAITRollback
- TestPGLiveStreamAwardsOwnerBindingAndProviderIndependentPayout
- TestPGLiveStreamAwardsEndDuringPaymentAndUnsupportedRefund
- TestPGLiveStreamAwardsOpposingActorsAndStreams
- TestPGLiveStreamAwardsExpiryCapsAndPayoutRollback
- TestPGLiveStreamAwardsPayoutRestartPersistence
- TestPGLiveStreamAwardsHTTPStrictRequestTransport

Required after an actual database restart: TestPGLiveStreamAwardsPayoutRestartPersistence only. It verifies a different treasury cannot consume the record, reconstructs the captured binding with provider=nil, checks canonical balances, and exercises the narrow payout-only object's readiness and idempotent processing. Saved fixture state contains IDs/currency only.

Remaining source gaps are provider `stream_awarded` data broadcasts, monetary refunds/chargebacks/reconciliation, realm-publisher distributions, external payment providers and egress/HLS/private playback. Paid awards, score/history/highlights/leaderboard and backed end distribution are implemented. No realtime award-delivery claim, refund promise or complete legacy parity is made.

## Integrated host candidate

The host now composes WithAwards with the existing canonical commerce repository and explicit flags for currency/treasury/amount/order bounds. `--livestream-awards-maintenance-only` selects the narrow payout object and requires no RTC provider; it is mutually exclusive with streaming APIs and uses the fast external maintenance cadence. Other backend modules retain their normal verification configuration. No account or wallet is provisioned by either mode. The guarded installer includes the award schema and unique reserve-source index. One composed case verifies unpaid-versus-paid visibility, actual synthetic canonical checkout,90/10 conservation, then a separately constructed provider-free maintenance host releases the earned reserve exactly once. Ten new PG cases await exact-commit private CI.
