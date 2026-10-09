# Realm boost purchase and entitlement continuation

This is an owned continuation from `820323e86861b3762391fe72417f1e5c1c87ee83`. It adds canonical, same-transaction realm boost purchase/checkout, active contribution status, leaderboard, and recorded boost XP. It does not claim complete legacy realm parity.

## Lawful contract evidence

The retained AGPL DysonNetwork revision `4ee2d0cebb9da8284a92483fee6dce795bf6947c` supplies:

- `inventory/operation-sources/DysonNetwork.Passport/Realm/RealmController.cs`, lines 117–129 and 571–704: 1–1,000,000 shares; optional currency; POST boosts returns `order_id`, `shares`, `currency`, `amount`; active member and `realms.boosts.manage`; status, grouped contribution leaderboard, and member experience route.
- `ports/passport/social/contract-evidence/RealmService.cs`, lines 20–56: current contributions are summed with an inclusive time cutoff. Shared model/policy definitions were not inspected.
- `ports/passport/artwork/source/ServiceCollectionExtensions.cs`, lines 287–346: the authored `realms.boost` paid-order consumer suppresses duplicate order IDs, records contribution currency/amount, and awards boost-share XP using the order reason.
- `ports/passport/realmboosts/source/RealmExperienceService.cs`: newly retained exact authored file, Git blob `e72a0e3c5906077e93e13a18995f0a528006d73c`, verified against the pinned GitHub response. It evidences 100 XP/share, positive-perk multipliers 1.5/2/2.5, active membership, and reason deduplication. Its root AGPL license is retained alongside its byte/hash manifest.

The separately retained AGPL Solian revision `ffd0838e5f04cb40bea311a39118ebeedad210c1`, `inventory/identity-client-evidence/solian/lib/realms/screens/realm_detail.dart`, lines 36, 1562–1569 and 3358–3527 supplies thresholds 0/10/25/50; default golds; 1 gold or 1,000 points per share; 30 days after applied payment; and the create-order → existing order checkout flow. Exact existing source/license manifests remain authoritative. The new provenance test freezes the complete authored sources used here.

No unlicensed NeTo/Spec/generated protocol, canceled SoSYS/Socommon repository, unlicensed Suki, or excluded fortune corpus was inspected. Imported shared declarations are not copied or treated as verified contract.

## Explicit owned policy

`realmboostpg.New` requires the canonical database/transaction guard, commerce adapter, an existing treasury account and wallet, and a non-nil `OwnedPolicy`. Nothing is seeded or migrated at runtime. Label caps must be explicitly supplied, start at zero, and be nondecreasing. Upstream label-cap values remain unverified. No default cap numbers are invented.

`OwnedPolicy.PerkLevels` must be non-nil. An intentionally empty map means base XP only. A supplied map identifies canonical internal subscription definitions and levels 1–3. The highest configured currently eligible level wins; that selection and mapping are explicit owned choices, not a reconstruction of `RemoteSubscriptionService.GetPerkSubscription`. Canonical row/document identity, non-test active status, already-begun unexpired time, and currently enabled internal definitions are required. Developer-app products are not used as platform perks. Unpaid, queued, testing, expired, cancelled or disabled-definition subscriptions do not grant a multiplier. The input policy is copied to prevent post-construction mutation.

XP uses integer arithmetic (100/150/200/250 per share), has one durable receipt per paid order, and remains after the 30-day boost entitlement expires. The experience endpoint projects this boost ledger only and sends `X-Realm-Experience-Scope: realm.boost.purchase` and `X-Realm-Experience-Complete: false` while preserving its array shape. Other XP sources, total realm-member XP/levels, level thresholds, labels CRUD, and remote subscription policy equivalence remain outside this slice; no successful fake response for those functions is added.

The optional order TTL defaults to one hour and is constrained to one minute–24 hours. Currency spelling normalization is an owned bounded policy: omitted/null/blank means golds; ASCII golds/points are case-insensitive after trimming. This is not claimed as exact excluded Shared normalization behavior. Leaderboard/experience `take` is 1–200. Ties use account ID as a deterministic final key. Canonical public/member visibility also protects boost reads on private realms, strengthening the retained boost handler's unqualified lookup.

## Atomic behavior and authority

Creation captures immutable actor/realm/treasury/currency/shares/amount, creates the canonical commerce order, and records the request key and audit together. Both currencies use exact wallet mills; the maximum request is one million golds or one billion points. No minting, free entitlements, nil payers, external provider requests, or source-fund debit shortcut is present.

