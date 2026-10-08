# Funded post awards and captured publisher payouts

This slice extends the frozen publisher-commerce delivery on exact base `8521bd679f4b2156ed23a1299c67aa49b2be1bde`. It adds a real checkout flow, canonical wallet reserves, explicit publisher payout configuration, durable settlement, signed post score and an opt-in rating producer. It does not claim whole-platform parity.

## Authored contracts and policy conflict

The pinned licensed `PostActionController.AwardPost` returns `{order_id}` for product `posts.award`, currency `points`. The authored Solian award sheet reads that ID and opens its existing payment overlay. Its current raw request omits attitude; Positive 0 remains the default. Explicit Negative 2 is supported, Neutral 1 rejected, and messages are limited to 4096 Unicode characters. The history renderer evidences amount, attitude, message and creation date; the owned response also exposes canonical IDs and settlement time. Generated SDK/model definitions were not used.

The full executable settlement chain differs from `docs/POST_AWARD_SETTLEMENT.md`:
- `PostService` and `PublisherService` forward the full positive amount.
- `MerchantServiceGrpc` requires an explicitly configured merchant payment wallet.
- `MerchantService.SettleWalletAsync` sums full pending amounts and calls the ordinary wallet transaction service without freezing or confirmation.
- `PaymentService` directly credits the full amount truncated to three decimals; its confirmation/release paths also credit the transaction amount unchanged. `WalletService` initializes a new pocket with that amount. No fee appears in this retained executable chain.
- The prose instead describes 80/20, individual-wallet fallback and an organization payout field.

Accordingly `merchant_full_v1` is the compatibility default. `documented_80_v1` is an explicit operator alternative only. Each order captures its policy, exact share and beneficiary. The alternative floors each individual share to mills; it does not promise group-rounding parity with the prose. Negative awards have zero share and leave their paid amount in the treasury. Positive zero-share rounding is immediately settled with no zero-value ledger transaction. Pending summaries sum those same captured shares. No individual-wallet fallback is inferred.

The existing wallet ledger replaces upstream nil-payer system issuance: checkout debits the purchaser and credits a configured canonical treasury, then reserves the captured positive share from that credit in the same transaction. Settlement consumes that existing reserve. No second ledger, free fulfillment, external payment or fabricated provider result exists.

## Routes and authority

Sphere and combined aliases:
- POST `/api/posts/{id}/awards` and `/sphere/posts/{id}/awards`: optional existing Idempotency-Key, ordinary request fields, current `posts.award`, returns `{order_id}`.
- GET the same path: bounded offset/take, X-Total, confirmed awards only.
- GET `.../awards/pending`: count, total_amount, payout_amount.

Wallet and combined aliases:
- PATCH `/api/merchants/{publisher}/wallet` and `/wallet/merchants/{publisher}/wallet`: source `{wallet_id}`, current `merchants.manage`, current canonical publisher Manager/Owner, current organization realm membership, and actual wallet management. Individual bindings must select that publisher owner's wallet with the owner as actor. Realm bindings require the publisher's exact realm wallet and current realm Owner.
- GET the wallet path is an explicit owned alias for the publisher payout binding, avoiding changes to the existing app-oriented merchant profile.
- GET `.../settlements`: bounded captured financial receipts for that publisher, never nested post/media content.
- POST `.../settlements/settle`: empty body, current `merchants.settlements.manage`, same current publisher/realm manager gate; bounded 100 receipts.

Publisher roles never confer global administration. Existing global rewards/resettle endpoints, XP/social-credit payouts and retroactive recomputation remain unimplemented; this slice does not repurpose their permissions. The authored permission documentation spells `publishers.rewards.settle` (plural).

Ordinary award reads and creation recheck current canonical post/publisher/realm visibility, blocks, enablement and moderation. A global administrator gets no ordinary visibility exception. Unpaid orders never appear as awards. Drafts, future posts and suppressed posts cannot receive new payments. A current payable post can be private or linked to a realm when the payer actually has canonical access. After a paid award, making content private or deleting it does not cancel an owed reserve. Settlement uses the captured beneficiary and current enabled wallet/account/realm ancestry; changing the merchant setting cannot redirect an old receipt.

