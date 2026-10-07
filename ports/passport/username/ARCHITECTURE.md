# Passport backend migration boundaries

This is a proposed decomposition of the remaining licensed C# Passport source,
not a report of implemented services. The machine-readable parity.json records
36 controller files, 255 HTTP verb attributes, and 59 database entity sets.
Controller attributes are not a complete runtime API specification.

## Keep Stargate authoritative

Authentication, sessions, OAuth/OIDC, account identity and the moved profile/social
graph already have a Go owner. Passport's current startup wires account and
action-log clients to Stargate. Do not introduce a second credential store or
change session/token behavior while migrating unrelated Passport features.

Retain the existing gateway paths and protobuf service identities. Architecture
can change behind adapters; externally visible paths, JSON fields and omission
rules, integer enum values, statuses, headers, permissions, events, and database
data must remain compatible.

## Proposed internal modules

Start with a modular Go service rather than creating many independent deployments.
Keep explicit interfaces for transactions, RPC clients, cache and events, then
split deployments only if required operationally.

| Module | Existing surface | Required boundaries |
|---|---|---|
| Profile extensions | board, badges, verification, leveling, credit views, friends overview | Stargate account projections; Passport-owned board/badge and experience data; Develop custom widgets |
| Activity/calendar | statuses, check-in, activities, calendars, subscriptions, notable days, fortune | relationship visibility; timezone/calendar rules; timers; push events |
| Community | realms, memberships, roles, labels, boosts, moderation | account/permissions RPCs; file references; payment events; experience accrual |
| Progression/activation | achievements, quests, grants, examinations, trials, activation | idempotent reward ledger; permission grants; question visibility; time limits |
| Proximity | meetings, location pins, nearby observations | expiration lifecycle; streaming; privacy visibility; account references |
| Support | tickets, messages, assignment, on-call | account permissions; contact lookup; notifications |
| Integrations | NFC, Apple PassKit, presence artwork, Steam/Last.fm, domain trust, IP geo | integration-specific credentials and key handling; external contracts; isolated failure handling |
| Purchases | affiliations, name-change cards, realm boosts | Wallet order events; durable idempotency and reconciliation; no money movement in migration tests |
| Rewind | personal/public yearly recaps | privacy mode; history queries; publication state |

The legacy controllers cross these boundaries. Shared read models and explicit
commands should replace hidden cross-module database coupling only after the
existing behavior has contract tests. A module is complete only when all its
HTTP/RPC handlers, persistence, events, scheduled jobs and recovery paths pass.

## Preserve asynchronous behavior

The source registers payment, realm-activity, action-log, location-pin and
websocket streams plus account-created handling. Consumer names, retry policy,
event schemas, deduplication and ordering are migration contracts.

Quartz jobs include recycling, night-owl notification, metric flushing, credit
validation, tenure leveling, staged presence polling, artwork cleanup and
relationship expiry. Hosted meeting/location-pin lifecycle tasks and streaming
subscriptions also need equivalents. A successful HTTP test alone does not
establish these features are migrated.

## First cohesive candidate: board

AccountBoardService is a bounded next candidate because its list/replace/update
operations have a small persistence boundary. Preserve sorted reads, replacement
transactions, app-owned payload retention, enabled/singleton widget validation,
canonical widget keys and cache invalidation after successful commit.

Historical planning note: this candidate subsequently became the bounded
transport-independent ports/passport/board package. Complete transport parity
remains blocked by missing eligible interface evidence. The unlicensed protocol
repository's definitions, schemas, descriptors and generated bindings are
excluded; do not request them or user exports. Earlier assumptions about obtaining
shared definitions are superseded:

- The pinned DysonNetwork gitlink for DysonNetwork.Shared is
  83c36cb92d667d3800f152665660c17886f71f7c
- The permitted parent repository's .gitmodules maps it to
  https://src.solsynth.dev/SoSYS/NeTo.git
- No public Solsynth/NeTo GitHub metadata was available (404); an organization
  repository-name search returned no NeTo result
- The Shared submodule's own license has not been verified. The parent
  repository license must not be assumed to license a separate submodule
- The licensed Solian client and DysonNetwork-owned call sites/migrations now
  support the local item projection and tested service boundary. Unresolved
  payload/auth/transport semantics require independently derived licensed
  contracts or separately licensed public documentation, not Shared exports

No SoSYS content was fetched to work around the existing access cancellation.
The board service has been read in licensed DysonNetwork; any eventual port is
source-derived and must never be described as clean-room.

## Verification and cutover gates

1. Freeze revisions and verify each source/dependency license
2. Inventory every endpoint, DTO, permission, event, job and persisted entity
3. Build synthetic fixtures for normal/empty/error/retry/duplicate/time-boundary cases
4. Compare old/new behavior using isolated test dependencies and databases
5. Validate schema/data migration with rollback/reconciliation and no real accounts
6. Run integration, race, serialization and cross-client compatibility tests
7. Only then prepare an explicitly authorized deployment/cutover plan

The current username utility passes local race tests, vet, and 100% statement
coverage. This does not establish differential C# equivalence, service parity,
Unicode comparison equivalence, RPC adapter behavior or production readiness.