Checkout requires current `orders.pay` and `realms.boosts.manage`, the same owning actor, current active realm membership, active realm owner and treasury, and the captured personal treasury wallet. The existing payment-proof adapter is retained by the composed host. Explicit canonical transaction, payer, currency, amount and managed-order checks bind settlement to fulfillment. A failed audit, receipt, permission, membership, funds, payment proof or deadline check rolls back order/hold/settlement/wallet outboxes/contribution/XP together. Repeat checkout uses the existing completed order and does not extend expiry or award again.

Account locks and realm locks cover eligibility through commit. NOWAIT secondary locks avoid reverse order deadlocks with existing checkout and social writers; contention returns the existing retry-required commerce error after rollback. No background replay of untrusted paid events is introduced.

Active shares use `fulfilled_at <= now AND expires_at >= now`, retaining the source's inclusive cutoff, with expiry exactly 30 elapsed days after fulfillment. `socialpg.WithBoosts` and `moderationpg.WithRealmBoosts` replace stale stored `boost_level` in realm detail/list and social/admin promotion gates with the live ledger. No scheduler is needed to remove expired promotion privileges. Accepting a pending elevated-role invitation rechecks the same live boost threshold, so an invitation cannot preserve an expired promotion entitlement. Existing joined roles and their effective permissions remain unchanged; normal-role invitations still work after expiry. When the feature is enabled, old manually seeded levels are not treated as paid contributions.

## Installation and integration

Apply `internal/realmboostpg/schema.sql` after social, authority, canonical wallet and commerce schemas, only through the explicit owned migration workflow. The separate host/registry delta wires `Config.RealmBoosts`, router/readiness and ten mandatory synthetic PostgreSQL cases, including the before/after restart fixture `realm-boost-record.json`. Group/dependency JSON is delivered as an unactivated review draft: the coordinator must regenerate it against the current integration tree and independently review new cases/markers before activation. The feature is unavailable without its explicit policy. Backend construction performs no I/O.

The host helper supplies current bearer proof and the same transaction guard to the module and injects its live entitlement projection into social and administrative moderation. Public optional reads do not turn an invalid supplied token into anonymous success.

### Standalone backend configuration

The standalone backend requires both `--enable-realm-boosts` and `--realm-boost-config=/absolute/path/to/policy.json`. Supplying only one is rejected. The normal no-`--serve` command validates this policy without connecting to the database, opening a listener, issuing credentials, or creating accounts/wallets. Serving still requires the existing explicit database, authority, enum, and payment-PIN configuration described in `authenticated-backend-host.md`.

The JSON file must be a regular, non-symlink file of at most 64 KiB. Its exact supported fields are:

- `version`: required integer `1`.
- `treasury_account_id`, `treasury_wallet_id`: required existing canonical UUIDs; readiness and checkout verify current activity and personal wallet ownership.
- `label_caps`: required array of exactly four integers, starting with zero, nondecreasing and each at most 1,000,000. These are explicitly selected owned policy, not inferred upstream defaults.
- `perk_levels`: required object mapping at most 1,000 explicit canonical internal subscription identifiers to integer levels 1–3. `{}` deliberately selects base XP; missing/null is rejected. Identifiers follow the server's nonblank, at-most-256-byte policy.
- `order_ttl_seconds`: optional integer 60–86,400; omission uses the server's one-hour default. Explicit null is invalid.

Unknown fields, case aliases, duplicate decoded keys, trailing JSON, null required values, fractional integers and invalid bounds are rejected. Price, currency rates and XP multipliers are not configurable through this file. The CLI calls the same pure configuration validator as the repository constructor. Configuration errors do not print file contents, account IDs, or credential values. No configuration file is discovered implicitly.

The restart fixture uses the shared before/after marker helpers. Before restart it exclusively saves original resource IDs, synthetic bearer proof, public verification key and observed contribution/XP values into the private disposable marker. After restart it reconstructs adapters only, authenticates the original proof, checks the saved rows, replays the original order, and verifies unchanged settlement, timestamps, XP and both balances. No new account, wallet, grant, token or unrelated fixture is created after restart. Disposable marker contents must never be published or uploaded as artifacts.

## Verification limits

Local no-socket checks cover source integrity, policy arithmetic, route parsing, strict JSON/duplicate-field rejection, fail-closed configuration, full normal/race/build/vet, PostgreSQL-16 SQL/PLpgSQL grammar and static SQL bind arity. PostgreSQL cases compile but skip without the marked disposable database. Actual money conservation, SQL locks, constraints, atomic rollback and durable restart require the coordinator's synthetic PostgreSQL CI; local package `ok` is not a database pass. No production/provider/socket operation was attempted.