## Transactions and money

Writes run in one canonical pool/transaction. Authority-only preflight precedes association observations. Sorted accounts, sorted realms, publisher and post locks are followed by ancestry/visibility rechecks; checkout secondary locks use NOWAIT and map to ORDER_RETRY_REQUIRED (409). The complete payer/treasury/beneficiary wallet set is locked before holds and settlement. The current merchant binding must exactly match the captured order at payment time. Ownership/binding drift rejects the unpaid order without debit.

The fixed-product commerce prepare/fulfill hooks preserve existing livestream award hooks, payment LockPayment and every final CheckPayment. Matching original order, payer, actor, currency, amount, confirmed transaction and captured purchase are required before fulfillment/reservation. The final authority/PIN checks follow all effects; any failure rolls back payment, reserve, receipt, post score, audit and events.

Four new tables: `owned_publisher_payout_wallets`, `owned_post_award_purchases`, `owned_post_award_receipts`, `owned_post_award_audit`. They refer to canonical identities, wallets, posts, orders and transactions. Post schema gains exact numeric `awarded_score`; every canonical/nested post projection uses the existing visibility gates. Receipt/distribution/transaction uniqueness provides replay safety. The release worker takes no bearer token, creates no payment authority and can consume only existing paid receipts and matching holds. Failed releases retain funds and persist bounded retry timing. A replaced treasury configuration needs the previous worker configuration until its old reserves drain; it cannot redirect them.

## Rating policy

Existing `reaction_only_v1` is unchanged by default. Explicit `reaction_and_paid_awards_v1` computes truncation toward zero of (positive reactions - negative reactions + signed awarded_score / 10), using the authored PublisherService formula and exact numeric arithmetic. Counts and award scores come from one SQL snapshot of currently public eligible canonical posts created during a completed UTC day. Private, realm-linked, disabled and suppressed inputs remain excluded.

The existing unique publisher/day receipt is retained across policy changes, including zero receipts. Existing reaction-only days are never recomputed or doubled. History carries each actual policy; overview/leaderboard/header report the configured policy and whether award score is included. Late awards do not rewrite already-produced days. XP/social-credit and legacy float rounding are not claimed.

## Host, CLI and migrations

The concrete isolated host wiring adds `application.Config.PostAwards *postawardpg.Config`, constructs the repository with the existing canonical guard/social provider and the same checkout instance, installs `WithPostAwards`, and dispatches the specific award routes before generic routes. Constructors start no worker. Unconfigured routes return unavailable.

Apply the updated base post schema before post readers; apply `postawardpg.Schema` after canonical account/social/post/wallet/commerce schemas. Apply the rating schema migration, which widens only its policy/formula constraint and preserves existing receipts. The isolated PG harness includes the new schema and exact new test names.

CLI: `--enable-post-awards`, existing canonical `--post-award-treasury-account` and `--post-award-treasury-wallet`, optional `--post-award-payout-policy`, `--post-award-max-amount`, and bounded `--post-award-release-interval`. The default performs startup recovery then runs at UTC midnight; a configured shorter interval explicitly changes that cadence. Each run is bounded to 1000 receipts and a 20-second context. `Backend.ProcessPostAwardsDue(ctx, limit)` exposes bounded recovery separately from sponsorship/rating work.

`--publisher-ratings-paid-awards` requires both explicitly enabled post awards and the existing rating producer/start day. It selects the paid-award policy; it does not rewrite history.

## Validation

Fourteen new PG cases cover checkout/receipt conservation, both payout policies and rounding, signed negative awards, beneficiary drift/missing configuration, private and organization realms, current authority after an account wait, full rollback, concurrent replay/opposite publishers/NOWAIT retries, disabled beneficiary release retry and rollback, order expiry/policy snapshot, the signed combined rating term, restart persistence, and composed HTTP/PIN checkout/release.

Local ordinary tests, race tests, vet and build run without a database. The pinned PostgreSQL parser checks SQL grammar only. Real PG execution/restart is owned by the integration CI; a local package pass with PG skipped is not database validation. No local socket, live provider, real user, credential provisioning or actual payment was used.

