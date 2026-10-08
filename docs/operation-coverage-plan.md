# Backend operation coverage plan

Prepared 2026-10-08 against `Solsynth/DysonNetwork@4ee2d0cebb9da8284a92483fee6dce795bf6947c` and replacement snapshot `fcb3449501070745bbce4a0a3ba43a4df9ccf9bc`.

Full-program completion plan; static surface mapping for the five remaining DysonNetwork C# services, plus cross-backend integration work. This is not a claim of complete or production-ready Go replacement.

Current implementation and exact-commit test evidence are tracked in [runtime-flow-acceptance.json](runtime-flow-acceptance.json). The per-operation plan below preserves the original research checkpoint; its old implementation/blocker labels are historical.

## What is mapped

- 98 controller files containing 99 controller class declarations and 818 static HTTP attributes. These are not 818 verified runtime endpoints.
- Static attributes by service: Develop 63, Messager 74, Passport 255, Sphere 331, Wallet 95.
- 22 licensed RPC implementation files, 99 override declarations; the remaining RPC-named file is a test, not a service.
- 25 job-named files, 34 literal job registrations, and 5 database files with 136 DbSet declarations.
- Runtime endpoint total and full-backend completion percentage remain unknown. Zero production services are certified complete by this plan.

See [the machine-readable plan](operation-coverage-plan.json) for every operation, action signature, source line, declared authorization, observed error code/status, direct database member, gate and family.

## Current integrated checkpoint

The integrated workspace has thirteen local HTTP operations: watch preference GET/PUT, read-only exchange catalog, badge manifest/icon, app discovery, miniapp package download, three NodeInfo routes, host-meta, bot commands and wallet product catalog. Eight are explicit synthetic compatibility candidates (apps 1, package 1, NodeInfo 3, host-meta 1, bot commands 1, wallet product catalog 1). Production service completion remains zero.

- Verification: The integration lead reports final source/Go/oracle checks pass, with 182 top-level race tests passed and 30 database tests explicitly skipped. The source gate covers 441 entries and 11 mutation checks. Bot commands uses a documented typed model and real SQL; wallet product catalog matches 383 configuration-reachable .NET cases plus five direct-definition diagnostics and 2,967 naming facts. Documentation/index validation is independent of implementation runtime validation.
- Pending: The 30 actual-PostgreSQL cases, six restart checks and current process HTTP checks have not run in the socket-restricted executor. The recovery checkpoint has been published; publication of this reconciled tree and exact-commit CI are being prepared. Earlier database success does not validate this new code.

## Independently published documentation

Positive partial contract evidence for permission architecture, OAuth/login/session concepts, routing and tag models. Live pages display July25 and are not pinned deployment contracts. Precise gaps and qualified negative searches are recorded separately.

See [the public-documentation audit](public-api-documentation-audit.md) for official links, retrieved factual evidence, version drift and exact remaining gaps.

## Evidence boundaries

- 818 counts static HTTP verb attributes, not distinct runtime endpoints or verified features.
- Service route candidates combine literal attributes; gateway aliases, ASP.NET constraint semantics, action discovery and global filters remain unverified.
- Observed body facts omit effects hidden in helper/service/dependency methods. A GET may mutate, and absence of a side-effect token is not proof of purity.
- Class and action authorization declarations are recorded separately. No declaration does not establish anonymous access.
- RPC override names/signatures describe licensed implementations only; they do not establish wire paths, protobuf numbers, serialization, streaming or compatibility.
- Job filenames omit classes defined in differently named files. Literal registrations reveal additional jobs; hosted workers/listeners require separate tracing.
- All source snapshots verify against expected Git blob SHA and SHA-256; Fortune corpus bytes are intentionally absent.
- No appsettings, production databases/accounts, SoSYS requests, excluded schemas, generated code or provider operations were used.

## Genuine blockers and bounded next steps

### B-AUTH-MODELS (missing_contract_evidence)

Eligible owned docs, licensed callers and authored public pages already establish token/route/permission concepts and several model fields. Remaining authority transport/freshness/failure behavior, full shared serialization, specific production enum/default values and inherited filters are unresolved.

Next step: Reconcile the precise missing fields/behaviors against further independently eligible authored clients or approved specifications. Use the new public-doc audit to preserve established facts and live-guide drift; do not demand a replacement for evidence already available.

### B-EXCLUDED-CONTRACTS (license_or_access_constraint)

Excluded source/schemas/descriptors/generated bindings and unresolved embedded corpus rights cannot be reused; SoSYS denial and cancelled reads remain in force.

Next step: Use only new explicit authorized/licensed interface evidence or approved original replacements. Preserve missing facts if that evidence cannot be supplied.

### B-RUNTIME-EVIDENCE (executor_and_release_gate)

Source/Go/oracle checks pass and 182 top-level race tests pass; 30 database tests were skipped. Thirty actual PostgreSQL cases, six restart checks and current process tests await execution. Recovery is published; reconciled-tree publication and exact-commit CI are in preparation.

Next step: Execute the already-wired suite in the authorized CI publication workflow and assess the exact resulting commit. Do not reinterpret local skips as actual database passes.

### B-REMAINING-DOMAINS (ordinary_unfinished_implementation)

Most remaining action/domain workflows still need implementations and tests. This is not an access blocker: the retained licensed service/controller evidence supports substantial local work.

Next step: Choose one transactional family (for example Messager recovery, Sphere taxonomy or Wallet ledger), finish its domain/repository/error/rollback acceptance, then move to the next. Avoid accumulating more candidate-only endpoints.

### B-CROSS-SERVICE-RELEASE (unverified_integration_and_rollout)

No end-to-end client, transport/event/storage interoperability, complete migration/reconciliation, rollback or production service rollout is established.

Next step: After one family closes its contracts, run controlled cross-service fixtures and a disposable-data migration/recovery rehearsal before enabling it outside synthetic mode.


## Current acceptance work and optional future implementations

### Close NodeInfo and host-meta acceptance

GET /.well-known/nodeinfo and GET /.well-known/host-meta are config-backed response families; NodeInfo 2.0/2.1 add PostgreSQL statistics.

- Evidence: Licensed NodeInfoController.cs and WebFingerController.cs in inventory/operation-sources; source hashes in manifest.json.
- Next implementation: The final combined local aggregate passed. Close current process and exact-commit CI checks, actual-PostgreSQL/restart checks for statistics, and gateway-route evidence. Host-meta XML attribute escaping is an explicit behavior improvement; no production-ready claim follows.
- Tests: Exact content type/body/URL output, empty and explicit domain, route/method behavior; for statistics, actual PostgreSQL counts, enum guard, deleted rows and restart.
- Remaining release blockers: Global filters/access policy, production publisher-type mapping and gateway aliases. XML domain handling changes need an explicit compatibility note.

### Close miniapp package acceptance

GET /api/miniapps/{slug}/package is a narrow binary/redirect operation. It is not side-effect-free: upstream increments the download counter before redirect or object fetch.

- Evidence: MiniAppPublicController.DownloadPackage, the licensed local service/storage evidence and Develop migration snapshot.
- Next implementation: Close actual-PostgreSQL/concurrent counter/restart/process validation on the locally implemented candidate, then resolve published-status and global-policy evidence. Do not substitute unproven list/detail shared-model endpoints.
- Tests: Not-found/no-package; increment order; redirect and stream headers; storage error/cancellation; concurrent counts and restart; explicit production policy gate.
- Remaining release blockers: Published status/soft-delete/global policy, real object-store ACLs and migration/rollback.

### Close acceptance on recovered exchange/badge handlers

Exchange and badge handlers are recovered and fixed locally, with their .NET oracles reproduced. Their family-level production gates remain open.

- Evidence: Read-only earlier workspace modules, their pinned provenance and .NET oracle fixtures; current recovery is owned by the main integration work.
- Next implementation: The modules are recovered in the integrated workspace. Close process-smoke and exact-commit CI checks; keep no-money-movement and asset provenance distinctions explicit.
- Tests: Decimal/configuration/casing edge cases, no side effects, badge/null/cache/error/icon fixtures and exact process restart behavior.
- Remaining release blockers: Global policy/gateway compatibility and explicit asset data permission notes; successful bounded tests do not complete Wallet or Passport.

### Close implemented wallet product catalog acceptance

GET /api/wallet-products/catalog is implemented from the owned five-field DTO and documented public access intent, with immutable explicit configuration.

- Evidence: WalletProductController.cs:50; WalletProductService.cs GetGoldsResupplyPackDefinition; Models/WalletProductOptions.cs. All retained with exact blob/SHA-256.
- Next implementation: Close process and exact-commit CI evidence on the existing candidate. No additional catalog implementation is needed.
- Tests: 383 configuration-reachable .NET cases, five direct-definition projection diagnostics and 2,967 naming facts pass; preserve exact ordered JSON/decimal and detached-copy checks.
- Remaining release blockers: Unknown symbolic missing-configuration Currency default, full deployed middleware/config lifecycle and actual process/CI checks. Checkout/payment is separate.

### Taxonomy domain/repository family from authored public docs

The official published Tag Ownership API supplies independent partial model and behavior facts beyond owned source, supporting concrete ownership/protection/event/quota policy work.

- Evidence: https://kb.solsynth.dev/solar-network/developers/tag-ownership/ (live read 2026-10-08, displayed July25), pinned PostTag/PostCategory controllers and permitted service/migration follow-on sources. See public-api-documentation-audit.md.
- Next implementation: Implement a bounded taxonomy domain/repository acceptance family after reading its eligible owned services/migrations. Do not add another guessed full-model HTTP projection.
- Tests: Normalization, ownership/protection, event cutoff, quota, concurrent updates, count/page and subscription consistency using synthetic PostgreSQL data; retain current-source precedence over dated guide prose.
- Remaining release blockers: Complete Usage/inherited/relationship JSON, category and quota wrapper model, current authority/filter/error behavior and gateway aliases. These exact gaps remain, not all model facts.

## Delivery phases

### P0: Establish the evidence ledger

Freeze the permitted baseline and source hashes. Map HTTP actions, local RPC implementation methods, scheduler registrations and DbSets. Keep missing contracts and per-operation implementations as separate states; this plan provides the initial ledger.

Exit evidence: 98 controller files reconcile to 818 static attributes; source integrity gate passes; no unsupported runtime-endpoint or production-completion claim.

### P1: Finish bounded lawful runnable families

Close runtime acceptance on recovered catalog/badge implementations and locally completed NodeInfo/miniapp package candidates. Do not expand candidate-only surface indefinitely while authority/schema/wire facts remain unresolved.

Exit evidence: Each claimed operation has a source-backed synthetic fixture, HTTP error/binding checks, actual process smoke, relevant persistence/restart result and a precise production-gap statement.

### P2: Implement local domain and persistence

Parallelize Develop project/product/board rules, Passport notable-day/realm/progression rules, Sphere taxonomy/post/feed selection, Messager message/room transactions and Wallet ledger/order/subscription state machines. Do not wait on auth to write isolated domain/repository tests.

Exit evidence: Per-family success/failure/concurrency tests exercise real disposable PostgreSQL; no in-memory substitute is counted as persistence; full exposed shared models remain gated until legally established.

### P3: Resolve authority and shared contracts

Use independently permitted interface evidence or rights-holder-approved specifications for missing claims, permission strings, shared model serialization, enums and wire descriptors. Keep unavailable facts explicit; do not import excluded sources or guess values.

Exit evidence: Authenticated and anonymous differential tests, permission denial matrix and interoperable transport fixtures pass without excluded artifacts.

### P4: Connect events, jobs, storage and providers

Wire tested local domains to cache/NATS/websocket/RPC adapters, object-store/media pipelines and controlled provider fixtures. Cover registered jobs and hosted workers, not just HTTP methods.

Exit evidence: Integration tests prove replay/idempotency, retry/ack, multi-replica coordination, storage cleanup and crash/restart behavior; no live provider side effect was required.

### P5: Verify full services and clients

Reconcile the action ledger with executable ASP.NET/gateway discovery and complete authorized client scenarios across existing Go identity/gateway/storage/notification services.

Exit evidence: Every in-scope operation is matched to an implementation, passing evidence or an explicit agreed omission; route/model/authority/event/storage parity is validated end to end.

### P6: Rehearse migration and release

Build containers and run synthetic data migration/reconciliation, rollback, failure recovery, configuration/health/readiness and load tests before a separately authorized cutover.

Exit evidence: Exact published commit has passing required CI, artifact/source manifests and a tested rollback plan. No production-complete claim while any required gate remains pending.

## Family coverage and next work

### develop.apps_products (26 static HTTP attributes)

Public app discovery/details/products and private app/product CRUD, board widgets, payloads, secrets and rotations.

- Work available now: Extend the existing PostgreSQL six-field discovery candidate. Implement licensed app/product service logic as isolated domain units; pin local model snapshots and transaction boundaries before other read projections. Keep app credential operations behind detached synthetic providers.
- Blocked compatibility facts: Owned APP_PRODUCTS documentation and an authored Solian payment consumer establish partial product field evidence. Remaining complete entity/relationship/inherited-field serialization, enum/storage mapping, global filters, ownership and remote publisher/account/order contracts are unresolved; this is not absence of all product model evidence.
- Acceptance: Discovery search/count/page and null fields agree with legacy; app/product uniqueness, state changes and ownership have transaction tests; secrets never leak into public projections; admin actions deny unauthorized principals.

### develop.developers (5 static HTTP attributes)

Developer lookup, stats, listing, enrollment and quota query.

- Work available now: Port quota arithmetic and local enrollment prerequisites from licensed services with explicit quota inputs, then test locally. Build repository seams without substituting guessed identities or remote publisher data.
- Blocked compatibility facts: Remote publisher hydration and membership; shared developer/profile models, quota constants and enrollment permission strings.
- Acceptance: Boundary quotas, suspended/deleted developers, duplicate enrollment, public/private fields, count/page and dependency-unavailable behavior are covered.

### develop.projects (5 static HTTP attributes)

Private developer project list/detail/create/update/delete.

- Work available now: Implement isolated PostgreSQL project lifecycle with synthetic owner/member identities once licensed service/migration field evidence is pinned. Exercise foreign-key and duplicate-name behavior.
- Blocked compatibility facts: Owner/member role semantics, developer visibility, shared model serialization and deletion cascades/soft-delete lifecycle.
- Acceptance: Cross-owner denials, mutation rollback, concurrent duplicates, deletion/recovery, pagination and exact null/validation semantics.

### develop.bots (17 static HTTP attributes)

Bot metadata, public developer/chat/commands, private bot CRUD, keys/rotation and chat manifest.

- Work available now: The public bot-command read is implemented as a typed, documented, real-SQL synthetic candidate. Close its actual PostgreSQL/restart/process evidence. Remaining public bot/developer/chat projections and private credential/chat workflows still need separate local-domain and contract work; do not duplicate the command candidate.
- Blocked compatibility facts: Shared bot/developer JSON, account-creation RPC, chat manifests, key storage/rotation/revocation and permissions; no real credentials may be generated by a test.
- Acceptance: Public responses omit secrets; bot ownership and revoked keys fail closed; command defaults, manifest validation and transactional key rotation are tested with inert test values.

### develop.boards (1 static HTTP attributes)

Public app-board feed, distinct from app CRUD board actions and Passport account boards.

- Work available now: Implement local board-widget selection and ordering against synthetic repository rows, retaining the per-account/private board distinction. Reuse existing board transaction lessons without assuming matching schemas.
- Blocked compatibility facts: Remote account/widget payload permission checks, shared board value shapes, script/payload processing and visibility.
- Acceptance: Widget order, duplicates, missing app, hidden/deleted records, malformed payload and cross-account access behavior are covered.

### develop.miniapps (9 static HTTP attributes)

Public miniapp discovery/detail/package download and private package upload/CRUD.

- Work available now: The bounded package-download candidate is implemented locally with persistent SQL counter and explicit synthetic ZIP storage. Its build/unit/race checks passed in the integrated workspace; actual PostgreSQL, restart and HTTP process validation remain pending. Next, close those checks before extending upload or full-model list/detail.
- Blocked compatibility facts: Published status mapping, shared miniapp/project/developer JSON for list/detail, soft-delete/global filters and production object-store/redirect ACL policy. Upload adds authenticated ownership and archive safety requirements.
- Acceptance: No-match and no-package errors; counter behavior on redirect and stream failures; synthetic persistence/restart; response headers, filenames, cancellation; 5 MiB package cap and manifest validation before upload acceptance.

### messager.messages (28 static HTTP attributes)

74-attribute Messager surface includes message list/detail/send/edit/delete/recovery, pins/reactions and encrypted payload workflows; this family covers ChatController actions.

- Work available now: Build message query/recovery repository and hydration pipeline around the existing range/membership/formatting policies using synthetic data. Preserve ordering/cursors and pre-resolved identity boundaries. Trace side effects before any transport exposure.
- Blocked compatibility facts: Shared message/room/member/file JSON and protobuf envelopes, permission/membership/E2EE state, attachment hydration, notification/event ordering and deleted-message representation.
- Acceptance: History and recovery gaps, sequence allocation, concurrent writes, membership changes, edits/deletes, tombstones, encrypted payload preservation, duplicate delivery and hydration failure tests.

### messager.rooms (37 static HTTP attributes)

Room CRUD, member administration, invites, direct/group/realm room workflows and per-member state.

- Work available now: Implement local room/member invariant checks and transaction policies from licensed room services. Test role transitions and idempotent join/leave against synthetic principals before adding adapters.
- Blocked compatibility facts: Shared room/member serialization, realm permission RPC, E2EE key/session contracts, fanout, file ACLs and remote profile hydration.
- Acceptance: Cross-room account separation; owner removal constraints; simultaneous joins/leaves; realm visibility; rollback; E2EE state transitions; consistent counters and websocket delivery.

### messager.calls (9 static HTTP attributes)

Real-time call lifecycle, participant state, token and provider room interactions.

- Work available now: Define an inert local realtime-provider adapter and port controller/service lifecycle decisions plus stale-session cleanup. Keep actual provider tokens and network connections out of tests.
- Blocked compatibility facts: LiveKit/provider credential/token contract, participant membership authority, websocket events, call persistence and production provider configuration.
- Acceptance: Join/leave/rejoin, duplicate callbacks, expired session cleanup, denial before provider calls, provider timeouts and crash recovery.

### passport.badges (2 static HTTP attributes)

Badge manifest and icon download.

- Work available now: The bounded badge handler and assets are recovered locally, defects fixed and the existing .NET oracle reproduced according to the integration lead. Close the remaining actual-process/exact-commit CI checks; do not treat its manifest/icons as completion of Passport.
- Blocked compatibility facts: Global policy/gateway routing; selected icon-set licensing and exact-vs-original-asset distinction must stay explicit. Existing oracle proves a bounded handler, not Passport.
- Acceptance: Manifest serialization, icon absence, content type, cache behavior, safe filenames, fixture asset provenance, integration command and restart smoke.

### passport.fortunes (2 static HTTP attributes)

Static fortune catalog and optional-language random selection.

- Work available now: Retain only route/action/error metadata and a replacement-data interface plan. Do not reuse the embedded sayings corpus without independent rights evidence. An original/user-supplied corpus would be a behavior/content change requiring an explicit parity note.
- Blocked compatibility facts: Embedded quotations have unresolved permission; AGPL on controller source does not establish rights to every quotation. Exact random algorithm parity is also unverified.
- Acceptance: Approved corpus provenance; language filtering and no-match errors; distribution/selection tests; exact data changes disclosed. No copied corpus enters the Go build.

### passport.accounts_presence (99 static HTTP attributes)

Account/admin/current/public/statistics/event, presence/artwork, passkit, domain trust, IP lookup and account rewind.

- Work available now: Resolve ownership with existing Stargate first. Port only remaining Passport-owned board/profile extras, presence policies and local statistics; implement external presence and IP providers as inert interfaces and deterministic fixtures.
- Blocked compatibility facts: Stargate-vs-Passport domain split, shared account/profile JSON, trust/credential state, geography/provider contracts, remote rewind/presence RPC and notification formats.
- Acceptance: No duplicate identity backend; privacy filters; account separation; optional/deleted state; deterministic presence expiry; no real pass signing or external notifications in tests.

### passport.progression (25 static HTTP attributes)

Progression/admin, affiliation spells and name-change cards.

- Work available now: Port local progression/eligibility/ranking arithmetic and consumption invariants from licensed services; retain existing username policy. Use explicit event inputs and synthetic credit balances.
- Blocked compatibility facts: Shared achievement/experience enums, remote account writes, card issuance/use authority, event delivery and transactional award idempotency.
- Acceptance: Thresholds, duplicated activity, award revocation, simultaneous redemptions, rollback, updated username uniqueness and reconciliation with event replay.

### passport.social_realms (43 static HTTP attributes)

Friend relationships and realm public/private/admin operations.

- Work available now: Separate already-Go relationship ownership from remaining realm lifecycle. Implement synthetic realm membership/role/quota invariants from licensed local services with transaction and concurrency tests.
- Blocked compatibility facts: Shared role/status constants, Stargate relationship behavior, remote chat/publisher creation and event contracts, authorization/visibility policy.
- Acceptance: Blocked/private relationships, pending/expired invites, role escalation denials, owner transfers, quota boundaries, deletion cascade and multi-service partial failure recovery.

### passport.location_nfc (32 static HTTP attributes)

Meetups, location pins, nearby and NFC/admin.

- Work available now: Build deterministic local lifecycle and query policies using synthetic coordinates, clocks and NFC values; trace hosted workers separately from HTTP attributes.
- Blocked compatibility facts: Privacy/consent, precision and retention rules, shared meet/location/NFC schemas, signed NFC ownership and remote notification/call semantics.
- Acceptance: Visibility and participant access; distance/time boundaries; expiration; no disclosure of synthetic or real location across accounts; idempotent NFC claims and worker restart.

### passport.tickets_exams (47 static HTTP attributes)

Support tickets/on-call and examination, question/group/trial admin/user workflows.

- Work available now: Implement isolated ticket state and exam/trial grading policies from licensed bodies. Use seeded synthetic assignments and questions, then add PostgreSQL transaction adapters.
- Blocked compatibility facts: Ticket/comment attachments and privacy, role/on-call authority, question-bank access, shared trial/status models, notifications and grading timestamp rules.
- Acceptance: No answer leakage before authorization; trial expiry; repeated submission; concurrent grade updates; ticket assignment/closure and attachment ACL tests.

### passport.notable_days (5 static HTTP attributes)

Public notable-day list/detail and privileged calendar create/update/delete.

- Work available now: Port source-visible region/year/recurrence/layer filtering and ordering into synthetic repository tests. Use a licensed local schema projection before returning any full shared model.
- Blocked compatibility facts: Shared NotableDayTag enum/serialized model, GetMetaPriority behavior and inherited filters; calendar corpus/seed data permissions require separate review.
- Acceptance: Case-insensitive region, year overlap and recurring anchors, layer filtering before count/page, soft deletion and cache purge; create/update validation differences remain explicit.

