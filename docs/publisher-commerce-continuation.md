# Publisher ratings and funded post sponsorship

This integrated slice is based on exact commit `8521bd679f4b2156ed23a1299c67aa49b2be1bde`. The unpublished implementation was reconstructed after execution-workspace loss. Its source evidence is recorded in [the pinned manifest](publisher-commerce-source-evidence.json). It includes the module implementation, narrow commerce hooks, host routing/readiness, explicit CLI configuration, scheduler composition and disposable validation wiring.

## Evidenced contracts

Publisher subscriptions in the authored PublisherSubscriptionController/Service are free follows, approval requests, mute/block settings and read state. Existing flows remain in their canonical social repositories. No paid publisher subscription is inferred.

Publisher public routes:

- GET `/api/publishers/{name}/rating`: numeric base-100 rating.
- GET `/api/publishers/{name}/rating/overview`: rating, tied rank, visible population, percentile and grade.
- GET `/api/publishers/{name}/rating/history`: is_member and records, with X-Total.
- GET `/api/publishers/leaderboard`: paged visible publishers with X-Total.

The rating service uses competition ranks (1, 1, 3 for ties), the authored percentile and grade thresholds, and current canonical name/UUID/retired-alias resolution. Ranking populations are filtered by the current viewer's publisher/realm/block/moderation policy. Global administration never bypasses ordinary private reads. Picture projection remains null rather than copying unverified media data.

Sponsorship routes:

- POST `/api/posts/{id}/sponsor`, body numeric amount, returns order_id and amount.
- GET `/api/posts/{id}/sponsor`: active total_amount.
- GET `/api/posts/{id}/sponsor/history`: bounded paid-bid history with X-Total; bidder sees their rows, the individual publisher account sees all rows, other authorized viewers see an empty list. Current post visibility is still required.
- GET `/api/posts/sponsor/leaderboard`: visible posts, total_amount and bid_count.
- GET `/api/posts/sponsor/current`: sponsored false or a canonical composed post carrying sponsored true.
- GET `/api/ads/{name}`: public per-post aggregate advertising statistics and display_chance.
- GET `/api/publishers/{name}/ads`: the corresponding publisher-member projection, requiring both a current unrevoked publisher membership and canonical realm membership.

The same ports recognize /sphere aliases. Page sizes are bounded at 100 and offset at 10,000. JSON rejects duplicate/unknown fields, string money and malformed bodies. Current identity comes from the host's canonical identity callback, never account headers.

## Paid lifecycle and invariants

Source product `ads.sponsor` always uses `golds`, with minimum 5. Canonical wallet amounts retain exact thousandths and serialize as JSON numbers. The source decimal-to-wallet precision boundary is the existing owned wallet amount policy. Optional Idempotency-Key binds actor, post, amount and treasury; mismatched replay conflicts.

Creating an order only inserts an immutable purchase binding and a canonical unpaid commerce order. It does not create an active bid. Ordinary Wallet checkout requires current `orders.pay` and `posts.sponsor`, verifies the canonical actor/order/product/post/publisher/treasury/amount relation, and rechecks current public eligibility. Private, drafted, future, deleted, shadowbanned, blocked or realm-linked posts cannot enter public promotion. Public organization publishers require a currently public, enabled realm. Self-sponsorship is supported.

Checkout's prepare hook locks additional accounts in sorted order, then realm, publisher, post, purchase and wallets. Secondary account/domain/wallet locks use NOWAIT because checkout already owns its actor/order. Contention rolls back the entire attempt with HTTP 409; retry the same order. Canonical ancestry and authority are rechecked under the acquired locks.

Only after CommerceHold and CommerceSettle create a confirmed canonical ledger transaction can fulfillment set the paid bid's transaction, confirmation and 24-hour expiry. The order then becomes Finished. The bid update, wallet debit/treasury credit, order, both outboxes and audit commit together. Audit/constraint failures roll everything back. Unique order and transaction bindings, locked purchase state and checkout's existing replay handling prevent duplicate fulfillment. Calling the internal fulfillment hook on an unpaid order without a confirmed transaction is rejected.