### sphere.federation_discovery (5 static HTTP attributes)

NodeInfo discovery/2.0/2.1 and WebFinger/host-meta.

- Work available now: The three-route NodeInfo and config-only host-meta candidates are implemented locally behind explicit synthetic opt-in/default denial. Host-meta renders no database result; XML attribute escaping is a documented improvement. Complete actual PostgreSQL, restart, process and exact-commit verification, then resolve production contracts rather than expanding synthetic surface.
- Blocked compatibility facts: NodeInfo advertises /nodeinfo/* but source action routes are /.well-known/nodeinfo/*; gateway aliases are unverified. Shared publisher enum/filter semantics affect statistics. Federation JSON naming/content types must not inherit an assumed global serializer.
- Acceptance: Discovery URLs, content negotiation, default/explicit domain, exact statistics predicates, enum guard, wildcard/case and missing WebFinger resource, PostgreSQL count/restart, no outbound federation calls.

### sphere.federation (55 static HTTP attributes)

Actors, inbox/outbox, follows/quotes, realm federation, keys and moderation.

- Work available now: Port pure ActivityPub object rendering/selection and local inbox delivery state into synthetic fixtures; inspect licensed protocol-facing bodies and implement controlled test peers only. Keep key generation and federation transmission disconnected.
- Blocked compatibility facts: Signature/key lifecycle, shared actor/object models, cross-domain trust, delivery acknowledgments/retries, remote fetching policy and gateway content negotiation; excluded protocol schemas cannot fill gaps.
- Acceptance: Signed/unsigned and expired requests; actor ownership; duplicate inbox delivery; malformed/oversized objects; retry/backoff; moderation; safe fetch boundaries; atomic outbox recovery with no third-party traffic.

### sphere.taxonomy (41 static HTTP attributes)

Category/tag discovery, lookup/subscriptions, tag ownership/protection and admin actions.

- Work available now: Published official authored Tag Ownership API documentation now supplies eight tag field/type facts and ownership/protection/event/quota behavior. Combine that dated, partial model evidence with pinned owned controllers and then inspect eligible service/migration behavior for substantive domain/repository tests; public docs do not establish full shared entity output.
- Blocked compatibility facts: Exact Usage/inherited/relationship serialization, category output and quota wrapper remain partial. Published admin-policy text drifts from pinned granular action attributes; role/global authority/filter/current alias behavior still needs reconciliation. See docs/public-api-documentation-audit.md.
- Acceptance: ILIKE semantics, seven-day inclusive popularity window, count before pagination, ownership changes, protected-tag quota, repeat subscribe/unsubscribe and null/error behavior.

### sphere.publishers (71 static HTTP attributes)

Publisher public/private/admin lifecycle, membership, ratings/subscriptions and related statistics.

- Work available now: Port local publisher query, role/quota and subscription policies with synthetic account identities. Use the licensed migration/model evidence to construct exact read projections only where all exposed fields are known.
- Blocked compatibility facts: Publisher shared model, permission/role enums, wallet settlement and account hydration RPC, federation identity and event propagation.
- Acceptance: Visibility, ownership/member boundaries, username uniqueness, ban/deletion, ratings and subscriptions, concurrent limits, settlement idempotency and failure rollback.

### sphere.live_ads (24 static HTTP attributes)

Live-stream and sponsored/advertising workflows.

- Work available now: Implement local stream-state, bidding/auction and schedule policies with fake provider endpoints, deterministic clocks and synthetic wallets; keep external ingress and financial adapters inactive.
- Blocked compatibility facts: Live provider tokens/ingress callbacks, shared status models, publisher roles, monetary precision and settlement/accounting semantics.
- Acceptance: Expired ingress, repeated provider callback, bid ordering/ties, budget/eligibility limits, cancellation and restart; no live provider or money movement.

### sphere.content_utilities (5 static HTTP attributes)

Autocompletion, WebReader and translation.

- Work available now: Port pure selection, key generation, cache-hit/miss and normalization policies from licensed implementations; use synthetic remote responses. Translation source currently keys text+target and ignores supplied source-language at provider call; preserve/document rather than silently improve.
- Blocked compatibility facts: Perk/subscription entitlement, external provider/API compatibility, content fetching/sanitization, cache representation and shared RPC transport.
- Acceptance: Cache byte/key/TTL behavior, empty result handling, authorization before provider calls, timeouts, query boundaries and safe synthetic URL/content fixtures.

### sphere.stickers_surveys (36 static HTTP attributes)

Sticker packs/items and survey/question/answer operations.

- Work available now: Port local validation, survey lifecycle/answer aggregation and sticker ranking policies using synthetic files and identities. Build persistent transaction tests before HTTP adapters.
- Blocked compatibility facts: Shared sticker/survey serialization and enums, file ACLs/object metadata, vote identity/permissions, cache/event semantics.
- Acceptance: Answer privacy, duplicate submissions, survey expiry, question mutation rules, sticker pack ownership, missing media and consistency after restart.

### sphere.timeline (7 static HTTP attributes)

Personal/public/discovery feeds and search/ranking.

- Work available now: Build actual repository visibility selection and pagination around the existing pre-resolved ranking/decay utilities and discovery policies. Use synthetic follow/realm/publisher/block graphs and explicit clocks.
- Blocked compatibility facts: Shared post/publisher serialization, permissions/moderation, search index contracts and hydration across account/file services. Ranking helpers alone do not produce a valid feed.
- Acceptance: Private/draft/deleted content excluded as specified, block rules, cursor consistency, ranking ties, decay boundaries, promotion/sponsorship behavior, count/page and multi-service failure.

### sphere.posts (87 static HTTP attributes)

Posts, actions/admin/stats/collections, watch preferences, account publishing settings and automod.

- Work available now: Continue the tested watch preference persistence slice; add watcher query only with explicit unresolved filters. Implement post/collection/automod domain and repository behavior incrementally with synthetic content, retaining the separation between status, visibility and moderation.
- Blocked compatibility facts: Shared post/content JSON and visibility/authority, media hydration, publisher/account RPC, automod models, cache/search/event ordering, watcher permission and soft-delete policies.
- Acceptance: Create/edit/delete atomicity; draft/privacy and moderation; collection/subscription ownership; reply/quote visibility; watch patch null retention; multi-process concurrency, restart and replay.

### wallet.product_catalog (5 static HTTP attributes)

Wallet product catalog, checkout/admin inspection and application of paid product orders.

- Work available now: The immutable-config GET catalog candidate is implemented with explicit currency or an explicit synthetic default. The real .NET binder/projection oracle covers 383 configuration-reachable cases, five direct-definition diagnostics and 2,967 naming facts. Close process and exact-commit CI evidence; checkout/apply-paid-order remain separate workflows.
- Blocked compatibility facts: Owned documentation establishes the five-field DTO and public/no-auth intent; framework binder/decimal/dictionary naming behavior is now tested. The symbolic no-configuration Currency initializer remains unverified, as do full deployment middleware/configuration lifecycle and provider/payment workflows.
- Acceptance: Retain the passing bounded framework comparison, immutable snapshot and no-provider/no-ledger guarantees. Verify current HTTP process and exact-commit CI; preserve explicit default-currency and synthetic-mode boundaries.

### wallet.ledger (27 static HTTP attributes)

Wallets/pockets/transaction queries, exchange catalog and mutation, balance/transfer requests, funds and contributions.

- Work available now: The read-only exchange catalog is recovered/fixed and its .NET decimal/configuration oracle reproduced according to the integration lead. Close current process/CI acceptance. The remaining ledger requires separate transactional domain implementation using synthetic identities, monetary inputs and idempotency keys; no real funds are connected.
- Blocked compatibility facts: Shared currency/status constants, account/public-wallet authority, full wallet/transaction JSON, double-entry/conservation rules, locking/isolation, transfer confirmation and fund recovery semantics.
- Acceptance: Exact decimal/no floating loss; balance conservation; overdraft and overflow; concurrent transfer; duplicate requests/events; expiration/rejection rollback; read models reconcile with ledger; catalog never performs exchange.

### wallet.orders_payments (15 static HTTP attributes)

Order creation/detail/actions and payment administration/provider callbacks.

- Work available now: Implement local order/inbound-order state machines and provider-event normalization using signed synthetic fixtures and inert provider adapters. Enumerate all transitions and attach ledger operations transactionally.
- Blocked compatibility facts: Provider validation/current external contracts, shared order enums, entitlement/account resolution, webhook idempotency, money rounding, credentials and cross-service event contract.
- Acceptance: Duplicate/out-of-order callbacks, invalid signatures, partial failure, order reuse rules, refund/cancellation outcomes, exactly-once ledger effect despite retry and restart; no live payment execution.

### wallet.subscriptions_gifts (38 static HTTP attributes)

Subscription/group catalog, billing/activation/provider checkout, subscription admin and gifts/coupons.

- Work available now: Pin licensed catalog service/options and local definitions before config or DB-backed catalog DTOs. Port eligibility/renewal/gift-cleanup decision logic with explicit clocks, synthetic state and inert providers.
- Blocked compatibility facts: Shared subscription/currency/period enums and display configuration, entitlement remote calls, gift privacy/ownership, payment provider contracts, duplicate-event lifecycle and renewal schedule timezone.
- Acceptance: Catalog/group order and display/null behavior; activation/upgrade/proration; duplicate redeem; expiration; recurring renewal retries, gift cleanup and entitlement reconciliation after crashes.

### wallet.merchants (9 static HTTP attributes)

Merchant management and settlement.

- Work available now: Implement synthetic merchant ownership and settlement batch planning from licensed controller/service/job evidence after ledger invariants are fixed. Model publisher/account adapters without contacting them.
- Blocked compatibility facts: Publisher authority, merchant/account models, settlement currency/rounding and schedule, wallet/transaction RPC and audit/reconciliation rules.
- Acceptance: Cross-publisher denial, repeated settlement, cutoff boundaries, concurrent runs, partial batch recovery, balanced ledger and immutable audit data.

### wallet.reporting (1 static HTTP attributes)

Wallet/payment administrative statistics.

- Work available now: Implement PostgreSQL report predicates and aggregation from pinned controllers with synthetic ledgers, explicit time windows and numeric types; test without exposing any real account data.
- Blocked compatibility facts: Admin permission strings, shared status/currency enums, filter/timezone policy, consistent accounting projections and production data schema.
- Acceptance: Unauthorized reports denied; count/sum reconcile to ledger; missing/deleted rows and date boundaries; no money precision loss and consistent concurrent snapshot.

## Cross-backend integration

- **Stargate identity**: Existing Go replacement is documented in the prior compatibility inventory. Verify pinned licensed ownership and account/session/profile/relationship interfaces; do not duplicate already-migrated identity from the username helper. No new repository access is implied by this plan.
- **Blade gateway and realtime entrypoint**: Prove service-prefix rewriting, special discovery/federation aliases, health gating, websocket fanout and discovery/TLS against the authorized pinned gateway before changing public route claims.
- **DysonFS storage/media**: Integrate permitted file/ACL/quota/object interfaces and migration semantics with synthetic storage. Existing Go storage does not remove media/native-library or quota dependency tests.
- **MetoerPush notifications/scheduling**: Verify cache-envelope/event casing, JetStream versus in-process queue behavior, delivery/ack/retry and restart semantics. Do not rewrite Ring blindly or infer durable delivery from service existence.
- **Other backend repositories and unavailable systems**: The root inventory is a scoped source map, not the entire organization. Reconcile licensed, pinned already-known backend ownership and explicitly authorized systems. SoSYS access denials, cancelled reads and missing permission remain genuine blockers; do not retry through another identity or route.
- **Clients**: Use existing clients only as authorized compatibility consumers. Client rewrites, branding/assets and unrelated frontend work are outside this backend Go port; test route, session, pagination and event behavior without copying unlicensed artifacts.

## Release gates

- **G-LICENSE**: Every reused implementation, asset and data corpus has verified applicable permission at its pinned revision. Excluded NeTo/Spec implementations, schemas, descriptors and generated bindings remain unused; no access-denial workaround.
- **G-CORPUS**: Independently establish rights for embedded sayings/calendar/seed/asset material or use approved original replacement data with an explicit behavior/content-change note.
- **G-ROUTING**: Execute framework and gateway differential tests for service routes, aliases, constraints, binding defaults, trailing slash, percent encoding, methods/405 and content negotiation. Static attributes alone are insufficient.
- **G-JSON**: Pin every exposed DTO and error shape, null/omission, dictionary casing, enum numbers, GUIDs, decimals, timestamps, content types, headers and validation/exception serialization from permitted evidence.
- **G-AUTH**: Recover global authentication/fallback policy, session/claim mapping, permission middleware and anonymous/unavailable behavior from eligible evidence. Synthetic bypasses stay explicit and production defaults deny.
- **G-AUTHORITY**: Verify per-resource owner/member/admin/visibility permissions, revocation, role changes, dependency failures and request-scoped identity. No guessed permission strings, numeric roles or real credential issuance.
- **G-SCHEMA**: Pin licensed table/column/default/index/FK and soft-delete/audit behavior for every operation. Use actual disposable PostgreSQL tests for transactions, count/page consistency, isolation, concurrency and restart.
- **G-EVENTS**: Define transport contracts only from permitted licensed callers or authorized documentation; prove cache/event envelopes, keying, protobuf interoperability, ordering, acknowledgment, retry, idempotency and websocket fanout. Unknown wire details remain blocked.
- **G-STORAGE**: Validate synthetic object references, streaming, size limits, archive/media handling, quota and ACL behavior, failure cleanup, cancellation and recovery; no production buckets or files.
- **G-CREDENTIALS**: Test credential/signature state machines only with inert synthetic material. Production key/session/token creation, storage, rotation and revocation require verified policy and separately authorized integration.
- **G-PROVIDERS**: Use controlled fixture providers until external API contracts, error handling, signatures, timeouts, idempotency and required account permissions are verified. No live payment, message, federation or account operation during migration tests.
- **G-JOBS**: Map every scheduler registration, hosted worker and listener; establish timezone/misfire, leases, cluster behavior, retry/ack, clock boundaries, duplicate execution, restart and catch-up semantics.
- **G-CONFIG**: Verify defaults and required settings, secret loading, service names/discovery, URLs, ports, serializer settings and config-binding differences; never infer missing shared constants.
- **G-RECOVERY**: Prove synthetic data copy/reconciliation, migration up/down and rollback, dual-write/cutover ownership, connection loss, process crash/restart and bounded operator recovery.
- **G-DEPLOY**: Build actual services/containers; verify health/readiness, TLS, metrics/logs/traces, graceful shutdown, required native media libraries and exact-commit CI/client smoke before any production-ready claim.

## All HTTP static operation records

The following is the complete controller-attribute index. Every row is a static candidate, not a verified endpoint. `?` in the action template is a route constraint/default, not an invented query parameter. Source links are pinned.

### Develop

| Method | Service route candidate | Action and pinned source | Family |
|---|---|---|---|
| GET | `/api/private/apps/{appId:guid}/products` | [AppProductController.ListProducts:L70](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/AppProductController.cs#L70) | develop.apps_products |
| GET | `/api/private/apps/{appId:guid}/products/{productId:guid}` | [AppProductController.GetProduct:L85](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/AppProductController.cs#L85) | develop.apps_products |
| POST | `/api/private/apps/{appId:guid}/products` | [AppProductController.CreateProduct:L102](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/AppProductController.cs#L102) | develop.apps_products |
| PATCH | `/api/private/apps/{appId:guid}/products/{productId:guid}` | [AppProductController.UpdateProduct:L147](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/AppProductController.cs#L147) | develop.apps_products |
| DELETE | `/api/private/apps/{appId:guid}/products/{productId:guid}` | [AppProductController.DeleteProduct:L204](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/AppProductController.cs#L204) | develop.apps_products |
| GET | `/api/apps/board` | [BoardPublicController.DiscoverBoardWidgets:L40](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/BoardPublicController.cs#L40) | develop.boards |
| GET | `/api/private/bots` | [BotAccountController.ListBots:L88](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/BotAccountController.cs#L88) | develop.bots |
| GET | `/api/private/bots/{botId:guid}` | [BotAccountController.GetBot:L112](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/BotAccountController.cs#L112) | develop.bots |
| POST | `/api/private/bots` | [BotAccountController.CreateBot:L140](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/BotAccountController.cs#L140) | develop.bots |
| PATCH | `/api/private/bots/{botId:guid}` | [BotAccountController.UpdateBot:L216](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/BotAccountController.cs#L216) | develop.bots |
| DELETE | `/api/private/bots/{botId:guid}` | [BotAccountController.DeleteBot:L281](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/BotAccountController.cs#L281) | develop.bots |
| GET | `/api/private/bots/{botId:guid}/keys` | [BotAccountController.ListBotKeys:L319](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/BotAccountController.cs#L319) | develop.bots |
| GET | `/api/private/bots/{botId:guid}/keys/{keyId:guid}` | [BotAccountController.GetBotKey:L344](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/BotAccountController.cs#L344) | develop.bots |
| POST | `/api/private/bots/{botId:guid}/keys` | [BotAccountController.CreateBotKey:L377](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/BotAccountController.cs#L377) | develop.bots |
| POST | `/api/private/bots/{botId:guid}/keys/{keyId:guid}/rotate` | [BotAccountController.RotateBotKey:L411](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/BotAccountController.cs#L411) | develop.bots |
| DELETE | `/api/private/bots/{botId:guid}/keys/{keyId:guid}` | [BotAccountController.DeleteBotKey:L439](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/BotAccountController.cs#L439) | develop.bots |
| GET | `/api/private/bots/{botId:guid}/chat` | [BotAccountController.GetChatConfig:L469](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/BotAccountController.cs#L469) | develop.bots |
| PUT | `/api/private/bots/{botId:guid}/chat` | [BotAccountController.UpdateChatConfig:L488](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/BotAccountController.cs#L488) | develop.bots |
| POST | `/api/private/bots/{botId:guid}/chat/manifest` | [BotAccountController.UpdateManifest:L526](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/BotAccountController.cs#L526) | develop.bots |
| GET | `/api/bots/{botId:guid}` | [BotAccountPublicController.GetBotTransparentInfo:L13](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/BotAccountPublicController.cs#L13) | develop.bots |
| GET | `/api/bots/{botId:guid}/developer` | [BotAccountPublicController.GetBotDeveloper:L27](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/BotAccountPublicController.cs#L27) | develop.bots |
| GET | `/api/bots/{botId:guid}/chat` | [BotAccountPublicController.GetBotChatConfig:L40](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/BotAccountPublicController.cs#L40) | develop.bots |
| GET | `/api/bots/{botId:guid}/commands` | [BotAccountPublicController.GetBotCommands:L63](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/BotAccountPublicController.cs#L63) | develop.bots |
| GET | `/api/private/apps` | [CustomAppController.ListApps:L79](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/CustomAppController.cs#L79) | develop.apps_products |
| GET | `/api/private/apps/{appId:guid}` | [CustomAppController.GetApp:L101](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/CustomAppController.cs#L101) | develop.apps_products |
| GET | `/api/private/apps/{appId:guid}/board` | [CustomAppController.ListBoardWidgets:L128](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/CustomAppController.cs#L128) | develop.apps_products |
| POST | `/api/private/apps/{appId:guid}/board` | [CustomAppController.CreateBoardWidget:L157](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/CustomAppController.cs#L157) | develop.apps_products |
| PUT | `/api/private/apps/{appId:guid}/board/{widgetKey}` | [CustomAppController.UpdateBoardWidget:L196](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/CustomAppController.cs#L196) | develop.apps_products |
| DELETE | `/api/private/apps/{appId:guid}/board/{widgetKey}` | [CustomAppController.DeleteBoardWidget:L236](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/CustomAppController.cs#L236) | develop.apps_products |
| POST | `/api/private/apps` | [CustomAppController.CreateApp:L269](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/CustomAppController.cs#L269) | develop.apps_products |
| PATCH | `/api/private/apps/{appId:guid}` | [CustomAppController.UpdateApp:L307](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/CustomAppController.cs#L307) | develop.apps_products |
| DELETE | `/api/private/apps/{appId:guid}` | [CustomAppController.DeleteApp:L346](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/CustomAppController.cs#L346) | develop.apps_products |
| POST | `/api/private/apps/{appId:guid}/orders` | [CustomAppController.CreateCustomOrder:L380](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/CustomAppController.cs#L380) | develop.apps_products |
| GET | `/api/private/apps/{appId:guid}/board/widgets` | [CustomAppController.ListOwnBoardWidgets:L430](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/CustomAppController.cs#L430) | develop.apps_products |
| POST | `/api/private/apps/{appId:guid}/board/payload` | [CustomAppController.UpdateBoardPayload:L455](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/CustomAppController.cs#L455) | develop.apps_products |
| GET | `/api/private/apps/{appId:guid}/secrets` | [CustomAppController.ListSecrets:L534](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/CustomAppController.cs#L534) | develop.apps_products |
| POST | `/api/private/apps/{appId:guid}/secrets` | [CustomAppController.CreateSecret:L585](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/CustomAppController.cs#L585) | develop.apps_products |
| GET | `/api/private/apps/{appId:guid}/secrets/{secretId:guid}` | [CustomAppController.GetSecret:L645](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/CustomAppController.cs#L645) | develop.apps_products |
| DELETE | `/api/private/apps/{appId:guid}/secrets/{secretId:guid}` | [CustomAppController.DeleteSecret:L686](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/CustomAppController.cs#L686) | develop.apps_products |
| POST | `/api/private/apps/{appId:guid}/secrets/{secretId:guid}/rotate` | [CustomAppController.RotateSecret:L724](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/CustomAppController.cs#L724) | develop.apps_products |
| GET | `/api/apps` | [CustomAppPublicController.DiscoverApps:L26](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/CustomAppPublicController.cs#L26) | develop.apps_products |
| GET | `/api/apps/{slug}` | [CustomAppPublicController.GetCustomAppBySlug:L37](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/CustomAppPublicController.cs#L37) | develop.apps_products |
| GET | `/api/apps/{slug}/products` | [CustomAppPublicController.GetAppProducts:L50](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/CustomAppPublicController.cs#L50) | develop.apps_products |
| GET | `/api/apps/{slug}/products/{identifier}` | [CustomAppPublicController.GetAppProductByIdentifier:L60](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/CustomAppPublicController.cs#L60) | develop.apps_products |
| GET | `/api/develop/quota` | [DevelopQuotaController.GetQuota:L21](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/DevelopQuotaController.cs#L21) | develop.developers |
| GET | `/api/developers/{name}` | [DeveloperController.GetDeveloper:L26](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/DeveloperController.cs#L26) | develop.developers |
| GET | `/api/developers/{name}/stats` | [DeveloperController.GetDeveloperStats:L34](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/DeveloperController.cs#L34) | develop.developers |
| GET | `/api/developers` | [DeveloperController.ListJoinedDevelopers:L54](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/DeveloperController.cs#L54) | develop.developers |
| POST | `/api/developers/{name}/enroll` | [DeveloperController.EnrollDeveloperProgram:L75](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/DeveloperController.cs#L75) | develop.developers |
| POST | `/api/private/miniapps/{miniAppId:guid}/package` | [MiniAppController.UploadPackage:L65](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/MiniApp/MiniAppController.cs#L65) | develop.miniapps |
| GET | `/api/private/miniapps` | [MiniAppController.ListMiniApps:L119](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/MiniApp/MiniAppController.cs#L119) | develop.miniapps |
| GET | `/api/private/miniapps/{miniAppId:guid}` | [MiniAppController.GetMiniApp:L146](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/MiniApp/MiniAppController.cs#L146) | develop.miniapps |
| POST | `/api/private/miniapps` | [MiniAppController.CreateMiniApp:L174](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/MiniApp/MiniAppController.cs#L174) | develop.miniapps |
| PATCH | `/api/private/miniapps/{miniAppId:guid}` | [MiniAppController.UpdateMiniApp:L217](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/MiniApp/MiniAppController.cs#L217) | develop.miniapps |
| DELETE | `/api/private/miniapps/{miniAppId:guid}` | [MiniAppController.DeleteMiniApp:L269](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/MiniApp/MiniAppController.cs#L269) | develop.miniapps |
| GET | `/api/miniapps` | [MiniAppPublicController.DiscoverPlugins:L18](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/MiniApp/MiniAppPublicController.cs#L18) | develop.miniapps |
| GET | `/api/miniapps/{slug}` | [MiniAppPublicController.GetMiniAppBySlug:L40](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/MiniApp/MiniAppPublicController.cs#L40) | develop.miniapps |
| GET | `/api/miniapps/{slug}/package` | [MiniAppPublicController.DownloadPackage:L52](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/MiniApp/MiniAppPublicController.cs#L52) | develop.miniapps |
| GET | `/api/private/projects` | [DevProjectController.ListProjects:L23](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Project/DevProjectController.cs#L23) | develop.projects |
| GET | `/api/private/projects/{id:guid}` | [DevProjectController.GetProject:L33](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Project/DevProjectController.cs#L33) | develop.projects |
| POST | `/api/private/projects` | [DevProjectController.CreateProject:L45](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Project/DevProjectController.cs#L45) | develop.projects |
| PUT | `/api/private/projects/{id:guid}` | [DevProjectController.UpdateProject:L67](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Project/DevProjectController.cs#L67) | develop.projects |
| DELETE | `/api/private/projects/{id:guid}` | [DevProjectController.DeleteProject:L94](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Project/DevProjectController.cs#L94) | develop.projects |

### Messager

| Method | Service route candidate | Action and pinned source | Family |
|---|---|---|---|
| GET | `/api/chat/summary` | [ChatController.GetChatSummary:L230](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatController.cs#L230) | messager.messages |
| GET | `/api/chat/unread` | [ChatController.GetTotalUnreadCount:L255](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatController.cs#L255) | messager.messages |
| POST | `/api/chat/read-all` | [ChatController.MarkAllAsRead:L269](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatController.cs#L269) | messager.messages |
| GET | `/api/chat/{roomId:guid}/subscriptions` | [ChatController.GetRoomSubscriptions:L284](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatController.cs#L284) | messager.messages |
| GET | `/api/chat/accounts/me/subscriptions` | [ChatController.GetMyAccountSubscriptions:L299](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatController.cs#L299) | messager.messages |
| GET | `/api/chat/accounts/me/status` | [ChatController.GetMyChatStatus:L310](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatController.cs#L310) | messager.messages |
| GET | `/api/chat/messages/search` | [ChatController.SearchMessages:L429](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatController.cs#L429) | messager.messages |
| GET | `/api/chat/{roomId:guid}/messages` | [ChatController.ListMessages:L546](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatController.cs#L546) | messager.messages |
| GET | `/api/chat/{roomId:guid}/messages/{messageId:guid}` | [ChatController.GetMessage:L585](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatController.cs#L585) | messager.messages |
| POST | `/api/chat/{roomId:guid}/messages` | [ChatController.SendMessage:L610](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatController.cs#L610) | messager.messages |
| POST | `/api/chat/{roomId:guid}/messages/voice` | [ChatController.SendVoiceMessage:L856](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatController.cs#L856) | messager.messages |
| POST | `/api/chat/{roomId:guid}/messages/placeholder` | [ChatController.SendPlaceholderMessage:L943](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatController.cs#L943) | messager.messages |
| POST | `/api/chat/{roomId:guid}/messages/redirect` | [ChatController.RedirectMessages:L967](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatController.cs#L967) | messager.messages |
| GET | `/api/chat/{roomId:guid}/voice/{voiceId:guid}` | [ChatController.GetVoiceMessage:L1067](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatController.cs#L1067) | messager.messages |
| PATCH | `/api/chat/{roomId:guid}/messages/{messageId:guid}` | [ChatController.UpdateMessage:L1087](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatController.cs#L1087) | messager.messages |
| DELETE | `/api/chat/{roomId:guid}/messages/{messageId:guid}` | [ChatController.DeleteMessage:L1299](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatController.cs#L1299) | messager.messages |
| POST | `/api/chat/{roomId:guid}/messages/{messageId:guid}/reactions` | [ChatController.ReactMessage:L1365](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatController.cs#L1365) | messager.messages |
| DELETE | `/api/chat/{roomId:guid}/messages/{messageId:guid}/reactions/{symbol}` | [ChatController.RemoveReactionMessage:L1416](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatController.cs#L1416) | messager.messages |
| GET | `/api/chat/{roomId:guid}/messages/{messageId:guid}/reactions` | [ChatController.ListMessageReactions:L1447](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatController.cs#L1447) | messager.messages |
| GET | `/api/chat/{roomId:guid}/messages/{messageId:guid}/thread` | [ChatController.GetMessageThread:L1500](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatController.cs#L1500) | messager.messages |
| POST | `/api/chat/{roomId:guid}/sync` | [ChatController.GetSyncData:L1650](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatController.cs#L1650) | messager.messages |
| POST | `/api/chat/sync` | [ChatController.GetGlobalSyncData:L1675](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatController.cs#L1675) | messager.messages |
| POST | `/api/chat/{roomId}/autocomplete` | [ChatController.AutocompleteChat:L1728](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatController.cs#L1728) | messager.messages |
| POST | `/api/chat/{roomId}/devices/me/joined` | [ChatController.DeviceJoined:L1810](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatController.cs#L1810) | messager.messages |
| POST | `/api/chat/{roomId:guid}/pins` | [ChatController.PinMessage:L1868](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatController.cs#L1868) | messager.messages |
| DELETE | `/api/chat/{roomId:guid}/pins/{pinId:guid}` | [ChatController.UnpinMessage:L1897](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatController.cs#L1897) | messager.messages |
| GET | `/api/chat/{roomId:guid}/pins` | [ChatController.ListPins:L1926](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatController.cs#L1926) | messager.messages |
| GET | `/api/chat/{roomId:guid}/bots/commands` | [ChatController.GetBotCommands:L1941](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatController.cs#L1941) | messager.messages |
| GET | `/api/chat/{id:guid}` | [ChatRoomController.GetChatRoom:L111](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L111) | messager.rooms |
| GET | `/api/chat/public/{scope}/{chatSlug}` | [ChatRoomController.GetChatRoomBySlug:L132](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L132) | messager.rooms |
| GET | `/api/chat` | [ChatRoomController.ListJoinedChatRooms:L144](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L144) | messager.rooms |
| POST | `/api/chat/rooms/sync` | [ChatRoomController.SyncJoinedChatRooms:L167](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L167) | messager.rooms |
| POST | `/api/chat/direct` | [ChatRoomController.CreateDirectMessage:L365](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L365) | messager.rooms |
| POST | `/api/chat/{roomId:guid}/confirm` | [ChatRoomController.ConfirmDirectMessage:L473](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L473) | messager.rooms |
| POST | `/api/chat/{roomId:guid}/block` | [ChatRoomController.BlockDirectMessage:L520](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L520) | messager.rooms |
| DELETE | `/api/chat/{roomId:guid}/block` | [ChatRoomController.UnblockDirectMessage:L575](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L575) | messager.rooms |
| GET | `/api/chat/direct/{accountId:guid}` | [ChatRoomController.GetDirectChatRoom:L631](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L631) | messager.rooms |
| POST | `/api/chat` | [ChatRoomController.CreateChatRoom:L701](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L701) | messager.rooms |
| PATCH | `/api/chat/{id:guid}` | [ChatRoomController.UpdateChatRoom:L796](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L796) | messager.rooms |
| POST | `/api/chat/{id:guid}/e2ee/enable` | [ChatRoomController.EnableE2Ee:L1002](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L1002) | messager.rooms |
| POST | `/api/chat/{id:guid}/mls/enable` | [ChatRoomController.EnableMls:L1015](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L1015) | messager.rooms |
| DELETE | `/api/chat/{id:guid}` | [ChatRoomController.DeleteChatRoom:L1087](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L1087) | messager.rooms |
| GET | `/api/chat/{roomId:guid}/members/me` | [ChatRoomController.GetRoomIdentity:L1155](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L1155) | messager.rooms |
| PATCH | `/api/chat/{roomId:guid}/members/me/profile` | [ChatRoomController.UpdateRoomIdentity:L1178](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L1178) | messager.rooms |
| DELETE | `/api/chat/{roomId:guid}/members/me/profile` | [ChatRoomController.ClearRoomIdentity:L1202](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L1202) | messager.rooms |
| GET | `/api/chat/{roomId:guid}/members/online` | [ChatRoomController.GetOnlineUsersCount:L1223](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L1223) | messager.rooms |
| GET | `/api/chat/{roomId:guid}/members` | [ChatRoomController.ListMembers:L1299](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L1299) | messager.rooms |
| POST | `/api/chat/invites/{roomId:guid}` | [ChatRoomController.InviteMember:L1413](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L1413) | messager.rooms |
| GET | `/api/chat/invites` | [ChatRoomController.ListChatInvites:L1583](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L1583) | messager.rooms |
| POST | `/api/chat/invites/{roomId:guid}/accept` | [ChatRoomController.AcceptChatInvite:L1608](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L1608) | messager.rooms |
| POST | `/api/chat/invites/{roomId:guid}/decline` | [ChatRoomController.DeclineChatInvite:L1654](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L1654) | messager.rooms |
| PATCH | `/api/chat/{roomId:guid}/members/me/notify` | [ChatRoomController.UpdateChatMemberNotify:L1680](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L1680) | messager.rooms |
| POST | `/api/chat/{roomId:guid}/members/{memberId:guid}/timeout` | [ChatRoomController.TimeoutChatMember:L1718](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L1718) | messager.rooms |
| DELETE | `/api/chat/{roomId:guid}/members/{memberId:guid}/timeout` | [ChatRoomController.RemoveChatMemberTimeout:L1794](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L1794) | messager.rooms |
| DELETE | `/api/chat/{roomId:guid}/members/{memberId:guid}` | [ChatRoomController.RemoveChatMember:L1862](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L1862) | messager.rooms |
| POST | `/api/chat/{roomId:guid}/members/me` | [ChatRoomController.JoinChatRoom:L1939](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L1939) | messager.rooms |
| DELETE | `/api/chat/{roomId:guid}/members/me` | [ChatRoomController.LeaveChat:L1995](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L1995) | messager.rooms |
| GET | `/api/chat/groups` | [ChatRoomController.ListGroups:L2093](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L2093) | messager.rooms |
| POST | `/api/chat/groups` | [ChatRoomController.CreateGroup:L2104](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L2104) | messager.rooms |
| PATCH | `/api/chat/groups/{groupId:guid}` | [ChatRoomController.UpdateGroup:L2116](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L2116) | messager.rooms |
| DELETE | `/api/chat/groups/{groupId:guid}` | [ChatRoomController.DeleteGroup:L2128](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L2128) | messager.rooms |
| PATCH | `/api/chat/rooms/{roomId:guid}/group` | [ChatRoomController.MoveToGroup:L2140](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L2140) | messager.rooms |
| DELETE | `/api/chat/rooms/{roomId:guid}/messages/{messageId:guid}` | [ChatRoomController.DeleteMessage:L2160](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L2160) | messager.rooms |
| POST | `/api/chat/rooms/{roomId:guid}/members/{accountId:guid}/timeout` | [ChatRoomController.TimeoutUser:L2232](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatRoomController.cs#L2232) | messager.rooms |
| GET | `/api/realms/{slug}/chat` | [RealmChatController.ListRealmChat:L16](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/RealmChatController.cs#L16) | messager.rooms |
| GET | `/api/chat/realtime/{roomId:guid}/participants` | [RealtimeCallController.GetParticipants:L40](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/RealtimeCallController.cs#L40) | messager.calls |
| POST | `/api/chat/realtime/{roomId:guid}/kick/{targetAccountId:guid}` | [RealtimeCallController.KickParticipant:L62](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/RealtimeCallController.cs#L62) | messager.calls |
| POST | `/api/chat/realtime/{roomId:guid}/mute/{targetAccountId:guid}` | [RealtimeCallController.MuteParticipant:L161](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/RealtimeCallController.cs#L161) | messager.calls |
| POST | `/api/chat/realtime/{roomId:guid}/unmute/{targetAccountId:guid}` | [RealtimeCallController.UnmuteParticipant:L169](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/RealtimeCallController.cs#L169) | messager.calls |
| GET | `/api/chat/realtime/{roomId:guid}` | [RealtimeCallController.GetOngoingCall:L177](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/RealtimeCallController.cs#L177) | messager.calls |
| GET | `/api/chat/realtime/{roomId:guid}/join` | [RealtimeCallController.JoinCall:L194](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/RealtimeCallController.cs#L194) | messager.calls |
| POST | `/api/chat/realtime/{roomId:guid}` | [RealtimeCallController.StartCall:L271](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/RealtimeCallController.cs#L271) | messager.calls |
| POST | `/api/chat/realtime/{roomId:guid}/invite/{targetAccountId:guid}` | [RealtimeCallController.InviteToCall:L292](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/RealtimeCallController.cs#L292) | messager.calls |
| DELETE | `/api/chat/realtime/{roomId:guid}` | [RealtimeCallController.LeaveCall:L388](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/RealtimeCallController.cs#L388) | messager.calls |

### Passport

| Method | Service route candidate | Action and pinned source | Family |
|---|---|---|---|
| GET | `/api/admin/accounts/metrics/activity` | [AccountAdminController.GetActivityMetrics:L115](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountAdminController.cs#L115) | passport.accounts_presence |
| GET | `/api/admin/accounts` | [AccountAdminController.ListAccounts:L139](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountAdminController.cs#L139) | passport.accounts_presence |
| GET | `/api/admin/accounts/{identifier}` | [AccountAdminController.GetAccount:L193](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountAdminController.cs#L193) | passport.accounts_presence |
| GET | `/api/admin/accounts/{identifier}/contacts` | [AccountAdminController.GetAccountContacts:L235](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountAdminController.cs#L235) | passport.accounts_presence |
| POST | `/api/admin/accounts/{identifier}/notifications/night-owl` | [AccountAdminController.SendTestNightOwlReminder:L255](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountAdminController.cs#L255) | passport.accounts_presence |
| GET | `/api/admin/accounts/{identifier}/factors` | [AccountAdminController.GetAccountAuthFactors:L294](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountAdminController.cs#L294) | passport.accounts_presence |
| POST | `/api/admin/accounts/{identifier}/verification` | [AccountAdminController.SetAccountVerification:L311](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountAdminController.cs#L311) | passport.accounts_presence |
| DELETE | `/api/admin/accounts/{identifier}/verification` | [AccountAdminController.ClearAccountVerification:L338](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountAdminController.cs#L338) | passport.accounts_presence |
| GET | `/api/admin/accounts/{identifier}/badges` | [AccountAdminController.GetAccountBadges:L354](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountAdminController.cs#L354) | passport.accounts_presence |
| POST | `/api/admin/accounts/{identifier}/badges` | [AccountAdminController.GrantBadge:L370](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountAdminController.cs#L370) | passport.accounts_presence |
| POST | `/api/admin/accounts/{identifier}/badges/{badgeId:guid}/activate` | [AccountAdminController.ActivateBadge:L394](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountAdminController.cs#L394) | passport.accounts_presence |
| DELETE | `/api/admin/accounts/{identifier}/badges/{badgeId:guid}` | [AccountAdminController.RevokeBadge:L406](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountAdminController.cs#L406) | passport.accounts_presence |
| GET | `/api/admin/accounts/{identifier}/board` | [AccountAdminController.GetAccountBoard:L452](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountAdminController.cs#L452) | passport.accounts_presence |
| PUT | `/api/admin/accounts/{identifier}/board` | [AccountAdminController.ReplaceAccountBoard:L463](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountAdminController.cs#L463) | passport.accounts_presence |
| POST | `/api/admin/accounts/{identifier}/board/items/{itemId:guid}/payload` | [AccountAdminController.PushBoardItemPayload:L485](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountAdminController.cs#L485) | passport.accounts_presence |
| DELETE | `/api/admin/accounts/{identifier}/board/items/{itemId:guid}` | [AccountAdminController.RemoveBoardItem:L513](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountAdminController.cs#L513) | passport.accounts_presence |
| POST | `/api/admin/accounts/{name}/credits` | [AccountAdminController.InvalidateSocialCreditCache:L532](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountAdminController.cs#L532) | passport.accounts_presence |
| POST | `/api/admin/accounts/presences/steam/scan` | [AccountAdminController.ScanSteamPresences:L540](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountAdminController.cs#L540) | passport.accounts_presence |
| POST | `/api/admin/accounts/presences/steam/scan/{identifier}` | [AccountAdminController.ScanSteamPresenceForAccount:L549](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountAdminController.cs#L549) | passport.accounts_presence |
| POST | `/api/admin/accounts/presences/steam/scan-by-steam-id/{steamId}` | [AccountAdminController.ScanSteamPresenceBySteamId:L565](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountAdminController.cs#L565) | passport.accounts_presence |
| POST | `/api/admin/accounts/presences/steam/scan/stages/{stage}` | [AccountAdminController.ScanSteamPresencesByStage:L575](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountAdminController.cs#L575) | passport.accounts_presence |
| POST | `/api/accounts/recovery/password` | [AccountController.RequestResetPassword:L33](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountController.cs#L33) | passport.accounts_presence |
| GET | `/api/accounts/{name}/statuses` | [AccountController.GetOtherStatus:L120](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountController.cs#L120) | passport.accounts_presence |
| GET | `/api/accounts/{name}/calendar` | [AccountController.GetOtherEventCalendar:L144](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountController.cs#L144) | passport.accounts_presence |
| GET | `/api/accounts/{name}/calendar/merged` | [AccountController.GetOtherMergedEventCalendar:L236](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountController.cs#L236) | passport.accounts_presence |
| GET | `/api/accounts/{name}/calendar/countdown` | [AccountController.GetOtherCountdown:L308](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountController.cs#L308) | passport.accounts_presence |
| GET | `/api/accounts/{name}/timeline` | [AccountController.GetTimeline:L368](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountController.cs#L368) | passport.accounts_presence |
| GET | `/api/accounts/{name}/calendar/events` | [AccountController.GetPublicCalendarEvents:L393](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountController.cs#L393) | passport.accounts_presence |
| GET | `/api/accounts/{name}/calendar/events/{id:guid}` | [AccountController.GetPublicCalendarEvent:L438](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountController.cs#L438) | passport.accounts_presence |
| GET | `/api/accounts/unknown/calendar/events/{id:guid}` | [AccountController.GetCalendarEventById:L476](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountController.cs#L476) | passport.accounts_presence |
| GET | `/api/accounts/me/activation/progress` | [AccountCurrentController.GetActivationProgress:L38](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountCurrentController.cs#L38) | passport.accounts_presence |
| GET | `/api/accounts/me/passbook/member` | [AccountCurrentController.GetMemberPass:L46](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountCurrentController.cs#L46) | passport.accounts_presence |
| GET | `/api/accounts/me/board` | [AccountCurrentController.GetBoard:L85](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountCurrentController.cs#L85) | passport.accounts_presence |
| PUT | `/api/accounts/me/board` | [AccountCurrentController.ReplaceBoard:L93](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountCurrentController.cs#L93) | passport.accounts_presence |
| GET | `/api/accounts/me/actions` | [AccountCurrentController.GetActionLogs:L111](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountCurrentController.cs#L111) | passport.accounts_presence |
| GET | `/api/accounts/me/badges` | [AccountCurrentController.GetBadges:L170](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountCurrentController.cs#L170) | passport.accounts_presence |
| POST | `/api/accounts/me/badges/{id:guid}/active` | [AccountCurrentController.ActivateBadge:L183](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountCurrentController.cs#L183) | passport.accounts_presence |
| GET | `/api/accounts/me/leveling` | [AccountCurrentController.GetLevelingHistory:L201](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountCurrentController.cs#L201) | passport.accounts_presence |
| GET | `/api/accounts/me/credits` | [AccountCurrentController.GetSocialCredit:L225](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountCurrentController.cs#L225) | passport.accounts_presence |
| GET | `/api/accounts/me/credits/history` | [AccountCurrentController.GetCreditHistory:L234](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountCurrentController.cs#L234) | passport.accounts_presence |
| GET | `/api/accounts/me/connections` | [AccountCurrentController.GetConnections:L257](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountCurrentController.cs#L257) | passport.accounts_presence |
| GET | `/api/accounts/me/statuses` | [AccountEventController.GetCurrentStatus:L36](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountEventController.cs#L36) | passport.accounts_presence |
| PATCH | `/api/accounts/me/statuses` | [AccountEventController.UpdateStatus:L44](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountEventController.cs#L44) | passport.accounts_presence |
| POST | `/api/accounts/me/statuses` | [AccountEventController.CreateStatus:L94](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountEventController.cs#L94) | passport.accounts_presence |
| DELETE | `/api/accounts/me/statuses` | [AccountEventController.DeleteStatus:L179](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountEventController.cs#L179) | passport.accounts_presence |
| GET | `/api/accounts/me/check-in` | [AccountEventController.GetCheckInResult:L202](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountEventController.cs#L202) | passport.accounts_presence |
| POST | `/api/accounts/me/check-in` | [AccountEventController.DoCheckIn:L227](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountEventController.cs#L227) | passport.accounts_presence |
| GET | `/api/accounts/me/calendar` | [AccountEventController.GetEventCalendar:L312](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountEventController.cs#L312) | passport.accounts_presence |
| GET | `/api/accounts/me/calendar/merged` | [AccountEventController.GetMergedEventCalendar:L372](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountEventController.cs#L372) | passport.accounts_presence |
| GET | `/api/accounts/me/calendar/tags` | [AccountEventController.GetCalendarTags:L415](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountEventController.cs#L415) | passport.accounts_presence |
| GET | `/api/accounts/me/calendar/events` | [AccountEventController.GetCalendarEvents:L423](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountEventController.cs#L423) | passport.accounts_presence |
| POST | `/api/accounts/me/calendar/events` | [AccountEventController.CreateCalendarEvent:L450](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountEventController.cs#L450) | passport.accounts_presence |
| GET | `/api/accounts/me/calendar/events/{id:guid}` | [AccountEventController.GetCalendarEvent:L492](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountEventController.cs#L492) | passport.accounts_presence |
| PUT | `/api/accounts/me/calendar/events/{id:guid}` | [AccountEventController.UpdateCalendarEvent:L507](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountEventController.cs#L507) | passport.accounts_presence |
| DELETE | `/api/accounts/me/calendar/events/{id:guid}` | [AccountEventController.DeleteCalendarEvent:L556](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountEventController.cs#L556) | passport.accounts_presence |
| GET | `/api/accounts/me/calendar/notable-days/{occurrenceKey}` | [AccountEventController.GetGeneratedNotableDay:L571](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountEventController.cs#L571) | passport.accounts_presence |
| GET | `/api/accounts/me/calendar/search` | [AccountEventController.SearchCalendar:L585](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountEventController.cs#L585) | passport.accounts_presence |
| GET | `/api/accounts/me/calendar/countdown` | [AccountEventController.GetCountdown:L630](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountEventController.cs#L630) | passport.accounts_presence |
| GET | `/api/accounts/me/calendar/subscriptions` | [AccountEventController.GetEventSubscriptions:L671](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountEventController.cs#L671) | passport.accounts_presence |
| POST | `/api/accounts/me/calendar/subscriptions/{accountId:guid}` | [AccountEventController.SubscribeToEvents:L681](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountEventController.cs#L681) | passport.accounts_presence |
| DELETE | `/api/accounts/me/calendar/subscriptions/{accountId:guid}` | [AccountEventController.UnsubscribeFromEvents:L703](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountEventController.cs#L703) | passport.accounts_presence |
| GET | `/api/accounts/me/calendar/subscriptions/subscribers` | [AccountEventController.GetEventSubscribers:L718](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountEventController.cs#L718) | passport.accounts_presence |
| GET | `/api/accounts/{name}/badges` | [AccountPublicController.GetBadgesByName:L23](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountPublicController.cs#L23) | passport.accounts_presence |
| GET | `/api/accounts/{name}/board` | [AccountPublicController.GetAccountBoard:L35](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountPublicController.cs#L35) | passport.accounts_presence |
| GET | `/api/accounts/{name}/credits` | [AccountPublicController.GetSocialCredits:L47](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountPublicController.cs#L47) | passport.accounts_presence |
| GET | `/api/admin/stats` | [AccountStatsAdminController.GetStats:L30](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountStatsAdminController.cs#L30) | passport.accounts_presence |
| GET | `/api/fortune` | [FortuneSayingController.ListFortunes:L123](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/FortuneSayingController.cs#L123) | passport.fortunes |
| GET | `/api/fortune/random` | [FortuneSayingController.GetRandomFortunes:L129](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/FortuneSayingController.cs#L129) | passport.fortunes |
| GET | `/api/friends/overview` | [FriendsController.GetOverview:L29](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/FriendsController.cs#L29) | passport.social_realms |
| GET | `/api/notable-days` | [NotableDaysController.ListNotableDays:L64](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/NotableDaysController.cs#L64) | passport.notable_days |
| GET | `/api/notable-days/{id:guid}` | [NotableDaysController.GetNotableDay:L113](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/NotableDaysController.cs#L113) | passport.notable_days |
| POST | `/api/notable-days` | [NotableDaysController.CreateNotableDay:L127](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/NotableDaysController.cs#L127) | passport.notable_days |
| PUT | `/api/notable-days/{id:guid}` | [NotableDaysController.UpdateNotableDay:L160](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/NotableDaysController.cs#L160) | passport.notable_days |
| DELETE | `/api/notable-days/{id:guid}` | [NotableDaysController.DeleteNotableDay:L192](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/NotableDaysController.cs#L192) | passport.notable_days |
| GET | `/api/passkit/v1` | [PassKitController.GetRoot:L31](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/PassKitController.cs#L31) | passport.accounts_presence |
| POST | `/api/passkit/v1/devices/{deviceLibraryIdentifier}/registrations/{passTypeIdentifier}/{serialNumber}` | [PassKitController.RegisterDevice:L51](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/PassKitController.cs#L51) | passport.accounts_presence |
| DELETE | `/api/passkit/v1/devices/{deviceLibraryIdentifier}/registrations/{passTypeIdentifier}/{serialNumber}` | [PassKitController.UnregisterDevice:L77](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/PassKitController.cs#L77) | passport.accounts_presence |
| GET | `/api/passkit/v1/devices/{deviceLibraryIdentifier}/registrations/{passTypeIdentifier}` | [PassKitController.GetUpdatedSerialNumbers:L95](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/PassKitController.cs#L95) | passport.accounts_presence |
| GET | `/api/passkit/v1/passes/{passTypeIdentifier}/{serialNumber}` | [PassKitController.GetLatestPass:L126](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/PassKitController.cs#L126) | passport.accounts_presence |
| POST | `/api/passkit/v1/log` | [PassKitController.Log:L142](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/PassKitController.cs#L142) | passport.accounts_presence |
| GET | `/api/activities` | [PresenceActivityController.GetActivities:L41](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/PresenceActivityController.cs#L41) | passport.accounts_presence |
| GET | `/api/activities/{identifier}` | [PresenceActivityController.GetActivitiesByAccountId:L104](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/PresenceActivityController.cs#L104) | passport.accounts_presence |
| POST | `/api/activities` | [PresenceActivityController.SetActivity:L127](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/PresenceActivityController.cs#L127) | passport.accounts_presence |
| POST | `/api/activities/sessions/start` | [PresenceActivityController.StartActivitySession:L216](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/PresenceActivityController.cs#L216) | passport.accounts_presence |
| POST | `/api/activities/sessions/end` | [PresenceActivityController.EndActivitySession:L282](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/PresenceActivityController.cs#L282) | passport.accounts_presence |
| GET | `/api/activities/catalog` | [PresenceActivityController.GetCatalog:L309](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/PresenceActivityController.cs#L309) | passport.accounts_presence |
| GET | `/api/activities/stats` | [PresenceActivityController.GetStats:L342](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/PresenceActivityController.cs#L342) | passport.accounts_presence |
| GET | `/api/activities/tags/stats` | [PresenceActivityController.GetTagStats:L369](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/PresenceActivityController.cs#L369) | passport.accounts_presence |
| PUT | `/api/activities/{id:guid}` | [PresenceActivityController.UpdateActivity:L397](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/PresenceActivityController.cs#L397) | passport.accounts_presence |
| DELETE | `/api/activities` | [PresenceActivityController.DeleteActivityById:L469](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/PresenceActivityController.cs#L469) | passport.accounts_presence |
| POST | `/api/presence/artworks` | [PresenceArtworkController.UploadArtwork:L21](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/PresenceArtworkController.cs#L21) | passport.accounts_presence |
| GET | `/api/presence/artworks/{hash}` | [PresenceArtworkController.GetArtwork:L47](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/PresenceArtworkController.cs#L47) | passport.accounts_presence |
| POST | `/api/affiliations/registration-invites/consume` | [AffiliationSpellController.ConsumeRegistrationInvite:L42](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Affiliation/AffiliationSpellController.cs#L42) | passport.progression |
| POST | `/api/affiliations/purchase` | [AffiliationSpellController.PurchaseSpell:L57](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Affiliation/AffiliationSpellController.cs#L57) | passport.progression |
| POST | `/api/affiliations` | [AffiliationSpellController.CreateSpell:L73](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Affiliation/AffiliationSpellController.cs#L73) | passport.progression |
| POST | `/api/affiliations/{spell}/results` | [AffiliationSpellController.RecordResult:L91](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Affiliation/AffiliationSpellController.cs#L91) | passport.progression |
| GET | `/api/affiliations` | [AffiliationSpellController.ListCreatedSpells:L114](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Affiliation/AffiliationSpellController.cs#L114) | passport.progression |
| GET | `/api/affiliations/{id:guid}` | [AffiliationSpellController.GetSpell:L149](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Affiliation/AffiliationSpellController.cs#L149) | passport.progression |
| GET | `/api/affiliations/{id:guid}/results` | [AffiliationSpellController.ListResults:L164](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Affiliation/AffiliationSpellController.cs#L164) | passport.progression |
| DELETE | `/api/affiliations/{id:guid}` | [AffiliationSpellController.DeleteSpell:L196](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Affiliation/AffiliationSpellController.cs#L196) | passport.progression |
| GET | `/api/domain-blocks` | [DomainTrustController.ListRules:L31](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/DomainTrust/DomainTrustController.cs#L31) | passport.accounts_presence |
| GET | `/api/domain-blocks/{id:guid}` | [DomainTrustController.GetRule:L45](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/DomainTrust/DomainTrustController.cs#L45) | passport.accounts_presence |
| POST | `/api/domain-blocks` | [DomainTrustController.CreateRule:L73](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/DomainTrust/DomainTrustController.cs#L73) | passport.accounts_presence |
| PATCH | `/api/domain-blocks/{id:guid}` | [DomainTrustController.UpdateRule:L112](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/DomainTrust/DomainTrustController.cs#L112) | passport.accounts_presence |
| DELETE | `/api/domain-blocks/{id:guid}` | [DomainTrustController.DeleteRule:L132](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/DomainTrust/DomainTrustController.cs#L132) | passport.accounts_presence |
| POST | `/api/domain-blocks/validate` | [DomainTrustController.ValidateUrl:L149](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/DomainTrust/DomainTrustController.cs#L149) | passport.accounts_presence |
| GET | `/api/domain-blocks/check` | [DomainTrustController.CheckUrl:L157](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/DomainTrust/DomainTrustController.cs#L157) | passport.accounts_presence |
| GET | `/api/domain-blocks/metrics` | [DomainTrustController.ListMetrics:L170](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/DomainTrust/DomainTrustController.cs#L170) | passport.accounts_presence |
| GET | `/api/domain-blocks/metrics/{domain}` | [DomainTrustController.GetMetric:L191](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/DomainTrust/DomainTrustController.cs#L191) | passport.accounts_presence |
| GET | `/api/admin/tests` | [TestAdminController.List:L17](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestAdminController.cs#L17) | passport.tickets_exams |
| POST | `/api/admin/tests` | [TestAdminController.Create:L21](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestAdminController.cs#L21) | passport.tickets_exams |
| PUT | `/api/admin/tests/{key}` | [TestAdminController.Update:L37](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestAdminController.cs#L37) | passport.tickets_exams |
| POST | `/api/admin/tests/{key}/publish` | [TestAdminController.Publish:L54](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestAdminController.cs#L54) | passport.tickets_exams |
| POST | `/api/admin/tests/{key}/archive` | [TestAdminController.Archive:L65](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestAdminController.cs#L65) | passport.tickets_exams |
| GET | `/api/admin/tests/{key}/trial` | [TestAdminController.Trial:L77](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestAdminController.cs#L77) | passport.tickets_exams |
| POST | `/api/admin/tests/{key}/trial/attempts` | [TestAdminController.StartTrial:L85](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestAdminController.cs#L85) | passport.tickets_exams |
| POST | `/api/admin/tests/{key}/trials` | [TestAdminController.CreateTrial:L96](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestAdminController.cs#L96) | passport.tickets_exams |
| POST | `/api/admin/tests/{key}/trial/grade` | [TestAdminController.GradeTrial:L114](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestAdminController.cs#L114) | passport.tickets_exams |
| GET | `/api/admin/tests/{key}/attempts` | [TestAdminController.ListAttempts:L138](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestAdminController.cs#L138) | passport.tickets_exams |
| POST | `/api/admin/tests/answers/{answerId:guid}/review` | [TestAdminController.ReviewAnswer:L149](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestAdminController.cs#L149) | passport.tickets_exams |
| GET | `/api/tests` | [TestController.ListPublicTests:L17](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestController.cs#L17) | passport.tickets_exams |
| GET | `/api/tests/activation` | [TestController.GetActivationRequirements:L25](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestController.cs#L25) | passport.tickets_exams |
| POST | `/api/tests/activation/recheck` | [TestController.RecheckActivationRequirements:L33](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestController.cs#L33) | passport.tickets_exams |
| GET | `/api/tests/{key}` | [TestController.GetTest:L42](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestController.cs#L42) | passport.tickets_exams |
| POST | `/api/tests/{key}/attempts` | [TestController.StartAttempt:L50](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestController.cs#L50) | passport.tickets_exams |
| POST | `/api/tests/attempts/{attemptId:guid}/submit` | [TestController.SubmitAttempt:L61](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestController.cs#L61) | passport.tickets_exams |
| GET | `/api/tests/attempts/{attemptId:guid}` | [TestController.GetAttempt:L72](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestController.cs#L72) | passport.tickets_exams |
| GET | `/api/admin/test-questions` | [TestQuestionAdminController.List:L18](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestQuestionAdminController.cs#L18) | passport.tickets_exams |
| POST | `/api/admin/test-questions` | [TestQuestionAdminController.Create:L28](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestQuestionAdminController.cs#L28) | passport.tickets_exams |
| POST | `/api/admin/test-questions/import` | [TestQuestionAdminController.Import:L42](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestQuestionAdminController.cs#L42) | passport.tickets_exams |
| GET | `/api/admin/test-questions/export` | [TestQuestionAdminController.Export:L62](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestQuestionAdminController.cs#L62) | passport.tickets_exams |
| DELETE | `/api/admin/test-questions/prune` | [TestQuestionAdminController.Prune:L82](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestQuestionAdminController.cs#L82) | passport.tickets_exams |
| PUT | `/api/admin/test-questions/{id:guid}` | [TestQuestionAdminController.Update:L93](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestQuestionAdminController.cs#L93) | passport.tickets_exams |
| DELETE | `/api/admin/test-questions/{id:guid}` | [TestQuestionAdminController.Delete:L109](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestQuestionAdminController.cs#L109) | passport.tickets_exams |
| GET | `/api/admin/test-question-groups` | [TestQuestionGroupAdminController.List:L17](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestQuestionGroupAdminController.cs#L17) | passport.tickets_exams |
| POST | `/api/admin/test-question-groups` | [TestQuestionGroupAdminController.Create:L22](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestQuestionGroupAdminController.cs#L22) | passport.tickets_exams |
| PUT | `/api/admin/test-question-groups/{key}` | [TestQuestionGroupAdminController.Update:L35](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestQuestionGroupAdminController.cs#L35) | passport.tickets_exams |
| DELETE | `/api/admin/test-question-groups/{key}` | [TestQuestionGroupAdminController.Delete:L48](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestQuestionGroupAdminController.cs#L48) | passport.tickets_exams |
| GET | `/api/admin/test-trials` | [TestTrialAdminController.List:L17](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestTrialAdminController.cs#L17) | passport.tickets_exams |
| POST | `/api/admin/test-trials` | [TestTrialAdminController.Create:L22](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestTrialAdminController.cs#L22) | passport.tickets_exams |
| PUT | `/api/admin/test-trials/{key}` | [TestTrialAdminController.Update:L37](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestTrialAdminController.cs#L37) | passport.tickets_exams |
| GET | `/api/tests/trials/{key}` | [TestTrialController.Get:L17](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestTrialController.cs#L17) | passport.tickets_exams |
| POST | `/api/tests/trials/{key}/attempts` | [TestTrialController.Start:L25](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Examination/TestTrialController.cs#L25) | passport.tickets_exams |
| GET | `/api/ip-check/geo` | [IpCheckController.GetClientGeo:L46](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/IpCheckController.cs#L46) | passport.accounts_presence |
| GET | `/api/ip-check` | [IpCheckController.GetIpCheck:L63](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/IpCheckController.cs#L63) | passport.accounts_presence |
| POST | `/api/pins` | [LocationPinController.CreatePin:L48](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Meet/LocationPinController.cs#L48) | passport.location_nfc |
| PUT | `/api/pins/{id:guid}/location` | [LocationPinController.UpdateLocation:L92](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Meet/LocationPinController.cs#L92) | passport.location_nfc |
| DELETE | `/api/pins/{id:guid}` | [LocationPinController.RemovePin:L139](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Meet/LocationPinController.cs#L139) | passport.location_nfc |
| GET | `/api/pins/nearby` | [LocationPinController.ListNearbyPins:L159](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Meet/LocationPinController.cs#L159) | passport.location_nfc |
| GET | `/api/pins/{id:guid}` | [LocationPinController.GetPin:L200](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Meet/LocationPinController.cs#L200) | passport.location_nfc |
| POST | `/api/pins/{id:guid}/disconnect` | [LocationPinController.DisconnectPin:L236](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Meet/LocationPinController.cs#L236) | passport.location_nfc |
| GET | `/api/pins/{id:guid}/stream` | [LocationPinController.StreamPin:L259](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Meet/LocationPinController.cs#L259) | passport.location_nfc |
| GET | `/api/pins/me` | [LocationPinController.GetMyPins:L313](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Meet/LocationPinController.cs#L313) | passport.location_nfc |
| POST | `/api/meets` | [MeetController.CreateMeet:L39](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Meet/MeetController.cs#L39) | passport.location_nfc |
| GET | `/api/meets/nearby` | [MeetController.ListNearbyMeets:L88](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Meet/MeetController.cs#L88) | passport.location_nfc |
| GET | `/api/meets` | [MeetController.ListMeets:L144](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Meet/MeetController.cs#L144) | passport.location_nfc |
| GET | `/api/meets/{id:guid}` | [MeetController.GetMeet:L160](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Meet/MeetController.cs#L160) | passport.location_nfc |
| POST | `/api/meets/{id:guid}/complete` | [MeetController.CompleteMeet:L190](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Meet/MeetController.cs#L190) | passport.location_nfc |
| DELETE | `/api/meets/{id:guid}` | [MeetController.DeleteMeet:L222](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Meet/MeetController.cs#L222) | passport.location_nfc |
| PATCH | `/api/meets/{id:guid}/visibility` | [MeetController.UpdateVisibility:L249](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Meet/MeetController.cs#L249) | passport.location_nfc |
| POST | `/api/meets/{id:guid}/join` | [MeetController.JoinMeet:L289](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Meet/MeetController.cs#L289) | passport.location_nfc |
| POST | `/api/meets/{id:guid}/pin` | [MeetController.CreateOrUpdatePin:L386](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Meet/MeetController.cs#L386) | passport.location_nfc |
| DELETE | `/api/meets/{id:guid}/pin` | [MeetController.RemovePin:L439](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Meet/MeetController.cs#L439) | passport.location_nfc |
| GET | `/api/meets/{id:guid}/pins` | [MeetController.GetTrailPins:L464](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Meet/MeetController.cs#L464) | passport.location_nfc |
| POST | `/api/accounts/me/name-change-card/order` | [NameChangeCardController.PurchaseOrder:L31](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/NameChangeCard/NameChangeCardController.cs#L31) | passport.progression |
| GET | `/api/accounts/me/name-change-card` | [NameChangeCardController.ListPurchases:L47](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/NameChangeCard/NameChangeCardController.cs#L47) | passport.progression |
| POST | `/api/accounts/me/name-change-card/use` | [NameChangeCardController.UseCard:L55](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/NameChangeCard/NameChangeCardController.cs#L55) | passport.progression |
| POST | `/api/nearby/presence-tokens` | [NearbyController.IssuePresenceTokens:L85](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Nearby/NearbyController.cs#L85) | passport.location_nfc |
| POST | `/api/nearby/resolve` | [NearbyController.Resolve:L119](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Nearby/NearbyController.cs#L119) | passport.location_nfc |
| POST | `/api/admin/nfc/tags` | [NfcAdminController.CreateEncryptedTag:L49](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Nfc/NfcAdminController.cs#L49) | passport.location_nfc |
| GET | `/api/admin/nfc/tags` | [NfcAdminController.ListEncryptedTags:L147](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Nfc/NfcAdminController.cs#L147) | passport.location_nfc |
| GET | `/api/nfc` | [NfcController.Resolve:L127](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Nfc/NfcController.cs#L127) | passport.location_nfc |
| GET | `/api/nfc/lookup` | [NfcController.Lookup:L381](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Nfc/NfcController.cs#L381) | passport.location_nfc |
| GET | `/api/nfc/tags/{id:guid}` | [NfcController.GetById:L408](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Nfc/NfcController.cs#L408) | passport.location_nfc |
| GET | `/api/nfc/tags` | [NfcController.ListTags:L429](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Nfc/NfcController.cs#L429) | passport.location_nfc |
| POST | `/api/nfc/tags` | [NfcController.RegisterTag:L458](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Nfc/NfcController.cs#L458) | passport.location_nfc |
| POST | `/api/nfc/tags/claim` | [NfcController.ClaimTag:L498](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Nfc/NfcController.cs#L498) | passport.location_nfc |
| PATCH | `/api/nfc/tags/{tagId:guid}` | [NfcController.UpdateTag:L556](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Nfc/NfcController.cs#L556) | passport.location_nfc |
| POST | `/api/nfc/tags/{tagId:guid}/lock` | [NfcController.LockTag:L593](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Nfc/NfcController.cs#L593) | passport.location_nfc |
| DELETE | `/api/nfc/tags/{tagId:guid}` | [NfcController.UnregisterTag:L624](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Nfc/NfcController.cs#L624) | passport.location_nfc |
| GET | `/.well-known/badges` | [BadgesDiscoveryController.GetBadgesManifest:L18](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Progression/BadgesDiscoveryController.cs#L18) | passport.badges |
| GET | `/.well-known/badges/icons/{iconName}` | [BadgesDiscoveryController.GetBadgeIcon:L36](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Progression/BadgesDiscoveryController.cs#L36) | passport.badges |
| GET | `/api/admin/progression/achievements` | [ProgressionAdminController.ListAchievements:L24](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Progression/ProgressionAdminController.cs#L24) | passport.progression |
| POST | `/api/admin/progression/achievements` | [ProgressionAdminController.CreateAchievement:L31](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Progression/ProgressionAdminController.cs#L31) | passport.progression |
| PUT | `/api/admin/progression/achievements/{identifier}` | [ProgressionAdminController.UpdateAchievement:L43](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Progression/ProgressionAdminController.cs#L43) | passport.progression |
| POST | `/api/admin/progression/achievements/{identifier}/enable` | [ProgressionAdminController.ToggleAchievement:L55](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Progression/ProgressionAdminController.cs#L55) | passport.progression |
| GET | `/api/admin/progression/quests` | [ProgressionAdminController.ListQuests:L67](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Progression/ProgressionAdminController.cs#L67) | passport.progression |
| POST | `/api/admin/progression/quests` | [ProgressionAdminController.CreateQuest:L74](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Progression/ProgressionAdminController.cs#L74) | passport.progression |
| PUT | `/api/admin/progression/quests/{identifier}` | [ProgressionAdminController.UpdateQuest:L86](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Progression/ProgressionAdminController.cs#L86) | passport.progression |
| POST | `/api/admin/progression/quests/{identifier}/enable` | [ProgressionAdminController.ToggleQuest:L98](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Progression/ProgressionAdminController.cs#L98) | passport.progression |
| POST | `/api/admin/progression/sync` | [ProgressionAdminController.SyncSeeds:L110](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Progression/ProgressionAdminController.cs#L110) | passport.progression |
| POST | `/api/admin/progression/test-ws-packet` | [ProgressionAdminController.TestWebSocketPacket:L119](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Progression/ProgressionAdminController.cs#L119) | passport.progression |
| GET | `/api/accounts/me/progression/achievements` | [ProgressionController.GetAchievements:L16](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Progression/ProgressionController.cs#L16) | passport.progression |
| GET | `/api/accounts/me/progression/achievements/stats` | [ProgressionController.GetAchievementStats:L28](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Progression/ProgressionController.cs#L28) | passport.progression |
| GET | `/api/accounts/me/progression/quests` | [ProgressionController.GetQuests:L37](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Progression/ProgressionController.cs#L37) | passport.progression |
| GET | `/api/accounts/me/progression/grants` | [ProgressionController.GetRewardGrants:L46](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Progression/ProgressionController.cs#L46) | passport.progression |
| GET | `/api/admin/realms` | [RealmAdminController.ListRealms:L60](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmAdminController.cs#L60) | passport.social_realms |
| GET | `/api/admin/realms/{slug}` | [RealmAdminController.GetRealm:L110](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmAdminController.cs#L110) | passport.social_realms |
| PATCH | `/api/admin/realms/{slug}` | [RealmAdminController.UpdateRealm:L146](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmAdminController.cs#L146) | passport.social_realms |
| POST | `/api/admin/realms/{slug}/verification` | [RealmAdminController.SetVerification:L209](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmAdminController.cs#L209) | passport.social_realms |
| DELETE | `/api/admin/realms/{slug}/verification` | [RealmAdminController.ClearVerification:L244](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmAdminController.cs#L244) | passport.social_realms |
| GET | `/api/admin/realms/{slug}/members` | [RealmAdminController.ListMembers:L269](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmAdminController.cs#L269) | passport.social_realms |
| PATCH | `/api/admin/realms/{slug}/members/{memberId:guid}/role` | [RealmAdminController.UpdateMemberRole:L316](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmAdminController.cs#L316) | passport.social_realms |
| DELETE | `/api/admin/realms/{slug}/members/{memberId:guid}` | [RealmAdminController.RemoveMember:L356](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmAdminController.cs#L356) | passport.social_realms |
| DELETE | `/api/admin/realms/{slug}` | [RealmAdminController.DeleteRealm:L390](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmAdminController.cs#L390) | passport.social_realms |
| GET | `/api/realms/quota` | [RealmController.GetQuota:L42](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmController.cs#L42) | passport.social_realms |
| GET | `/api/realms/{slug}` | [RealmController.GetRealm:L55](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmController.cs#L55) | passport.social_realms |
| GET | `/api/realms` | [RealmController.ListJoinedRealms:L64](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmController.cs#L64) | passport.social_realms |
| GET | `/api/realms/invites` | [RealmController.ListInvites:L81](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmController.cs#L81) | passport.social_realms |
| POST | `/api/realms/invites/{slug}` | [RealmController.InviteMember:L136](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmController.cs#L136) | passport.social_realms |
| POST | `/api/realms/invites/{slug}/accept` | [RealmController.AcceptMemberInvite:L221](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmController.cs#L221) | passport.social_realms |
| POST | `/api/realms/invites/{slug}/decline` | [RealmController.DeclineMemberInvite:L253](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmController.cs#L253) | passport.social_realms |
| GET | `/api/realms/{slug}/members` | [RealmController.ListMembers:L286](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmController.cs#L286) | passport.social_realms |
| GET | `/api/realms/{slug}/members/me` | [RealmController.GetCurrentIdentity:L379](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmController.cs#L379) | passport.social_realms |
| PATCH | `/api/realms/{slug}/members/me/profile` | [RealmController.UpdateCurrentIdentity:L396](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmController.cs#L396) | passport.social_realms |
| DELETE | `/api/realms/{slug}/members/me/profile` | [RealmController.DeleteCurrentIdentity:L418](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmController.cs#L418) | passport.social_realms |
| GET | `/api/realms/{slug}/labels` | [RealmController.ListLabels:L438](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmController.cs#L438) | passport.social_realms |
| POST | `/api/realms/{slug}/labels` | [RealmController.CreateLabel:L459](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmController.cs#L459) | passport.social_realms |
| PATCH | `/api/realms/{slug}/labels/{labelId:guid}` | [RealmController.UpdateLabel:L491](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmController.cs#L491) | passport.social_realms |
| DELETE | `/api/realms/{slug}/labels/{labelId:guid}` | [RealmController.DeleteLabel:L516](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmController.cs#L516) | passport.social_realms |
| PATCH | `/api/realms/{slug}/members/{memberId:guid}/label` | [RealmController.UpdateMemberLabel:L540](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmController.cs#L540) | passport.social_realms |
| POST | `/api/realms/{slug}/boosts` | [RealmController.BoostRealm:L571](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmController.cs#L571) | passport.social_realms |
| GET | `/api/realms/{slug}/boosts` | [RealmController.GetBoostStatus:L619](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmController.cs#L619) | passport.social_realms |
| GET | `/api/realms/{slug}/boosts/leaderboard` | [RealmController.GetBoostLeaderboard:L640](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmController.cs#L640) | passport.social_realms |
| GET | `/api/realms/{slug}/members/{memberId:guid}/experience` | [RealmController.GetMemberExperience:L687](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmController.cs#L687) | passport.social_realms |
| DELETE | `/api/realms/{slug}/members/me` | [RealmController.LeaveRealm:L709](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmController.cs#L709) | passport.social_realms |
| POST | `/api/realms` | [RealmController.CreateRealm:L755](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmController.cs#L755) | passport.social_realms |
| PATCH | `/api/realms/{slug}` | [RealmController.Update:L826](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmController.cs#L826) | passport.social_realms |
| POST | `/api/realms/{slug}/members/me` | [RealmController.JoinRealm:L900](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmController.cs#L900) | passport.social_realms |
| DELETE | `/api/realms/{slug}/members/{memberId:guid}` | [RealmController.RemoveMember:L969](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmController.cs#L969) | passport.social_realms |
| PATCH | `/api/realms/{slug}/members/{memberId:guid}/role` | [RealmController.UpdateMemberRole:L1006](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmController.cs#L1006) | passport.social_realms |
| DELETE | `/api/realms/{slug}` | [RealmController.Delete:L1047](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmController.cs#L1047) | passport.social_realms |
| GET | `/api/realms/{slug}/permissions/roles` | [RealmController.GetRolePermissions:L1129](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmController.cs#L1129) | passport.social_realms |
| POST | `/api/realms/{slug}/permissions/roles` | [RealmController.UpdateRolePermissions:L1145](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmController.cs#L1145) | passport.social_realms |
| GET | `/api/realms/{slug}/permissions/users/{accountId:guid}` | [RealmController.GetUserPermissions:L1185](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmController.cs#L1185) | passport.social_realms |
| POST | `/api/realms/{slug}/permissions/users` | [RealmController.UpdateUserPermissions:L1201](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmController.cs#L1201) | passport.social_realms |
| GET | `/api/realms/{slug}/posts/moderation-logs` | [RealmController.GetModerationLogs:L1241](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmController.cs#L1241) | passport.social_realms |
| GET | `/api/realms/public` | [RealmPublicController.ListCommunityRealms:L13](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmPublicController.cs#L13) | passport.social_realms |
| GET | `/api/rewind/{code}` | [AccountRewindController.GetRewindPoint:L16](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Rewind/AccountRewindController.cs#L16) | passport.accounts_presence |
| GET | `/api/rewind/me` | [AccountRewindController.GetCurrentRewindPoint:L24](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Rewind/AccountRewindController.cs#L24) | passport.accounts_presence |
| POST | `/api/rewind/me/{year:int}/public` | [AccountRewindController.SetRewindPointPublic:L33](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Rewind/AccountRewindController.cs#L33) | passport.accounts_presence |
| POST | `/api/rewind/me/{year:int}/private` | [AccountRewindController.SetRewindPointPrivate:L50](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Rewind/AccountRewindController.cs#L50) | passport.accounts_presence |
| POST | `/api/tickets` | [TicketController.CreateTicket:L346](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Ticket/TicketController.cs#L346) | passport.tickets_exams |
| GET | `/api/tickets` | [TicketController.GetTickets:L393](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Ticket/TicketController.cs#L393) | passport.tickets_exams |
| GET | `/api/tickets/me` | [TicketController.GetMyTickets:L440](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Ticket/TicketController.cs#L440) | passport.tickets_exams |
| GET | `/api/tickets/{id}` | [TicketController.GetTicketById:L469](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Ticket/TicketController.cs#L469) | passport.tickets_exams |
| PUT | `/api/tickets/{id}` | [TicketController.UpdateTicket:L501](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Ticket/TicketController.cs#L501) | passport.tickets_exams |
| DELETE | `/api/tickets/{id}` | [TicketController.DeleteTicket:L560](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Ticket/TicketController.cs#L560) | passport.tickets_exams |
| POST | `/api/tickets/{id:guid}/messages` | [TicketController.AddMessage:L609](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Ticket/TicketController.cs#L609) | passport.tickets_exams |
| POST | `/api/tickets/{id:guid}/status` | [TicketController.UpdateStatus:L682](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Ticket/TicketController.cs#L682) | passport.tickets_exams |
| POST | `/api/tickets/{id:guid}/assign` | [TicketController.Assign:L811](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Ticket/TicketController.cs#L811) | passport.tickets_exams |
| GET | `/api/tickets/count` | [TicketController.GetTicketsCount:L879](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Ticket/TicketController.cs#L879) | passport.tickets_exams |
| GET | `/api/admin/tickets/on-call` | [TicketOnCallAdminController.ListOnCallAdmins:L22](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Ticket/TicketOnCallAdminController.cs#L22) | passport.tickets_exams |
| POST | `/api/admin/tickets/on-call` | [TicketOnCallAdminController.AddOnCallAdmin:L30](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Ticket/TicketOnCallAdminController.cs#L30) | passport.tickets_exams |
| DELETE | `/api/admin/tickets/on-call/{accountId:guid}` | [TicketOnCallAdminController.RemoveOnCallAdmin:L54](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Ticket/TicketOnCallAdminController.cs#L54) | passport.tickets_exams |

### Sphere

| Method | Service route candidate | Action and pinned source | Family |
|---|---|---|---|
| GET | `/api/account/publishing` | [PublishingSettingsController.GetSettings:L33](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Account/AccountSettingsController.cs#L33) | sphere.posts |
| PATCH | `/api/account/publishing` | [PublishingSettingsController.UpdateSettings:L54](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Account/AccountSettingsController.cs#L54) | sphere.posts |
| GET | `/activitypub/actors/{username}` | [ActivityPubController.GetActor:L73](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/ActivityPubController.cs#L73) | sphere.federation |
| POST | `/activitypub/actors/{username}/inbox` | [ActivityPubController.PostInbox:L163](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/ActivityPubController.cs#L163) | sphere.federation |
| GET | `/activitypub/objects/{id:guid}` | [ActivityPubController.GetObject:L212](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/ActivityPubController.cs#L212) | sphere.federation |
| GET | `/activitypub/actors/{username}/outbox` | [ActivityPubController.GetOutbox:L243](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/ActivityPubController.cs#L243) | sphere.federation |
| GET | `/activitypub/actors/{username}/followers` | [ActivityPubController.GetFollowers:L409](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/ActivityPubController.cs#L409) | sphere.federation |
| GET | `/activitypub/actors/{username}/following` | [ActivityPubController.GetFollowing:L479](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/ActivityPubController.cs#L479) | sphere.federation |
| GET | `/activitypub/actors/{username}/featured` | [ActivityPubController.GetFeatured:L584](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/ActivityPubController.cs#L584) | sphere.federation |
| GET | `/api/activitypub/following` | [ActivityPubFollowController.GetFollowing:L64](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/ActivityPubFollowController.cs#L64) | sphere.federation |
| GET | `/api/activitypub/followers` | [ActivityPubFollowController.GetFollowers:L107](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/ActivityPubFollowController.cs#L107) | sphere.federation |
| GET | `/api/activitypub/search` | [ActivityPubFollowController.SearchRemoteUsers:L150](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/ActivityPubFollowController.cs#L150) | sphere.federation |
| GET | `/api/activitypub/relationships` | [ActivityPubFollowController.GetRelationships:L178](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/ActivityPubFollowController.cs#L178) | sphere.federation |
| GET | `/api/activitypub/check/{username}` | [ActivityPubFollowController.CheckActor:L237](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/ActivityPubFollowController.cs#L237) | sphere.federation |
| GET | `/activitypub/realms/{slug}` | [ActivityPubRealmController.GetCommunityActor:L28](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/ActivityPubRealmController.cs#L28) | sphere.federation |
| GET | `/activitypub/realms/{slug}/followers` | [ActivityPubRealmController.GetCommunityFollowers:L89](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/ActivityPubRealmController.cs#L89) | sphere.federation |
| POST | `/activitypub/realms/{slug}/inbox` | [ActivityPubRealmController.ReceiveCommunityActivity:L125](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/ActivityPubRealmController.cs#L125) | sphere.federation |
| GET | `/activitypub/realms/{slug}/outbox` | [ActivityPubRealmController.GetCommunityOutbox:L330](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/ActivityPubRealmController.cs#L330) | sphere.federation |
| GET | `/api/fediverse/actors/{username}@{instance}` | [FediverseActorController.GetActorByHandle:L51](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/FediverseActorController.cs#L51) | sphere.federation |
| GET | `/api/fediverse/actors/{id:guid}` | [FediverseActorController.GetActorById:L85](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/FediverseActorController.cs#L85) | sphere.federation |
| GET | `/api/fediverse/actors/search` | [FediverseActorController.SearchActors:L105](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/FediverseActorController.cs#L105) | sphere.federation |
| GET | `/api/fediverse/actors/{id:guid}/posts` | [FediverseActorController.GetActorPosts:L226](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/FediverseActorController.cs#L226) | sphere.federation |
| GET | `/api/fediverse/actors/{id:guid}/followers` | [FediverseActorController.GetActorFollowers:L777](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/FediverseActorController.cs#L777) | sphere.federation |
| GET | `/api/fediverse/actors/{id:guid}/following` | [FediverseActorController.GetActorFollowing:L804](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/FediverseActorController.cs#L804) | sphere.federation |
| GET | `/api/fediverse/actors/{id:guid}/relationship` | [FediverseActorController.GetActorRelationship:L831](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/FediverseActorController.cs#L831) | sphere.federation |
| GET | `/api/fediverse/actors/availability` | [FediverseActorController.CheckAvailability:L995](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/FediverseActorController.cs#L995) | sphere.federation |
| POST | `/api/fediverse/actors/{id:guid}/follow` | [FediverseActorController.FollowActor:L1062](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/FediverseActorController.cs#L1062) | sphere.federation |
| POST | `/api/fediverse/actors/{id:guid}/unfollow` | [FediverseActorController.UnfollowActor:L1100](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/FediverseActorController.cs#L1100) | sphere.federation |
| GET | `/api/admin/fediverse/keys/audit` | [FediverseKeyAdminController.GetAuditResult:L19](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/FediverseKeyAdminController.cs#L19) | sphere.federation |
| POST | `/api/admin/fediverse/keys/backfill` | [FediverseKeyAdminController.BackfillKeys:L26](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/FediverseKeyAdminController.cs#L26) | sphere.federation |
| POST | `/api/admin/fediverse/keys/cleanup` | [FediverseKeyAdminController.CleanupOrphanedKeys:L45](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/FediverseKeyAdminController.cs#L45) | sphere.federation |
| GET | `/api/admin/fediverse/keys/stats` | [FediverseKeyAdminController.GetStatistics:L52](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/FediverseKeyAdminController.cs#L52) | sphere.federation |
| GET | `/api/admin/fediverse/keys/actor/{actorId:guid}` | [FediverseKeyAdminController.GetActorKeyInfo:L66](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/FediverseKeyAdminController.cs#L66) | sphere.federation |
| POST | `/api/admin/fediverse/keys/actor/{actorId:guid}/ensure` | [FediverseKeyAdminController.EnsureKey:L93](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/FediverseKeyAdminController.cs#L93) | sphere.federation |
| POST | `/api/admin/fediverse/keys/actor/{actorId:guid}/regenerate` | [FediverseKeyAdminController.RegenerateKey:L107](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/FediverseKeyAdminController.cs#L107) | sphere.federation |
| GET | `/activitypub/actors/{username}/main-key` | [FediverseKeyController.GetMainKey:L21](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/FediverseKeyController.cs#L21) | sphere.federation |
| GET | `/activitypub/actors/{username}/publickey` | [FediverseKeyController.GetPublicKey:L62](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/FediverseKeyController.cs#L62) | sphere.federation |
| GET | `/api/fediverse/moderation/rules` | [FediverseModerationController.ListRules:L21](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/FediverseModerationController.cs#L21) | sphere.federation |
| GET | `/api/fediverse/moderation/rules/{id:guid}` | [FediverseModerationController.GetRule:L42](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/FediverseModerationController.cs#L42) | sphere.federation |
| POST | `/api/fediverse/moderation/rules` | [FediverseModerationController.CreateRule:L52](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/FediverseModerationController.cs#L52) | sphere.federation |
| PUT | `/api/fediverse/moderation/rules/{id:guid}` | [FediverseModerationController.UpdateRule:L83](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/FediverseModerationController.cs#L83) | sphere.federation |
| DELETE | `/api/fediverse/moderation/rules/{id:guid}` | [FediverseModerationController.DeleteRule:L126](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/FediverseModerationController.cs#L126) | sphere.federation |
| POST | `/api/fediverse/moderation/rules/{id:guid}/toggle` | [FediverseModerationController.ToggleRule:L144](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/FediverseModerationController.cs#L144) | sphere.federation |
| POST | `/api/fediverse/moderation/check-domain` | [FediverseModerationController.CheckDomain:L164](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/FediverseModerationController.cs#L164) | sphere.federation |
| POST | `/api/fediverse/moderation/check-actor` | [FediverseModerationController.CheckActor:L172](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/FediverseModerationController.cs#L172) | sphere.federation |
| POST | `/api/fediverse/moderation/check-actor-rule` | [FediverseModerationController.CheckActorRule:L180](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/FediverseModerationController.cs#L180) | sphere.federation |
| GET | `/api/fediverse/moderation/actors` | [FediverseModerationController.ListActorRules:L188](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/FediverseModerationController.cs#L188) | sphere.federation |
| POST | `/api/fediverse/moderation/actors/suspend` | [FediverseModerationController.SuspendActor:L215](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/FediverseModerationController.cs#L215) | sphere.federation |
| DELETE | `/api/fediverse/moderation/actors/suspend/{id:guid}` | [FediverseModerationController.UnsuspendActor:L272](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/FediverseModerationController.cs#L272) | sphere.federation |
| GET | `/.well-known/nodeinfo` | [NodeInfoController.Discovery:L17](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/NodeInfoController.cs#L17) | sphere.federation_discovery |
| GET | `/.well-known/nodeinfo/2.0` | [NodeInfoController.NodeInfo2_0:L38](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/NodeInfoController.cs#L38) | sphere.federation_discovery |
| GET | `/.well-known/nodeinfo/2.1` | [NodeInfoController.NodeInfo2_1:L92](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/NodeInfoController.cs#L92) | sphere.federation_discovery |
| GET | `/quote-authorizations/{id:guid}` | [QuoteAuthorizationController.GetQuoteAuthorization:L22](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/QuoteAuthorizationController.cs#L22) | sphere.federation |
| POST | `/quote-authorizations` | [QuoteAuthorizationController.CreateQuoteAuthorization:L64](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/QuoteAuthorizationController.cs#L64) | sphere.federation |
| DELETE | `/quote-authorizations/{id:guid}` | [QuoteAuthorizationController.RevokeQuoteAuthorization:L111](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/QuoteAuthorizationController.cs#L111) | sphere.federation |
| GET | `/activitypub/actor` | [ServerActorController.GetServerActor:L26](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/ServerActorController.cs#L26) | sphere.federation |
| GET | `/activitypub/actor/outbox` | [ServerActorController.GetServerOutbox:L66](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/ServerActorController.cs#L66) | sphere.federation |
| GET | `/activitypub/actor/followers` | [ServerActorController.GetServerFollowers:L80](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/ServerActorController.cs#L80) | sphere.federation |
| GET | `/activitypub/actor/following` | [ServerActorController.GetServerFollowing:L94](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/ServerActorController.cs#L94) | sphere.federation |
| GET | `/activitypub/actor/main-key` | [ServerActorController.GetServerMainKey:L108](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/ServerActorController.cs#L108) | sphere.federation |
| GET | `/.well-known/host-meta` | [WebFingerController.GetHostMeta:L18](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/WebFingerController.cs#L18) | sphere.federation_discovery |
| GET | `/.well-known/webfinger` | [WebFingerController.GetWebFinger:L32](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/WebFingerController.cs#L32) | sphere.federation_discovery |
| POST | `/api/autocomplete` | [AutocompletionController.TextAutocomplete:L14](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Autocompletion/AutocompletionController.cs#L14) | sphere.content_utilities |
| GET | `/api/automod/rules` | [AutomodController.ListRules:L21](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Automod/AutomodController.cs#L21) | sphere.posts |
| GET | `/api/automod/rules/{id:guid}` | [AutomodController.GetRule:L42](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Automod/AutomodController.cs#L42) | sphere.posts |
| POST | `/api/automod/rules` | [AutomodController.CreateRule:L52](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Automod/AutomodController.cs#L52) | sphere.posts |
| PUT | `/api/automod/rules/{id:guid}` | [AutomodController.UpdateRule:L81](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Automod/AutomodController.cs#L81) | sphere.posts |
| DELETE | `/api/automod/rules/{id:guid}` | [AutomodController.DeleteRule:L119](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Automod/AutomodController.cs#L119) | sphere.posts |
| POST | `/api/automod/rules/bulk` | [AutomodController.BulkUpsert:L134](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Automod/AutomodController.cs#L134) | sphere.posts |
| POST | `/api/automod/rules/{id:guid}/test` | [AutomodController.TestRule:L197](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Automod/AutomodController.cs#L197) | sphere.posts |
| POST | `/api/livestreams` | [LiveStreamController.Create:L55](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Live/LiveStreamController.cs#L55) | sphere.live_ads |
| GET | `/api/livestreams` | [LiveStreamController.GetActive:L121](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Live/LiveStreamController.cs#L121) | sphere.live_ads |
| GET | `/api/livestreams/publisher/{publisherId:guid}` | [LiveStreamController.GetByPublisher:L134](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Live/LiveStreamController.cs#L134) | sphere.live_ads |
| GET | `/api/livestreams/{id:guid}` | [LiveStreamController.GetById:L148](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Live/LiveStreamController.cs#L148) | sphere.live_ads |
| GET | `/api/livestreams/{id:guid}/token` | [LiveStreamController.GetToken:L174](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Live/LiveStreamController.cs#L174) | sphere.live_ads |
| POST | `/api/livestreams/{id:guid}/start` | [LiveStreamController.StartStreaming:L237](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Live/LiveStreamController.cs#L237) | sphere.live_ads |
| POST | `/api/livestreams/{id:guid}/egress` | [LiveStreamController.StartEgress:L290](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Live/LiveStreamController.cs#L290) | sphere.live_ads |
| POST | `/api/livestreams/{id:guid}/egress/stop` | [LiveStreamController.StopEgress:L322](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Live/LiveStreamController.cs#L322) | sphere.live_ads |
| POST | `/api/livestreams/{id:guid}/hls` | [LiveStreamController.StartHlsEgress:L347](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Live/LiveStreamController.cs#L347) | sphere.live_ads |
| POST | `/api/livestreams/{id:guid}/hls/stop` | [LiveStreamController.StopHlsEgress:L398](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Live/LiveStreamController.cs#L398) | sphere.live_ads |
| POST | `/api/livestreams/{id:guid}/end` | [LiveStreamController.End:L423](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Live/LiveStreamController.cs#L423) | sphere.live_ads |
| PATCH | `/api/livestreams/{id:guid}` | [LiveStreamController.Update:L448](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Live/LiveStreamController.cs#L448) | sphere.live_ads |
| DELETE | `/api/livestreams/{id:guid}` | [LiveStreamController.Delete:L514](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Live/LiveStreamController.cs#L514) | sphere.live_ads |
| GET | `/api/livestreams/{id:guid}/details` | [LiveStreamController.GetRoomDetails:L551](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Live/LiveStreamController.cs#L551) | sphere.live_ads |
| PATCH | `/api/livestreams/{id:guid}/thumbnail` | [LiveStreamController.UpdateThumbnail:L574](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Live/LiveStreamController.cs#L574) | sphere.live_ads |
| GET | `/api/livestreams/{id:guid}/chat` | [LiveStreamController.GetChatMessages:L612](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Live/LiveStreamController.cs#L612) | sphere.live_ads |
| POST | `/api/livestreams/{id:guid}/chat` | [LiveStreamController.SendChatMessage:L640](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Live/LiveStreamController.cs#L640) | sphere.live_ads |
| DELETE | `/api/livestreams/{id:guid}/chat/{messageId:guid}` | [LiveStreamController.DeleteChatMessage:L689](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Live/LiveStreamController.cs#L689) | sphere.live_ads |
| POST | `/api/livestreams/{id:guid}/chat/{messageId:guid}/timeout` | [LiveStreamController.TimeoutUser:L714](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Live/LiveStreamController.cs#L714) | sphere.live_ads |
| GET | `/api/livestreams/{id:guid}/awards` | [LiveStreamController.GetLiveStreamAwards:L754](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Live/LiveStreamController.cs#L754) | sphere.live_ads |
| GET | `/api/livestreams/{id:guid}/awards/active` | [LiveStreamController.GetActiveAwards:L779](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Live/LiveStreamController.cs#L779) | sphere.live_ads |
| GET | `/api/livestreams/{id:guid}/awards/leaderboard` | [LiveStreamController.GetLiveStreamAwardLeaderboard:L812](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Live/LiveStreamController.cs#L812) | sphere.live_ads |
| POST | `/api/livestreams/{id:guid}/awards` | [LiveStreamController.AwardLiveStream:L864](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Live/LiveStreamController.cs#L864) | sphere.live_ads |
| GET | `/api/ads/{name}` | [AdsController.ListPublisherAds:L16](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/AdsController.cs#L16) | sphere.live_ads |
| POST | `/api/posts/blog/check-permission` | [PostActionController.CheckBlogCreatePermission:L309](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostActionController.cs#L309) | sphere.posts |
| POST | `/api/posts` | [PostActionController.CreatePost:L353](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostActionController.cs#L353) | sphere.posts |
| POST | `/api/posts/{id:guid}/reactions` | [PostActionController.ReactPost:L804](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostActionController.cs#L804) | sphere.posts |
| POST | `/api/posts/{id:guid}/bookmark` | [PostActionController.BookmarkPost:L881](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostActionController.cs#L881) | sphere.posts |
| DELETE | `/api/posts/{id:guid}/bookmark` | [PostActionController.UnbookmarkPost:L933](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostActionController.cs#L933) | sphere.posts |
| GET | `/api/posts/{id:guid}/bookmark` | [PostActionController.GetBookmarkStatus:L965](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostActionController.cs#L965) | sphere.posts |
| GET | `/api/posts/{id:guid}/awards` | [PostActionController.GetPostAwards:L988](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostActionController.cs#L988) | sphere.posts |
| GET | `/api/posts/{id:guid}/awards/pending` | [PostActionController.GetPendingPostAwards:L1005](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostActionController.cs#L1005) | sphere.posts |
| POST | `/api/posts/{id:guid}/awards` | [PostActionController.AwardPost:L1028](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostActionController.cs#L1028) | sphere.posts |
| POST | `/api/posts/{id:guid}/sponsor` | [PostActionController.SponsorPost:L1097](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostActionController.cs#L1097) | sphere.posts |
| POST | `/api/posts/{id:guid}/pin` | [PostActionController.PinPost:L1139](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostActionController.cs#L1139) | sphere.posts |
| DELETE | `/api/posts/{id:guid}/pin` | [PostActionController.UnpinPost:L1196](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostActionController.cs#L1196) | sphere.posts |
| PATCH | `/api/posts/{id:guid}` | [PostActionController.UpdatePost:L1252](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostActionController.cs#L1252) | sphere.posts |
| DELETE | `/api/posts/{id:guid}` | [PostActionController.DeletePost:L1664](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostActionController.cs#L1664) | sphere.posts |
| POST | `/api/posts/batch/delete` | [PostActionController.BatchDeletePosts:L1708](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostActionController.cs#L1708) | sphere.posts |
| POST | `/api/posts/batch/visibility` | [PostActionController.BatchUpdateVisibility:L1764](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostActionController.cs#L1764) | sphere.posts |
| POST | `/api/posts/{id:guid}/publish` | [PostActionController.PublishDraft:L1832](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostActionController.cs#L1832) | sphere.posts |
| POST | `/api/posts/{id:guid}/boost` | [PostActionController.BoostPost:L1889](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostActionController.cs#L1889) | sphere.posts |
| DELETE | `/api/posts/{id:guid}/boost` | [PostActionController.UnboostPost:L1995](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostActionController.cs#L1995) | sphere.posts |
| GET | `/api/posts/{id:guid}/boosts` | [PostActionController.GetPostBoosts:L2062](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostActionController.cs#L2062) | sphere.posts |
| POST | `/api/posts/{id:guid}/realm/moderate` | [PostActionController.ModeratePostInRealm:L2083](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostActionController.cs#L2083) | sphere.posts |
| GET | `/api/admin/posts` | [PostAdminController.ListPosts:L43](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostAdminController.cs#L43) | sphere.posts |
| GET | `/api/admin/posts/{id:guid}` | [PostAdminController.GetPost:L105](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostAdminController.cs#L105) | sphere.posts |
| GET | `/api/admin/posts/{id:guid}/lock` | [PostAdminController.GetLockStatus:L124](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostAdminController.cs#L124) | sphere.posts |
| POST | `/api/admin/posts/{id:guid}/lock` | [PostAdminController.LockPost:L134](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostAdminController.cs#L134) | sphere.posts |
| DELETE | `/api/admin/posts/{id:guid}/lock` | [PostAdminController.UnlockPost:L151](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostAdminController.cs#L151) | sphere.posts |
| POST | `/api/admin/posts/{id:guid}/visibility` | [PostAdminController.SetVisibility:L168](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostAdminController.cs#L168) | sphere.posts |
| POST | `/api/admin/posts/{id:guid}/shadowban` | [PostAdminController.ShadowbanPost:L204](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostAdminController.cs#L204) | sphere.posts |
| DELETE | `/api/admin/posts/{id:guid}/shadowban` | [PostAdminController.UnshadowbanPost:L231](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostAdminController.cs#L231) | sphere.posts |
| POST | `/api/admin/posts/{id:guid}/realm/remove` | [PostAdminController.RemovePostFromRealm:L251](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostAdminController.cs#L251) | sphere.posts |
| DELETE | `/api/admin/posts/{id:guid}` | [PostAdminController.DeletePost:L303](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostAdminController.cs#L303) | sphere.posts |
| POST | `/api/admin/posts/{id:guid}/lock/batch` | [PostAdminController.LockPostsBatch:L319](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostAdminController.cs#L319) | sphere.posts |
| DELETE | `/api/admin/posts/lock/batch` | [PostAdminController.UnlockPostsBatch:L336](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostAdminController.cs#L336) | sphere.posts |
| GET | `/api/admin/categories` | [PostCategoryAdminController.ListCategories:L36](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCategoryAdminController.cs#L36) | sphere.taxonomy |
| GET | `/api/admin/categories/{slug}` | [PostCategoryAdminController.GetCategory:L84](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCategoryAdminController.cs#L84) | sphere.taxonomy |
| POST | `/api/admin/categories` | [PostCategoryAdminController.CreateCategory:L101](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCategoryAdminController.cs#L101) | sphere.taxonomy |
| PATCH | `/api/admin/categories/{slug}` | [PostCategoryAdminController.UpdateCategory:L133](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCategoryAdminController.cs#L133) | sphere.taxonomy |
| DELETE | `/api/admin/categories/{slug}` | [PostCategoryAdminController.DeleteCategory:L176](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCategoryAdminController.cs#L176) | sphere.taxonomy |
| GET | `/api/posts/categories` | [PostCategoryController.ListCategories:L19](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCategoryController.cs#L19) | sphere.taxonomy |
| GET | `/api/posts/tags` | [PostCategoryController.ListTags:L74](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCategoryController.cs#L74) | sphere.taxonomy |
| GET | `/api/posts/categories/{slug}` | [PostCategoryController.GetCategory:L129](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCategoryController.cs#L129) | sphere.taxonomy |
| POST | `/api/posts/categories/{slug}/subscribe` | [PostCategoryController.SubscribeCategory:L138](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCategoryController.cs#L138) | sphere.taxonomy |
| POST | `/api/posts/categories/{slug}/unsubscribe` | [PostCategoryController.UnsubscribeCategory:L170](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCategoryController.cs#L170) | sphere.taxonomy |
| GET | `/api/posts/categories/{slug}/subscription` | [PostCategoryController.GetCategorySubscription:L194](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCategoryController.cs#L194) | sphere.taxonomy |
| POST | `/api/posts/tags/{slug}/subscribe` | [PostCategoryController.SubscribeTag:L214](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCategoryController.cs#L214) | sphere.taxonomy |
| POST | `/api/posts/tags/{slug}/unsubscribe` | [PostCategoryController.UnsubscribeTag:L248](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCategoryController.cs#L248) | sphere.taxonomy |
| GET | `/api/posts/tags/{slug}/subscription` | [PostCategoryController.GetTagSubscription:L276](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCategoryController.cs#L276) | sphere.taxonomy |
| GET | `/api/categories/subscriptions` | [PostCategorySubController.ListCategoriesSubscription:L20](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCategorySubController.cs#L20) | sphere.taxonomy |
| GET | `/api/admin/collections` | [PostCollectionAdminController.ListCollections:L31](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCollectionAdminController.cs#L31) | sphere.posts |
| GET | `/api/admin/collections/{id:guid}` | [PostCollectionAdminController.GetCollection:L85](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCollectionAdminController.cs#L85) | sphere.posts |
| PATCH | `/api/admin/collections/{id:guid}` | [PostCollectionAdminController.UpdateCollection:L102](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCollectionAdminController.cs#L102) | sphere.posts |
| DELETE | `/api/admin/collections/{id:guid}` | [PostCollectionAdminController.DeleteCollection:L139](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCollectionAdminController.cs#L139) | sphere.posts |
| GET | `/api/collections` | [PostCollectionController.ListAllCollections:L131](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCollectionController.cs#L131) | sphere.posts |
| GET | `/api/publishers/{publisherName}/collections` | [PostCollectionController.ListCollections:L162](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCollectionController.cs#L162) | sphere.posts |
| POST | `/api/publishers/{publisherName}/collections` | [PostCollectionController.CreateCollection:L169](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCollectionController.cs#L169) | sphere.posts |
| GET | `/api/publishers/{publisherName}/collections/{slug}` | [PostCollectionController.GetCollection:L210](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCollectionController.cs#L210) | sphere.posts |
| POST | `/api/publishers/{publisherName}/collections/{slug}/subscribe` | [PostCollectionController.SubscribeCollection:L219](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCollectionController.cs#L219) | sphere.posts |
| POST | `/api/publishers/{publisherName}/collections/{slug}/unsubscribe` | [PostCollectionController.UnsubscribeCollection:L254](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCollectionController.cs#L254) | sphere.posts |
| GET | `/api/publishers/{publisherName}/collections/{slug}/subscription` | [PostCollectionController.GetCollectionSubscription:L278](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCollectionController.cs#L278) | sphere.posts |
| PATCH | `/api/publishers/{publisherName}/collections/{slug}` | [PostCollectionController.UpdateCollection:L301](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCollectionController.cs#L301) | sphere.posts |
| DELETE | `/api/publishers/{publisherName}/collections/{slug}` | [PostCollectionController.DeleteCollection:L333](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCollectionController.cs#L333) | sphere.posts |
| GET | `/api/publishers/{publisherName}/collections/{slug}/posts` | [PostCollectionController.ListCollectionPosts:L349](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCollectionController.cs#L349) | sphere.posts |
| POST | `/api/publishers/{publisherName}/collections/{slug}/posts` | [PostCollectionController.AddCollectionPost:L371](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCollectionController.cs#L371) | sphere.posts |
| POST | `/api/publishers/{publisherName}/collections/{slug}/posts/batch` | [PostCollectionController.BatchAddCollectionPosts:L402](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCollectionController.cs#L402) | sphere.posts |
| POST | `/api/publishers/{publisherName}/collections/{slug}/posts/batch/remove` | [PostCollectionController.BatchRemoveCollectionPosts:L430](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCollectionController.cs#L430) | sphere.posts |
| DELETE | `/api/publishers/{publisherName}/collections/{slug}/posts/{postId:guid}` | [PostCollectionController.RemoveCollectionPost:L451](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCollectionController.cs#L451) | sphere.posts |
| PUT | `/api/publishers/{publisherName}/collections/{slug}/posts/reorder` | [PostCollectionController.ReorderCollectionPosts:L468](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCollectionController.cs#L468) | sphere.posts |
| GET | `/api/publishers/{publisherName}/collections/{slug}/posts/{postId:guid}/prev` | [PostCollectionController.GetPrevCollectionPost:L488](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCollectionController.cs#L488) | sphere.posts |
| GET | `/api/publishers/{publisherName}/collections/{slug}/posts/{postId:guid}/next` | [PostCollectionController.GetNextCollectionPost:L494](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostCollectionController.cs#L494) | sphere.posts |
| GET | `/api/posts/featured` | [PostController.ListFeaturedPosts:L275](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostController.cs#L275) | sphere.posts |
| GET | `/api/posts/sponsor/current` | [PostController.GetCurrentSponsoredPost:L285](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostController.cs#L285) | sphere.posts |
| GET | `/api/posts/sponsor/leaderboard` | [PostController.GetSponsorLeaderboard:L293](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostController.cs#L293) | sphere.posts |
| GET | `/api/posts/{id:guid}/sponsor` | [PostController.GetPostSponsorship:L300](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostController.cs#L300) | sphere.posts |
| GET | `/api/posts/{id:guid}/sponsor/history` | [PostController.GetPostSponsorHistory:L307](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostController.cs#L307) | sphere.posts |
| GET | `/api/posts/drafts` | [PostController.ListDrafts:L334](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostController.cs#L334) | sphere.posts |
| GET | `/api/posts/bookmarks` | [PostController.ListBookmarks:L392](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostController.cs#L392) | sphere.posts |
| GET | `/api/posts` | [PostController.ListPosts:L472](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostController.cs#L472) | sphere.posts |
| GET | `/api/posts/{publisherName}/{slug}` | [PostController.GetPost:L880](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostController.cs#L880) | sphere.posts |
| GET | `/api/posts/{id:guid}` | [PostController.GetPost:L956](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostController.cs#L956) | sphere.posts |
| GET | `/api/posts/{id:guid}/prev` | [PostController.GetPrevPost:L1028](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostController.cs#L1028) | sphere.posts |
| GET | `/api/posts/{id:guid}/next` | [PostController.GetNextPost:L1198](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostController.cs#L1198) | sphere.posts |
| GET | `/api/posts/{id:guid}/reactions` | [PostController.GetReactions:L1368](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostController.cs#L1368) | sphere.posts |
| GET | `/api/posts/reactions/users/{name}` | [PostController.ListUserReactions:L1412](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostController.cs#L1412) | sphere.posts |
| GET | `/api/posts/{id:guid}/replies/featured` | [PostController.GetFeaturedReply:L1520](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostController.cs#L1520) | sphere.posts |
| GET | `/api/posts/{id:guid}/replies/pinned` | [PostController.ListPinnedReplies:L1559](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostController.cs#L1559) | sphere.posts |
| GET | `/api/posts/{id:guid}/replies` | [PostController.ListReplies:L1595](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostController.cs#L1595) | sphere.posts |
| GET | `/api/posts/{id:guid}/replies/threaded` | [PostController.ListThreadedReplies:L1661](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostController.cs#L1661) | sphere.posts |
| GET | `/api/posts/{id:guid}/thread` | [PostController.GetThread:L1779](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostController.cs#L1779) | sphere.posts |
| GET | `/api/posts/{id:guid}/chain` | [PostController.GetChain:L2016](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostController.cs#L2016) | sphere.posts |
| GET | `/api/posts/{id:guid}/forwards` | [PostController.ListForwards:L2072](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostController.cs#L2072) | sphere.posts |
| GET | `/api/admin/stats` | [PostStatsAdminController.GetStats:L31](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostStatsAdminController.cs#L31) | sphere.posts |
| POST | `/api/posts/tags` | [PostTagController.CreateTag:L63](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostTagController.cs#L63) | sphere.taxonomy |
| GET | `/api/posts/tags/{slug}` | [PostTagController.GetTag:L88](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostTagController.cs#L88) | sphere.taxonomy |
| PATCH | `/api/posts/tags/{slug}` | [PostTagController.UpdateTag:L96](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostTagController.cs#L96) | sphere.taxonomy |
| POST | `/api/posts/tags/{slug}/claim` | [PostTagController.ClaimTag:L121](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostTagController.cs#L121) | sphere.taxonomy |
| POST | `/api/posts/tags/{slug}/release` | [PostTagController.ReleaseTag:L154](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostTagController.cs#L154) | sphere.taxonomy |
| PATCH | `/api/posts/tags/{slug}/protect` | [PostTagController.SetProtected:L193](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostTagController.cs#L193) | sphere.taxonomy |
| GET | `/api/posts/tags/quota` | [PostTagController.GetProtectedTagQuota:L235](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostTagController.cs#L235) | sphere.taxonomy |
| GET | `/api/posts/tags/{slug}/quota` | [PostTagController.GetProtectedTagQuota:L236](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostTagController.cs#L236) | sphere.taxonomy |
| GET | `/api/admin/posts/tags` | [PostTagAdminController.ListTags:L295](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostTagController.cs#L295) | sphere.taxonomy |
| GET | `/api/admin/tags` | [PostTagAdminController.ListTags:L296](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostTagController.cs#L296) | sphere.taxonomy |
| GET | `/api/admin/posts/tags/{slug}` | [PostTagAdminController.GetTag:L361](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostTagController.cs#L361) | sphere.taxonomy |
| GET | `/api/admin/tags/{slug}` | [PostTagAdminController.GetTag:L362](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostTagController.cs#L362) | sphere.taxonomy |
| POST | `/api/admin/posts/tags` | [PostTagAdminController.CreateTag:L376](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostTagController.cs#L376) | sphere.taxonomy |
| POST | `/api/admin/tags` | [PostTagAdminController.CreateTag:L377](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostTagController.cs#L377) | sphere.taxonomy |
| POST | `/api/admin/posts/tags/{slug}/assign` | [PostTagAdminController.AssignTag:L412](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostTagController.cs#L412) | sphere.taxonomy |
| POST | `/api/admin/tags/{slug}/assign` | [PostTagAdminController.AssignTag:L413](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostTagController.cs#L413) | sphere.taxonomy |
| DELETE | `/api/admin/posts/tags/{slug}/assign` | [PostTagAdminController.UnassignTag:L435](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostTagController.cs#L435) | sphere.taxonomy |
| DELETE | `/api/admin/tags/{slug}/assign` | [PostTagAdminController.UnassignTag:L436](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostTagController.cs#L436) | sphere.taxonomy |
| PATCH | `/api/admin/posts/tags/{slug}/protect` | [PostTagAdminController.SetProtected:L455](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostTagController.cs#L455) | sphere.taxonomy |
| PATCH | `/api/admin/tags/{slug}/protect` | [PostTagAdminController.SetProtected:L456](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostTagController.cs#L456) | sphere.taxonomy |
| PATCH | `/api/admin/posts/tags/{slug}/event` | [PostTagAdminController.SetEvent:L487](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostTagController.cs#L487) | sphere.taxonomy |
| PATCH | `/api/admin/tags/{slug}/event` | [PostTagAdminController.SetEvent:L488](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostTagController.cs#L488) | sphere.taxonomy |
| PATCH | `/api/admin/posts/tags/{slug}` | [PostTagAdminController.AdminUpdateTag:L511](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostTagController.cs#L511) | sphere.taxonomy |
| PATCH | `/api/admin/tags/{slug}` | [PostTagAdminController.AdminUpdateTag:L512](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostTagController.cs#L512) | sphere.taxonomy |
| DELETE | `/api/admin/posts/tags/{slug}` | [PostTagAdminController.DeleteTag:L539](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostTagController.cs#L539) | sphere.taxonomy |
| DELETE | `/api/admin/tags/{slug}` | [PostTagAdminController.DeleteTag:L540](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostTagController.cs#L540) | sphere.taxonomy |
| GET | `/api/posts/watch` | [PostWatchController.GetPostWatchPreferences:L43](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostWatchController.cs#L43) | sphere.posts |
| PUT | `/api/posts/watch` | [PostWatchController.UpdatePostWatchPreferences:L55](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostWatchController.cs#L55) | sphere.posts |
| GET | `/api/admin/publishers` | [PublisherAdminController.ListPublishers:L58](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherAdminController.cs#L58) | sphere.publishers |
| GET | `/api/admin/publishers/{name}` | [PublisherAdminController.GetPublisher:L115](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherAdminController.cs#L115) | sphere.publishers |
| PATCH | `/api/admin/publishers/{name}` | [PublisherAdminController.UpdatePublisher:L147](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherAdminController.cs#L147) | sphere.publishers |
| POST | `/api/admin/publishers/{name}/shadowban` | [PublisherAdminController.ShadowbanPublisher:L197](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherAdminController.cs#L197) | sphere.publishers |
| DELETE | `/api/admin/publishers/{name}/shadowban` | [PublisherAdminController.UnshadowbanPublisher:L225](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherAdminController.cs#L225) | sphere.publishers |
| POST | `/api/admin/publishers/{name}/verification` | [PublisherAdminController.SetVerification:L246](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherAdminController.cs#L246) | sphere.publishers |
| DELETE | `/api/admin/publishers/{name}/verification` | [PublisherAdminController.ClearVerification:L276](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherAdminController.cs#L276) | sphere.publishers |
| DELETE | `/api/admin/publishers/{name}` | [PublisherAdminController.DeletePublisher:L296](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherAdminController.cs#L296) | sphere.publishers |
| GET | `/api/publishers/quota` | [PublisherController.GetQuota:L43](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherController.cs#L43) | sphere.publishers |
| GET | `/api/publishers` | [PublisherController.ListManagedPublishers:L56](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherController.cs#L56) | sphere.publishers |
| GET | `/api/publishers/invites` | [PublisherController.ListInvites:L90](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherController.cs#L90) | sphere.publishers |
| POST | `/api/publishers/invites/{name}` | [PublisherController.InviteMember:L118](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherController.cs#L118) | sphere.publishers |
| POST | `/api/publishers/invites/{name}/accept` | [PublisherController.AcceptMemberInvite:L169](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherController.cs#L169) | sphere.publishers |
| POST | `/api/publishers/invites/{name}/decline` | [PublisherController.DeclineMemberInvite:L207](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherController.cs#L207) | sphere.publishers |
| DELETE | `/api/publishers/{name}/members/{memberId:guid}` | [PublisherController.RemoveMember:L243](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherController.cs#L243) | sphere.publishers |
| DELETE | `/api/publishers/{name}/members/me` | [PublisherController.LeavePublisher:L289](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherController.cs#L289) | sphere.publishers |
| PATCH | `/api/publishers/{name}/members/{memberId:guid}/role` | [PublisherController.UpdateMemberRole:L329](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherController.cs#L329) | sphere.publishers |
| POST | `/api/publishers/individual` | [PublisherController.CreatePublisherIndividual:L414](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherController.cs#L414) | sphere.publishers |
| POST | `/api/publishers/organization/{realmSlug}` | [PublisherController.CreatePublisherOrganization:L502](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherController.cs#L502) | sphere.publishers |
| PATCH | `/api/publishers/{name}` | [PublisherController.UpdatePublisher:L598](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherController.cs#L598) | sphere.publishers |
| DELETE | `/api/publishers/{name}` | [PublisherController.DeletePublisher:L756](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherController.cs#L756) | sphere.publishers |
| GET | `/api/publishers/{name}/members` | [PublisherController.ListMembers:L799](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherController.cs#L799) | sphere.publishers |
| GET | `/api/publishers/{name}/members/me` | [PublisherController.GetCurrentIdentity:L824](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherController.cs#L824) | sphere.publishers |
| GET | `/api/publishers/{name}/features` | [PublisherController.ListPublisherFeatures:L849](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherController.cs#L849) | sphere.publishers |
| GET | `/api/publishers/{name}/rewards` | [PublisherController.GetPublisherExpectedReward:L866](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherController.cs#L866) | sphere.publishers |
| GET | `/api/publishers/{name}/rating/history` | [PublisherController.GetPublisherRatingHistory:L887](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherController.cs#L887) | sphere.publishers |
| POST | `/api/publishers/{name}/features` | [PublisherController.AddPublisherFeature:L932](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherController.cs#L932) | sphere.publishers |
| DELETE | `/api/publishers/{name}/features` | [PublisherController.RemovePublisherFeature:L1002](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherController.cs#L1002) | sphere.publishers |
| POST | `/api/publishers/rewards/settle` | [PublisherController.SettlePublisherAward:L1046](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherController.cs#L1046) | sphere.publishers |
| POST | `/api/publishers/awards/settle` | [PublisherController.SettlePostAwards:L1056](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherController.cs#L1056) | sphere.publishers |
| POST | `/api/publishers/rewards/resettle` | [PublisherController.AggressiveResettle:L1072](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherController.cs#L1072) | sphere.publishers |
| GET | `/api/publishers/{name}/fediverse` | [PublisherController.GetFediverseStatus:L1090](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherController.cs#L1090) | sphere.publishers |
| POST | `/api/publishers/{name}/fediverse` | [PublisherController.EnableFediverse:L1105](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherController.cs#L1105) | sphere.publishers |
| PATCH | `/api/publishers/{name}/fediverse` | [PublisherController.UpdateFediverseActor:L1137](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherController.cs#L1137) | sphere.publishers |
| DELETE | `/api/publishers/{name}/fediverse` | [PublisherController.DisableFediverse:L1208](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherController.cs#L1208) | sphere.publishers |
| GET | `/api/publishers/{name}/domains` | [PublisherController.ListDomains:L1244](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherController.cs#L1244) | sphere.publishers |
| POST | `/api/publishers/{name}/domains` | [PublisherController.AddDomain:L1267](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherController.cs#L1267) | sphere.publishers |
| DELETE | `/api/publishers/{name}/domains/{domainId:guid}` | [PublisherController.RemoveDomain:L1314](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherController.cs#L1314) | sphere.publishers |
| POST | `/api/publishers/{name}/domains/{domainId:guid}/recheck` | [PublisherController.RecheckDomain:L1341](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherController.cs#L1341) | sphere.publishers |
| GET | `/api/publishers/{name}/ads` | [PublisherController.ListAdvertisingPosts:L1374](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherController.cs#L1374) | sphere.publishers |
| GET | `/api/publishers/search` | [PublisherPublicController.SearchPublishers:L80](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherPublicController.cs#L80) | sphere.publishers |
| GET | `/api/publishers/{name}` | [PublisherPublicController.GetPublisher:L118](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherPublicController.cs#L118) | sphere.publishers |
| GET | `/api/publishers/{name}/heatmap` | [PublisherPublicController.GetPublisherHeatmap:L138](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherPublicController.cs#L138) | sphere.publishers |
| GET | `/api/publishers/{name}/stats` | [PublisherPublicController.GetPublisherStats:L147](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherPublicController.cs#L147) | sphere.publishers |
| GET | `/api/publishers/{name}/content-stats` | [PublisherPublicController.GetPublisherContentStats:L156](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherPublicController.cs#L156) | sphere.publishers |
| GET | `/api/publishers/of/{accountId:guid}` | [PublisherPublicController.GetAccountManagedPublishers:L167](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherPublicController.cs#L167) | sphere.publishers |
| GET | `/api/publishers/{name}/rating` | [PublisherPublicController.GetPublisherRating:L179](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherPublicController.cs#L179) | sphere.publishers |
| GET | `/api/publishers/leaderboard` | [PublisherPublicController.GetLeaderboard:L190](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherPublicController.cs#L190) | sphere.publishers |
| GET | `/api/publishers/{name}/rating/overview` | [PublisherPublicController.GetRatingOverview:L202](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherPublicController.cs#L202) | sphere.publishers |
| GET | `/api/publishers/{name}/subscription` | [PublisherSubscriptionController.CheckSubscriptionStatus:L53](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherSubscriptionController.cs#L53) | sphere.publishers |
| GET | `/api/publishers/{name}/subscription/read-status` | [PublisherSubscriptionController.GetSubscriptionReadStatus:L120](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherSubscriptionController.cs#L120) | sphere.publishers |
| POST | `/api/publishers/{name}/subscribe` | [PublisherSubscriptionController.Subscribe:L143](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherSubscriptionController.cs#L143) | sphere.publishers |
| POST | `/api/publishers/{name}/unsubscribe` | [PublisherSubscriptionController.Unsubscribe:L274](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherSubscriptionController.cs#L274) | sphere.publishers |
| GET | `/api/publishers/subscriptions` | [PublisherSubscriptionController.GetCurrentSubscriptions:L344](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherSubscriptionController.cs#L344) | sphere.publishers |
| GET | `/api/publishers/{name}/subscribers` | [PublisherSubscriptionController.ListSubscribers:L460](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherSubscriptionController.cs#L460) | sphere.publishers |
| POST | `/api/publishers/{name}/subscribers/{accountId:guid}` | [PublisherSubscriptionController.AddSubscriber:L517](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherSubscriptionController.cs#L517) | sphere.publishers |
| DELETE | `/api/publishers/{name}/subscribers/{accountId:guid}` | [PublisherSubscriptionController.RemoveSubscriber:L577](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherSubscriptionController.cs#L577) | sphere.publishers |
| PATCH | `/api/publishers/{name}/subscription/me/notify` | [PublisherSubscriptionController.UpdateMySubscriptionNotify:L602](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherSubscriptionController.cs#L602) | sphere.publishers |
| PUT | `/api/publishers/{name}/subscription/read-status` | [PublisherSubscriptionController.UpdateSubscriptionReadStatus:L630](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherSubscriptionController.cs#L630) | sphere.publishers |
| PUT | `/api/publishers/subscriptions/read-status` | [PublisherSubscriptionController.MarkAllSubscriptionsRead:L665](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherSubscriptionController.cs#L665) | sphere.publishers |
| GET | `/api/publishers/subscriptions/live` | [PublisherSubscriptionController.GetSubscribedPublisherLiveStreams:L681](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherSubscriptionController.cs#L681) | sphere.publishers |
| GET | `/api/publishers/{name}/subscription/requests` | [PublisherSubscriptionController.GetPendingFollowRequests:L724](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherSubscriptionController.cs#L724) | sphere.publishers |
| POST | `/api/publishers/{name}/subscription/requests/{requestId}/approve` | [PublisherSubscriptionController.ApproveFollowRequest:L765](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherSubscriptionController.cs#L765) | sphere.publishers |
| POST | `/api/publishers/{name}/subscription/requests/{requestId}/reject` | [PublisherSubscriptionController.RejectFollowRequest:L822](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherSubscriptionController.cs#L822) | sphere.publishers |
| GET | `/api/publishers/blocked` | [PublisherSubscriptionController.GetBlockedPublishers:L888](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherSubscriptionController.cs#L888) | sphere.publishers |
| GET | `/api/publishers/muted` | [PublisherSubscriptionController.GetMutedPublishers:L899](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherSubscriptionController.cs#L899) | sphere.publishers |
| GET | `/api/publishers/{name}/relationship` | [PublisherSubscriptionController.GetPublisherRelationship:L910](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherSubscriptionController.cs#L910) | sphere.publishers |
| POST | `/api/publishers/{name}/block` | [PublisherSubscriptionController.BlockPublisher:L934](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherSubscriptionController.cs#L934) | sphere.publishers |
| DELETE | `/api/publishers/{name}/block` | [PublisherSubscriptionController.UnblockPublisher:L959](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherSubscriptionController.cs#L959) | sphere.publishers |
| POST | `/api/publishers/{name}/mute` | [PublisherSubscriptionController.MutePublisher:L984](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherSubscriptionController.cs#L984) | sphere.publishers |
| DELETE | `/api/publishers/{name}/mute` | [PublisherSubscriptionController.UnmutePublisher:L1009](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherSubscriptionController.cs#L1009) | sphere.publishers |
| GET | `/api/scrap/link` | [WebReaderController.ScrapLink:L16](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Reader/WebReaderController.cs#L16) | sphere.content_utilities |
| DELETE | `/api/scrap/link/cache` | [WebReaderController.InvalidateCache:L47](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Reader/WebReaderController.cs#L47) | sphere.content_utilities |
| DELETE | `/api/scrap/cache/all` | [WebReaderController.InvalidateAllCache:L59](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Reader/WebReaderController.cs#L59) | sphere.content_utilities |
| GET | `/api/stickers` | [StickerController.ListStickerPacks:L49](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Sticker/StickerController.cs#L49) | sphere.stickers_surveys |
| GET | `/api/stickers/me` | [StickerController.ListStickerPacksOwned:L99](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Sticker/StickerController.cs#L99) | sphere.stickers_surveys |
| GET | `/api/stickers/{id:guid}` | [StickerController.GetStickerPack:L118](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Sticker/StickerController.cs#L118) | sphere.stickers_surveys |
| GET | `/api/stickers/by-prefix/{prefix}` | [StickerController.GetStickerPackByPrefix:L129](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Sticker/StickerController.cs#L129) | sphere.stickers_surveys |
| POST | `/api/stickers` | [StickerController.CreateStickerPack:L148](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Sticker/StickerController.cs#L148) | sphere.stickers_surveys |
| PATCH | `/api/stickers/{id:guid}` | [StickerController.UpdateStickerPack:L196](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Sticker/StickerController.cs#L196) | sphere.stickers_surveys |
| DELETE | `/api/stickers/{id:guid}` | [StickerController.DeleteStickerPack:L243](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Sticker/StickerController.cs#L243) | sphere.stickers_surveys |
| GET | `/api/stickers/{packId:guid}/content` | [StickerController.ListStickers:L267](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Sticker/StickerController.cs#L267) | sphere.stickers_surveys |
| GET | `/api/stickers/lookup/{identifier}` | [StickerController.GetStickerByIdentifier:L279](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Sticker/StickerController.cs#L279) | sphere.stickers_surveys |
| GET | `/api/stickers/lookup/{identifier}/open` | [StickerController.OpenStickerByIdentifier:L288](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Sticker/StickerController.cs#L288) | sphere.stickers_surveys |
| POST | `/api/stickers/lookup/batch` | [StickerController.LookupStickersByPlaceholderBatch:L297](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Sticker/StickerController.cs#L297) | sphere.stickers_surveys |
| GET | `/api/stickers/search` | [StickerController.SearchSticker:L311](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Sticker/StickerController.cs#L311) | sphere.stickers_surveys |
| GET | `/api/stickers/{packId:guid}/content/{id:guid}` | [StickerController.GetSticker:L331](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Sticker/StickerController.cs#L331) | sphere.stickers_surveys |
| PATCH | `/api/stickers/{packId:guid}/content/{id:guid}` | [StickerController.UpdateSticker:L364](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Sticker/StickerController.cs#L364) | sphere.stickers_surveys |
| PATCH | `/api/stickers/{packId:guid}/content/batch/rendering-settings` | [StickerController.BatchUpdateStickerRenderingSettings:L408](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Sticker/StickerController.cs#L408) | sphere.stickers_surveys |
| DELETE | `/api/stickers/{packId:guid}/content/{id:guid}` | [StickerController.DeleteSticker:L455](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Sticker/StickerController.cs#L455) | sphere.stickers_surveys |
| POST | `/api/stickers/{packId:guid}/content` | [StickerController.CreateSticker:L480](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Sticker/StickerController.cs#L480) | sphere.stickers_surveys |
| GET | `/api/stickers/{packId:guid}/own` | [StickerController.GetStickerPackOwnership:L527](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Sticker/StickerController.cs#L527) | sphere.stickers_surveys |
| POST | `/api/stickers/{packId:guid}/own` | [StickerController.AcquireStickerPack:L542](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Sticker/StickerController.cs#L542) | sphere.stickers_surveys |
| DELETE | `/api/stickers/{packId:guid}/own` | [StickerController.ReleaseStickerPack:L571](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Sticker/StickerController.cs#L571) | sphere.stickers_surveys |
| PATCH | `/api/stickers/me/order` | [StickerController.ReorderOwnedStickerPacks:L602](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Sticker/StickerController.cs#L602) | sphere.stickers_surveys |
| PATCH | `/api/stickers/{packId:guid}/content/order` | [StickerController.ReorderStickers:L631](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Sticker/StickerController.cs#L631) | sphere.stickers_surveys |
| GET | `/api/surveys/{id:guid}` | [SurveyController.GetSurvey:L29](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Survey/SurveyController.cs#L29) | sphere.stickers_surveys |
| POST | `/api/surveys/{id:guid}/answer` | [SurveyController.AnswerSurvey:L61](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Survey/SurveyController.cs#L61) | sphere.stickers_surveys |
| DELETE | `/api/surveys/{id:guid}/answer` | [SurveyController.DeleteSurveyAnswer:L114](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Survey/SurveyController.cs#L114) | sphere.stickers_surveys |
| GET | `/api/surveys/{id:guid}/feedback` | [SurveyController.GetSurveyFeedback:L164](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Survey/SurveyController.cs#L164) | sphere.stickers_surveys |
| GET | `/api/surveys/me` | [SurveyController.ListSurveys:L216](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Survey/SurveyController.cs#L216) | sphere.stickers_surveys |
| POST | `/api/surveys` | [SurveyController.CreateSurvey:L326](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Survey/SurveyController.cs#L326) | sphere.stickers_surveys |
| PATCH | `/api/surveys/{id:guid}` | [SurveyController.UpdateSurvey:L412](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Survey/SurveyController.cs#L412) | sphere.stickers_surveys |
| POST | `/api/surveys/{id:guid}/publish` | [SurveyController.PublishSurvey:L587](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Survey/SurveyController.cs#L587) | sphere.stickers_surveys |
| POST | `/api/surveys/{id:guid}/archive` | [SurveyController.ArchiveSurvey:L646](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Survey/SurveyController.cs#L646) | sphere.stickers_surveys |
| POST | `/api/surveys/{id:guid}/clone` | [SurveyController.CloneSurvey:L701](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Survey/SurveyController.cs#L701) | sphere.stickers_surveys |
| DELETE | `/api/surveys/{id:guid}` | [SurveyController.DeleteSurvey:L760](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Survey/SurveyController.cs#L760) | sphere.stickers_surveys |
| POST | `/api/surveys/{id:guid}/subscribe` | [SurveyController.SubscribeToSurvey:L842](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Survey/SurveyController.cs#L842) | sphere.stickers_surveys |
| POST | `/api/surveys/{id:guid}/unsubscribe` | [SurveyController.UnsubscribeFromSurvey:L873](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Survey/SurveyController.cs#L873) | sphere.stickers_surveys |
| GET | `/api/surveys/{id:guid}/subscription` | [SurveyController.GetSurveySubscription:L893](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Survey/SurveyController.cs#L893) | sphere.stickers_surveys |
| GET | `/api/timeline` | [ActivityController.ListEvents:L28](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Timeline/TimelineController.cs#L28) | sphere.timeline |
| GET | `/api/timeline/discovery/profile` | [TimelineDiscoveryController.GetProfile:L18](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Timeline/TimelineDiscoveryController.cs#L18) | sphere.timeline |
| POST | `/api/timeline/discovery/uninterested` | [TimelineDiscoveryController.MarkUninterested:L27](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Timeline/TimelineDiscoveryController.cs#L27) | sphere.timeline |
| DELETE | `/api/timeline/discovery/uninterested` | [TimelineDiscoveryController.RemoveUninterested:L49](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Timeline/TimelineDiscoveryController.cs#L49) | sphere.timeline |
| POST | `/api/timeline/discovery/feedback` | [TimelineDiscoveryController.LeaveFeedback:L71](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Timeline/TimelineDiscoveryController.cs#L71) | sphere.timeline |
| PUT | `/api/timeline/discovery/weights` | [TimelineDiscoveryController.AdjustWeight:L101](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Timeline/TimelineDiscoveryController.cs#L101) | sphere.timeline |
| POST | `/api/timeline/discovery/reset` | [TimelineDiscoveryController.ResetInterests:L125](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Timeline/TimelineDiscoveryController.cs#L125) | sphere.timeline |
| POST | `/api/translate` | [TranslationController.Translate:L26](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Translation/TranslationController.cs#L26) | sphere.content_utilities |

### Wallet

| Method | Service route candidate | Action and pinned source | Family |
|---|---|---|---|
| GET | `/api/merchants/{merchant}` | [MerchantController.GetMerchant:L33](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/MerchantController.cs#L33) | wallet.merchants |
| GET | `/api/merchants/{merchant}/settlements` | [MerchantController.ListSettlements:L60](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/MerchantController.cs#L60) | wallet.merchants |
| GET | `/api/merchants/{merchant}/orders` | [MerchantController.ListOrders:L111](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/MerchantController.cs#L111) | wallet.merchants |
| PATCH | `/api/merchants/{merchant}/wallet` | [MerchantController.UpdateWallet:L182](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/MerchantController.cs#L182) | wallet.merchants |
| GET | `/api/merchants/{merchant}/settlements/pending` | [MerchantController.GetPendingSummary:L254](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/MerchantController.cs#L254) | wallet.merchants |
| POST | `/api/merchants/{merchant}/settlements/settle` | [MerchantController.ManualSettle:L286](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/MerchantController.cs#L286) | wallet.merchants |
| GET | `/api/merchants/{merchant}/stats/overview` | [MerchantController.GetOverviewStats:L322](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/MerchantController.cs#L322) | wallet.merchants |
| GET | `/api/merchants/{merchant}/stats/incoming` | [MerchantController.GetPeriodIncoming:L352](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/MerchantController.cs#L352) | wallet.merchants |
| GET | `/api/merchants/{merchant}/stats/daily` | [MerchantController.GetDailyIncoming:L407](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/MerchantController.cs#L407) | wallet.merchants |
| POST | `/api/orders` | [OrderController.CreateOrder:L109](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/OrderController.cs#L109) | wallet.orders_payments |
| GET | `/api/orders/mine` | [OrderController.ListMyOrders:L311](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/OrderController.cs#L311) | wallet.orders_payments |
| GET | `/api/orders/mine/{id:guid}` | [OrderController.GetMyOrder:L359](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/OrderController.cs#L359) | wallet.orders_payments |
| GET | `/api/orders/{id:guid}` | [OrderController.GetOrderById:L387](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/OrderController.cs#L387) | wallet.orders_payments |
| POST | `/api/orders/{id:guid}/pay` | [OrderController.PayOrder:L441](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/OrderController.cs#L441) | wallet.orders_payments |
| PATCH | `/api/orders/{id:guid}/status` | [OrderController.UpdateOrderStatus:L479](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/OrderController.cs#L479) | wallet.orders_payments |
| POST | `/api/orders/metrics` | [OrderController.GetAppOrderMetrics:L531](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/OrderController.cs#L531) | wallet.orders_payments |
| POST | `/api/orders/payouts` | [OrderController.CreateAppPayout:L579](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/OrderController.cs#L579) | wallet.orders_payments |
| GET | `/api/admin/payments/transactions` | [PaymentAdminController.ListTransactions:L35](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/PaymentAdminController.cs#L35) | wallet.orders_payments |
| GET | `/api/admin/payments/transactions/{id:guid}` | [PaymentAdminController.GetTransaction:L89](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/PaymentAdminController.cs#L89) | wallet.orders_payments |
| GET | `/api/admin/payments/orders` | [PaymentAdminController.ListOrders:L106](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/PaymentAdminController.cs#L106) | wallet.orders_payments |
| GET | `/api/admin/payments/orders/{id:guid}` | [PaymentAdminController.GetOrder:L165](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/PaymentAdminController.cs#L165) | wallet.orders_payments |
| GET | `/api/admin/payments/inbound-orders` | [PaymentAdminController.ListInboundOrders:L182](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/PaymentAdminController.cs#L182) | wallet.orders_payments |
| GET | `/api/admin/payments/inbound-orders/{id:guid}` | [PaymentAdminController.GetInboundOrder:L235](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/PaymentAdminController.cs#L235) | wallet.orders_payments |
| POST | `/api/admin/payments/balance` | [PaymentAdminController.ModifyWalletBalance:L250](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/PaymentAdminController.cs#L250) | wallet.orders_payments |
| GET | `/api/admin/subscriptions` | [SubscriptionAdminController.ListSubscriptions:L51](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionAdminController.cs#L51) | wallet.subscriptions_gifts |
| GET | `/api/admin/subscriptions/catalog` | [SubscriptionAdminController.ListCatalog:L94](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionAdminController.cs#L94) | wallet.subscriptions_gifts |
| GET | `/api/admin/subscriptions/catalog/{identifier}` | [SubscriptionAdminController.GetCatalogItem:L101](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionAdminController.cs#L101) | wallet.subscriptions_gifts |
| POST | `/api/admin/subscriptions/catalog` | [SubscriptionAdminController.UpsertCatalogItem:L112](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionAdminController.cs#L112) | wallet.subscriptions_gifts |
| DELETE | `/api/admin/subscriptions/catalog/{identifier}` | [SubscriptionAdminController.DeleteCatalogItem:L145](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionAdminController.cs#L145) | wallet.subscriptions_gifts |
| POST | `/api/admin/subscriptions/maintenance/update-expired` | [SubscriptionAdminController.UpdateExpiredSubscriptions:L156](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionAdminController.cs#L156) | wallet.subscriptions_gifts |
| POST | `/api/admin/subscriptions/maintenance/activate-pending` | [SubscriptionAdminController.ActivatePendingSubscriptions:L169](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionAdminController.cs#L169) | wallet.subscriptions_gifts |
| POST | `/api/admin/subscriptions/maintenance/cancel-unavailable-in-app-wallet` | [SubscriptionAdminController.CancelUnavailableInAppWalletSubscriptions:L183](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionAdminController.cs#L183) | wallet.subscriptions_gifts |
| GET | `/api/subscriptions/catalog` | [SubscriptionController.ListCatalog:L89](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionController.cs#L89) | wallet.subscriptions_gifts |
| GET | `/api/subscriptions/groups` | [SubscriptionController.ListCatalogGroups:L96](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionController.cs#L96) | wallet.subscriptions_gifts |
| GET | `/api/subscriptions` | [SubscriptionController.ListSubscriptions:L103](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionController.cs#L103) | wallet.subscriptions_gifts |
| GET | `/api/subscriptions/billing` | [SubscriptionController.ListBillingRecords:L130](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionController.cs#L130) | wallet.subscriptions_gifts |
| GET | `/api/subscriptions/pending-activations` | [SubscriptionController.ListPendingActivations:L172](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionController.cs#L172) | wallet.subscriptions_gifts |
| GET | `/api/subscriptions/fuzzy/{prefix}` | [SubscriptionController.GetSubscriptionFuzzy:L197](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionController.cs#L197) | wallet.subscriptions_gifts |
| GET | `/api/subscriptions/groups/{groupIdentifier}` | [SubscriptionController.GetSubscriptionGroup:L204](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionController.cs#L204) | wallet.subscriptions_gifts |
| POST | `/api/subscriptions/groups/{groupIdentifier}/activate` | [SubscriptionController.ActivateSubscriptionInGroup:L253](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionController.cs#L253) | wallet.subscriptions_gifts |
| GET | `/api/subscriptions/groups/{groupIdentifier}/active` | [SubscriptionController.GetActiveSubscriptionInGroup:L295](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionController.cs#L295) | wallet.subscriptions_gifts |
| GET | `/api/subscriptions/{identifier}` | [SubscriptionController.GetSubscription:L326](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionController.cs#L326) | wallet.subscriptions_gifts |
| POST | `/api/subscriptions` | [SubscriptionController.CreateSubscription:L449](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionController.cs#L449) | wallet.subscriptions_gifts |
| POST | `/api/subscriptions/{identifier}/cancel` | [SubscriptionController.CancelSubscription:L502](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionController.cs#L502) | wallet.subscriptions_gifts |
| POST | `/api/subscriptions/{identifier}/order` | [SubscriptionController.CreateSubscriptionOrder:L533](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionController.cs#L533) | wallet.subscriptions_gifts |
| POST | `/api/subscriptions/order/restore/afdian` | [SubscriptionController.RestorePurchaseFromAfdian:L587](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionController.cs#L587) | wallet.subscriptions_gifts |
| POST | `/api/subscriptions/order/restore/paddle` | [SubscriptionController.RestorePurchaseFromPaddle:L604](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionController.cs#L604) | wallet.subscriptions_gifts |
| POST | `/api/subscriptions/order/restore/apple` | [SubscriptionController.RestorePurchaseFromAppleStore:L621](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionController.cs#L621) | wallet.subscriptions_gifts |
| POST | `/api/subscriptions/{identifier}/checkout/paddle` | [SubscriptionController.CreatePaddleCheckout:L655](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionController.cs#L655) | wallet.subscriptions_gifts |
| POST | `/api/subscriptions/{identifier}/checkout/afdian` | [SubscriptionController.CreateAfdianCheckout:L708](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionController.cs#L708) | wallet.subscriptions_gifts |
| POST | `/api/subscriptions/order/handle/afdian` | [SubscriptionController.AfdianWebhook:L748](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionController.cs#L748) | wallet.subscriptions_gifts |
| POST | `/api/subscriptions/order/handle/paddle` | [SubscriptionController.PaddleWebhook:L759](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionController.cs#L759) | wallet.subscriptions_gifts |
| POST | `/api/subscriptions/order/handle/apple` | [SubscriptionController.AppleStoreWebhook:L770](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionController.cs#L770) | wallet.subscriptions_gifts |
| GET | `/api/subscriptions/gifts/sent` | [SubscriptionGiftController.ListSentGifts:L25](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionGiftController.cs#L25) | wallet.subscriptions_gifts |
| GET | `/api/subscriptions/gifts/received` | [SubscriptionGiftController.ListReceivedGifts:L51](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionGiftController.cs#L51) | wallet.subscriptions_gifts |
| GET | `/api/subscriptions/gifts/{giftId}` | [SubscriptionGiftController.GetGift:L77](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionGiftController.cs#L77) | wallet.subscriptions_gifts |
| GET | `/api/subscriptions/gifts/check/{giftCode}` | [SubscriptionGiftController.CheckGiftCode:L101](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionGiftController.cs#L101) | wallet.subscriptions_gifts |
| POST | `/api/subscriptions/gifts/purchase` | [SubscriptionGiftController.PurchaseGift:L172](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionGiftController.cs#L172) | wallet.subscriptions_gifts |
| POST | `/api/subscriptions/gifts/redeem` | [SubscriptionGiftController.RedeemGift:L222](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionGiftController.cs#L222) | wallet.subscriptions_gifts |
| POST | `/api/subscriptions/gifts/{giftId}/send` | [SubscriptionGiftController.SendGift:L255](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionGiftController.cs#L255) | wallet.subscriptions_gifts |
| POST | `/api/subscriptions/gifts/{giftId}/cancel` | [SubscriptionGiftController.CancelGift:L277](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionGiftController.cs#L277) | wallet.subscriptions_gifts |
| POST | `/api/subscriptions/gifts/{giftId}/order` | [SubscriptionGiftController.CreateGiftOrder:L299](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionGiftController.cs#L299) | wallet.subscriptions_gifts |
| POST | `/api/wallets` | [WalletController.CreateWallet:L45](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletController.cs#L45) | wallet.ledger |
| GET | `/api/wallets` | [WalletController.GetWallet:L89](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletController.cs#L89) | wallet.ledger |
| GET | `/api/wallets/all` | [WalletController.GetWallets:L101](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletController.cs#L101) | wallet.ledger |
| GET | `/api/wallets/{id:guid}` | [WalletController.GetWalletById:L126](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletController.cs#L126) | wallet.ledger |
| GET | `/api/wallets/stats` | [WalletController.GetWalletStats:L164](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletController.cs#L164) | wallet.ledger |
| GET | `/api/wallets/transactions` | [WalletController.GetTransactions:L281](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletController.cs#L281) | wallet.ledger |
| GET | `/api/wallets/transactions/{id:guid}` | [WalletController.GetTransactionById:L367](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletController.cs#L367) | wallet.ledger |
| GET | `/api/wallets/orders` | [WalletController.GetOrders:L429](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletController.cs#L429) | wallet.ledger |
| GET | `/api/wallets/exchange` | [WalletController.GetCurrencyExchanges:L627](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletController.cs#L627) | wallet.ledger |
| POST | `/api/wallets/exchange` | [WalletController.ExchangeCurrency:L656](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletController.cs#L656) | wallet.ledger |
| POST | `/api/wallets/balance` | [WalletController.ModifyWalletBalance:L833](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletController.cs#L833) | wallet.ledger |
| POST | `/api/wallets/transfer` | [WalletController.Transfer:L868](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletController.cs#L868) | wallet.ledger |
| POST | `/api/wallets/transfer/requests` | [WalletController.CreateTransferRequest:L976](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletController.cs#L976) | wallet.ledger |
| GET | `/api/wallets/transfer/requests/{id:guid}` | [WalletController.GetTransferRequest:L1031](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletController.cs#L1031) | wallet.ledger |
| POST | `/api/wallets/{id:guid}/default` | [WalletController.SetDefaultWallet:L1043](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletController.cs#L1043) | wallet.ledger |
| POST | `/api/wallets/{id:guid}/public-id/enable` | [WalletController.EnablePublicId:L1059](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletController.cs#L1059) | wallet.ledger |
| POST | `/api/wallets/{id:guid}/public-id/disable` | [WalletController.DisablePublicId:L1085](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletController.cs#L1085) | wallet.ledger |
| POST | `/api/wallets/funds` | [WalletController.CreateFund:L1131](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletController.cs#L1131) | wallet.ledger |
| GET | `/api/wallets/funds` | [WalletController.GetFunds:L1188](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletController.cs#L1188) | wallet.ledger |
| GET | `/api/wallets/funds/{id:guid}` | [WalletController.GetFund:L1222](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletController.cs#L1222) | wallet.ledger |
| POST | `/api/wallets/funds/{id:guid}/receive` | [WalletController.ReceiveFund:L1251](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletController.cs#L1251) | wallet.ledger |
| GET | `/api/wallets/overview` | [WalletController.GetWalletOverview:L1275](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletController.cs#L1275) | wallet.ledger |
| POST | `/api/wallets/transactions/{id:guid}/confirm` | [WalletController.ConfirmTransaction:L1303](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletController.cs#L1303) | wallet.ledger |
| POST | `/api/wallets/transactions/{id:guid}/reject` | [WalletController.RejectTransaction:L1322](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletController.cs#L1322) | wallet.ledger |
| GET | `/api/wallets/transactions/pending` | [WalletController.GetPendingTransactions:L1341](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletController.cs#L1341) | wallet.ledger |
| POST | `/api/wallets/funds/{id:guid}/contribute` | [WalletController.ContributeToFund:L1403](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletController.cs#L1403) | wallet.ledger |
| GET | `/api/wallets/funds/{id:guid}/contributors` | [WalletController.GetFundContributors:L1428](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletController.cs#L1428) | wallet.ledger |
| GET | `/api/admin/wallet-products/golds-resupply-pack` | [WalletProductAdminController.GetGoldsResupplyPack:L27](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletProductAdminController.cs#L27) | wallet.product_catalog |
| POST | `/api/admin/wallet-products/orders/{orderId:guid}/apply` | [WalletProductAdminController.ApplyPaidWalletProductOrder:L47](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletProductAdminController.cs#L47) | wallet.product_catalog |
| GET | `/api/wallet-products/catalog` | [WalletProductController.GetCatalog:L50](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletProductController.cs#L50) | wallet.product_catalog |
| POST | `/api/wallet-products/golds-resupply-pack/checkout/paddle` | [WalletProductController.CreatePaddleCheckout:L69](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletProductController.cs#L69) | wallet.product_catalog |
| POST | `/api/wallet-products/golds-resupply-pack/checkout/afdian` | [WalletProductController.CreateAfdianCheckout:L108](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletProductController.cs#L108) | wallet.product_catalog |
| GET | `/api/admin/stats` | [WalletStatsAdminController.GetStats:L32](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletStatsAdminController.cs#L32) | wallet.reporting |

## RPC implementation method index

Names below are implementation methods only. No protobuf definitions or generated bindings were inspected. Wire compatibility remains blocked.

- [DysonNetwork.Develop/Identity/CustomAppServiceGrpc.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/CustomAppServiceGrpc.cs): GetCustomApp, CheckCustomAppSecret, GetAppDeveloper, GetAppProduct, GetBoardWidget, ValidateBoardWidgetPayload
- [DysonNetwork.Messager/Rewind/MessagerRewindServiceGrpc.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Rewind/MessagerRewindServiceGrpc.cs): GetRewindEvent
- [DysonNetwork.Passport/Account/AccountServiceGrpc.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountServiceGrpc.cs): GetAccount, GetBotAccount, GetAccountBatch, GetBotAccountBatch, GetAccountStatus, GetAccountStatusBatch, LookupAccountBatch, SearchAccount, ListAccounts, ListFriends, ListBlocked, ListMuted, ListCloseFriends, GetProfile, UpdateProfile, UpdateBoardItemPayload, ListBadges, GetRelationship, HasRelationship, GrantBadge, GetBadge, UpdateBadge, GetNotableDays, GetUserCalendarEvents, GetCountdownEvents
- [DysonNetwork.Passport/Account/PresenceServiceGrpc.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/PresenceServiceGrpc.cs): ListFriendsActivities
- [DysonNetwork.Passport/Credit/SocialCreditServiceGrpc.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Credit/SocialCreditServiceGrpc.cs): AddRecord, GetSocialCredit
- [DysonNetwork.Passport/DomainTrust/DomainTrustServiceGrpc.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/DomainTrust/DomainTrustServiceGrpc.cs): ValidateUrl, IsDomainBlocked
- [DysonNetwork.Passport/Leveling/ExperienceServiceGrpc.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Leveling/ExperienceServiceGrpc.cs): AddRecord
- [DysonNetwork.Passport/Nfc/NfcServiceGrpc.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Nfc/NfcServiceGrpc.cs): ValidateNfcToken, ResolveNfcTag
- [DysonNetwork.Passport/Realm/RealmServiceGrpc.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmServiceGrpc.cs): GetRealm, GetRealmBatch, GetUserRealms, GetPublicRealms, SearchRealms, SendInviteNotify, IsMemberWithRole, LoadMemberAccount, LoadMemberAccounts, HasPermission
- [DysonNetwork.Shared.Tests/Capabilities/CapabilityGrpcServiceTests.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Shared.Tests/Capabilities/CapabilityGrpcServiceTests.cs): filename_is_test_not_service; not inspected
- [DysonNetwork.Sphere/Autocompletion/AutocompletionServiceGrpc.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Autocompletion/AutocompletionServiceGrpc.cs): Autocomplete
- [DysonNetwork.Sphere/Post/PostServiceGrpc.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostServiceGrpc.cs): GetPost, GetPostBatch, SearchPosts, ListPosts
- [DysonNetwork.Sphere/Publisher/PublisherRatingServiceGrpc.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherRatingServiceGrpc.cs): AddRecord, GetRating
- [DysonNetwork.Sphere/Publisher/PublisherServiceGrpc.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherServiceGrpc.cs): GetPublisher, GetPublisherBatch, ListPublishers, ListPublisherMembers, SetPublisherFeatureFlag, HasPublisherFeature, IsPublisherMember, RenamePublisher
- [DysonNetwork.Sphere/Reader/WebReaderGrpcService.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Reader/WebReaderGrpcService.cs): ScrapeArticle, GetLinkPreview, InvalidateLinkPreviewCache
- [DysonNetwork.Sphere/Rewind/SphereRewindServiceGrpc.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Rewind/SphereRewindServiceGrpc.cs): GetRewindEvent
- [DysonNetwork.Sphere/Sticker/StickerServiceGrpc.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Sticker/StickerServiceGrpc.cs): GetStickerByIdentifier
- [DysonNetwork.Sphere/Survey/SurveyServiceGrpc.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Survey/SurveyServiceGrpc.cs): GetSurvey, GetSurveyBatch, ListSurveys, GetSurveyAnswer, GetSurveyStats, GetSurveyQuestionStats
- [DysonNetwork.Wallet/Payment/MerchantServiceGrpc.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/MerchantServiceGrpc.cs): UpsertMerchant, CreateMerchantSettlement, GetPendingSettlements, SettleMerchant
- [DysonNetwork.Wallet/Payment/PaymentServiceGrpc.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/PaymentServiceGrpc.cs): CreateOrder, CreateTransactionWithAccount, CreateTransaction, CancelOrder, RefundOrder, Transfer, GetWalletFund, RegisterAppSubscriptionDefinition
- [DysonNetwork.Wallet/Payment/SubscriptionCatalogServiceGrpc.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionCatalogServiceGrpc.cs): UpsertDefinition, ListDefinitions
- [DysonNetwork.Wallet/Payment/SubscriptionServiceGrpc.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/SubscriptionServiceGrpc.cs): GetSubscription, GetPerkSubscription, GetPerkSubscriptions, CreateSubscription, CancelSubscription
- [DysonNetwork.Wallet/Payment/WalletServiceGrpc.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletServiceGrpc.cs): GetWallet, CheckWalletExists, CreateWallet, GetOrCreateWalletPocket

## Scheduler registration index

Schedules are literal source values; timezone, scheduler persistence, clustering, misfires and restarts are unverified.

- Messager / AppDatabaseRecyclingJob: `0 0 0 * * ?`.
- Messager / ChatVoiceCleanupJob: `start/repeat configuration; inspect source`.
- Messager / PlaceholderExpirationJob: `0 * * * * ?`.
- Passport / AppDatabaseRecyclingJob: `0 0 0 * * ?`.
- Passport / NightOwlReminderJob: `0 0 * * * ?`.
- Passport / DomainValidationMetricFlushJob: `every 1 minutes`.
- Passport / SocialCreditValidationJob: `every 60 minutes`.
- Passport / RealmTenureLevelingJob: `0 0 1 * * ?`.
- Passport / PresenceUpdateJob: `every 1 minutes`.
- Passport / PresenceUpdateJob: `every 5 minutes`.
- Passport / PresenceUpdateJob: `every 10 minutes`.
- Passport / PresenceArtworkCleanupJob: `start/repeat configuration; inspect source`.
- Passport / RelationshipExpiryJob: `every 5 minutes`.
- Sphere / PostIndexBackfillJob: `start/repeat configuration; inspect source` (start now).
- Sphere / AppDatabaseRecyclingJob: `0 0 0 * * ?`.
- Sphere / PostViewFlushJob: `every 1 minutes`.
- Sphere / PostInterestFlushJob: `every 1 minutes`.
- Sphere / StickerPopularityFlushJob: `every 1 minutes`.
- Sphere / PublisherSettlementJob: `0 0 0 * * ?`.
- Sphere / DeliveryRetryJob: `every 1 minutes`.
- Sphere / DeliveryCleanupJob: `0 0 3 * * ?`.
- Sphere / DeliveryHealthCheckJob: `0 */5 * * * ?`.
- Sphere / FediverseActorCleanupJob: `0 0 4 * * ?`.
- Sphere / LiveStreamIngressCleanupJob: `every 5 minutes`.
- Sphere / PublisherFollowRequestCleanupJob: `0 0 6 * * ?`.
- Sphere / OutboxBackfillJob: `0 0 */6 * * ?`.
- Sphere / PostSponsorAuctionJob: `0 0 * * * ?`.
- Wallet / AppDatabaseRecyclingJob: `0 0 0 * * ?`.
- Wallet / SubscriptionRenewalJob: `every 30 minutes`.
- Wallet / GiftCleanupJob: `every 1 hours`.
- Wallet / FundExpirationJob: `every 1 hours`.
- Wallet / TransactionExpirationJob: `every 15 minutes`.
- Wallet / FundRaisingDeadlineJob: `every 15 minutes`.
- Wallet / AppSettlementJob: `0 0 0 * * ?`.

## Completion criteria

- A completed operation has an exact implementation path, permitted source/contract evidence and reproducible passing tests for its success and failure cases; a static route record is never counted as completion.
- A completed family includes repository/domain behavior, authority, serialization, errors, persistence, jobs/events/storage/provider interactions and restart/recovery appropriate to that family.
- Any synthetic-only candidate remains disabled or fail-closed by default for production use, with explicit fake principal/status/schema configuration and no in-memory fallback presented as PostgreSQL.
- No generated descriptor or binding, protocol schema, shared implementation or missing-license data corpus is used as a shortcut for unknown contracts.
- No actual payment, credential issuance, production write, external notification, live federation or provider action is necessary for acceptance fixtures.
- Unit, race, contract, source-integrity, actual-PostgreSQL and process-smoke evidence is recorded distinctly. Skipped, failed and never-run checks are not reported as passing.
- Full completion requires a reconciled runtime operation ledger, all required release gates, existing-Go service/client integration, exact-commit CI and a synthetic migration/rollback rehearsal.