The authored system burned sponsorship money into its system sink. The owned implementation instead credits an explicitly configured canonical treasury wallet, preserving wallet conservation without a second ledger or a fabricated provider success. There is no publisher payout, refund API, external transaction or fulfillment based only on JSON metadata. Final canonical authority checks run after the commerce event write; the integration must retain the independent payment PIN final check after every settlement/fulfillment effect.

Treasury account/wallet ownership and enablement are checked at readiness and payment. An outstanding order whose configured treasury binding changes cannot silently redirect payment. Order TTL defaults to one hour and is configurable from one minute to 24 hours. Maximum amount defaults to 1,000,000 golds; cumulative purchases per post default to 10,000, configurable up to 100,000. Existing grants and wallet balances are never provisioned by runtime constructors.

## Placements and ratings workers

The local hourly auction uses settled, unexpired bids on currently public eligible posts, a maximum 10,000-candidate set and exact integer-thousandths weights. Cryptographic rejection sampling produces proportional odds without converting money to floats. A per-hour transaction lock and unique UTC hour prevent duplicate placements. Selected ancestry is locked and visibility checked again before commit. A winning placement lasts its source-defined hour; individual bid expiry stops future auction weight but does not shorten a placement already won. Private/deleted/moderated transitions immediately suppress all future public projections.

`sponsorshippg.NewAuctionWorker(db, canonicalSocial, now)` returns a worker requiring no user bearer, signing key or payment authority. Call Ready and Run with a bounded context on the hourly schedule. This worker only creates placements from paid state and cannot create orders or bids.

GET current does not claim a view occurred. A host integration that actually displays a sponsored projection may call `RecordDisplay(ctx, actor, postID)`; this is not an HTTP counter endpoint. It rechecks current placement/visibility and increments durable impression counts atomically. Automatic timeline/featured-feed injection, anonymous fingerprint cadence and PerkLevel ad-free entitlement mapping remain explicit gaps. No fake impressions or invented entitlement level are generated.

The authored daily rating formula is trunc(upvotes - downvotes + AwardedScore × 0.1), using posts created on the preceding UTC day and their current reactions. Canonical paid PostAward/AwardedScore input is not implemented in this slice. The explicitly enabled `reaction_only_v1` producer records actual positive-minus-negative canonical reactions on currently anonymous-public posts, excluding realm-linked/private/moderated posts and disabled reaction authors. It does not synthesize the missing award term, XP or social-credit changes.

Responses identify that subset through X-Publisher-Rating-Policy, overview/leaderboard policy and award_score_included=false, and each history record's policy. This is a documented owned subset, not full legacy grading parity. The simple numeric rating endpoint preserves its wire number and carries the policy header.

Configure `publisherratingpg.Config{ReactionOnly:true, EarliestDay: UTCDate, Now: now}` to enable the producer. Unconfigured producer calls return unavailable. ProcessYesterday(ctx, limit), ProcessDay(ctx, day, limit) or ProcessPublisherDay(ctx, publisherID, day) record bounded batches of at most 1,000 publishers, only completed UTC days within the last 31 days and no earlier than the configured date. All input counts for a batch share one SQL snapshot; counts exceeding one million fail rather than clipping a score. Unique publisher/day receipts, including hidden zero-delta receipts, make retries and restarts idempotent. Repeated batches can drain the same day; scoped retries do not double-apply it. There is no arbitrary public rating writer or legacy aggressive resettlement duplication.

## Host, schema and shared seam

Apply `sponsorshippg.Schema` after canonical accounts, realms, posts, wallet and commerce schemas. It adds four tables: owned_post_sponsorships, owned_post_sponsor_placements, owned_post_sponsor_impressions and owned_post_sponsor_audit. Apply `publisherratingpg.Schema` after posts/interactions; it adds owned_publisher_rating_records. No canonical identity or ledger is duplicated.

Construct the existing commerce repository first, preserving current app adapters, clock, PIN verifier, name-card and livestream-award hooks. Then construct:

1. `sponsorshippg.New(db, guard, checkout, canonicalSocial, canonicalPostAssociations, sponsorshippg.Config{TreasuryAccountID: accountID, TreasuryWalletID: walletID, Now: now})`.
2. Call `checkout.WithSponsorship(sponsorRepo)` on that exact existing instance.
3. Call sponsorRepo.Ready and construct `sponsorship.NewHandler(sponsorRepo, canonicalPostIdentity)`.
4. Construct `publisherratingpg.New(db, canonicalSocial, ratingConfig)`, call Ready, and use `publisherrating.NewHandler(ratingRepo, canonicalPostIdentity)`.

Constructors return repository and error. The optional canonical associations adapter should be the existing outer timeline adapter so nested relations and media reuse all current visibility checks. Wire sponsorship fulfillment in both combined API and Wallet checkout processes, even if that process does not serve Sphere reads. The actor, authority verifier and repositories must share the exact SQL pool and original bound proof.

Dispatch both ports' Handles(path) before generic publisher/post routes, especially /publishers/leaderboard and /posts/sponsor/current. Public reads still require an installed canonical identity callback; absent required dependencies fail unavailable.

The shared commerce changes add SponsorFulfillment and fixed-product prepare/settle/fulfill/final-authority calls in PayOrder. This exact 8521 composition preserves PIN LockPayment immediately after transaction entry, final CheckPayment after all fulfillment effects, and all livestream-award hooks. Sponsorship contention now has a distinct ErrRetry mapped to commerce.ErrRetry and HTTP 409; permanent state conflicts remain separate.

The host owns a publisherCommerceModules instance, routes the specific endpoints before generic publisher/post dispatch, performs conditional readiness checks, and installs the callback on the existing PIN-protected commerce instance. application.Config exposes Sponsorship and PublisherRatings options. Backend.ProcessPublisherCommerceDue(ctx, limit) runs an idempotent current-hour auction and bounded rating catch-up across configured completed days within the last 31 days.

CLI enablement is explicit: --enable-post-sponsorship requires --sponsor-treasury-account and --sponsor-treasury-wallet. Optional bounds are --sponsor-max-amount, --sponsor-post-order-limit and --sponsor-order-ttl. --enable-publisher-ratings requires --publisher-ratings-earliest-day=YYYY-MM-DD and enables only reaction_only_v1. Supplying module settings without enablement fails configuration validation. --publisher-commerce-interval defaults to one minute, bounded from one second to one hour. In serving mode only, the enabled worker runs once at startup and then polls with a 20-second context and a 100-item batch; constructors start no scheduler. One placement per UTC hour and one receipt per publisher/day remain the durable effect boundaries.

The disposable runner explicitly installs both schemas; its guard includes the twelve domain cases, two restart cases and TestPGBackendPublisherCommerceComposition. The composed HTTP case proves missing payment policy denial, 409 contention rollback, one-use payment proof consumption, exact wallet settlement/replay, current privacy withdrawal and real reaction-derived rating production. No current uncommitted public-board changes are included.

## Validation

The full scripts/verify.sh passes: build, vet, uncached tests and uncached full race tests, source integrity, coverage-plan checks, exact 431-case/57-restart guard and 60-schema disposable wiring checks. PostgreSQL tests compile and explicitly skip locally; actual disposable PostgreSQL and restart evidence belongs to parent CI. Twelve domain guarded cases plus one composed HTTP case cover unpaid rejection, exact ledger conservation, replay/actor isolation, current permissions/privacy, audit rollback, nonblocking and opposite-owner contention, order/bid expiry, current placement and member projections, canonical reaction-only producer counts, aliases/ties, batch rollback/concurrency and two restart assertions. The integrated exact guard contains all thirteen cases and both RestartPersistence cases.

Fixtures use synthetic canonical accounts/wallets, explicit grants/quotas, frozen verifier-consistent clocks and relative synthetic deadlines. They create real disposable commerce holds/settlements and leave no aggregate-only pending/frozen ledger rows. No local server, socket, live provider, real credential, production user or real-person grading was used.
