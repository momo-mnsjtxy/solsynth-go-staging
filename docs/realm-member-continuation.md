# Realm labels and scoped member progression

This independently owned continuation starts at `106c35a0e860add9d58d40737ff27cc4743e5cc1`. It adds the retained label-management flow, labels in canonical member reads, and an explicitly configured daily-tenure maintenance flow with a scoped progression/history projection. The existing realm boost checkout, money, receipts and live entitlement are unchanged; the original experience route combines the two known ledgers when this module is enabled.

## Contract evidence and limits

All evidence was already retained with its exact authored bytes and root license. `docs/realm-member-source-evidence.json` records the source paths, pinned revisions, Git blobs, SHA-256 digests and relevant ranges. Existing source-integrity guards and `realmboosts.TestPinnedAuthoredEvidence` freeze these same sources.

- AGPL DysonNetwork `4ee2d0cebb9da8284a92483fee6dce795bf6947c`: the complete retained `RealmController.cs`, request fields at 109–135, label endpoints at 438–569, member projection/filter at 289–375, and XP-history endpoint at 673–704. Labels require moderator rank, current boost level at least one, `realms.labels.manage` for CRUD and `realms.members.manage` for assignment. Creation alone checks the label cap. Deletion clears every assigned label. Assignment targets the member's **account ID**, requires active membership, and rejects labels from another realm.
- The complete authored `RealmService.cs`, especially 59–76 and 122–140, provides active membership, numerical minimum-role checks, count, and label loading. Imported Shared model or policy definitions were not used.
- The complete authored `RealmExperienceService.cs`, lines 9–13, 28–72 and 83–114, provides 5 daily tenure XP for active members whose UTC join date precedes today, reason `realm.tenure.daily`, ISO date deduplication, and floor-applied positive perk multipliers 1.5/2/2.5. Thus daily awards are exactly 5/7/10/12. Constants for chat and post XP are present, but retained evidence does not establish their event trigger/cooldown contracts; this module awards neither.
- AGPL Solian `ffd0838e5f04cb40bea311a39118ebeedad210c1`, retained `realm_detail.dart`, supplies the authored label editor, assignment caller using account ID, `label_id` member filtering, and text-icon hint. The experience card reads imported leveling properties without defining their formula. No level thresholds have been reconstructed from it.

No protocol/generated bindings, unlicensed repositories, canceled SoSYS/Socommon material or unlicensed Suki were inspected. No frontend was implemented. Exact legacy member levels, XP from other sources, imported models, tenure cron scheduling, profile customization, or complete legacy member ordering/status/search parity are not claimed.

## Canonical HTTP flow

Both `/api` and `/passport` accept:

- `GET /realms/{ref}/labels`: name/ID-ordered labels, with canonical public/private visibility.
- `POST /realms/{ref}/labels`: full label fields, optional `Idempotency-Key` of at most 128 bytes. The same actor/key/request returns the original current label. A changed request/realm or deleted label conflicts and cannot resurrect it.
- `PATCH /realms/{ref}/labels/{id}`: complete authored request shape. Name is required; omitted nullable description/color/icon clear those values as in the source controller.
- `DELETE /realms/{ref}/labels/{id}`: delete and assignment cleanup in one transaction; 204 on success.
- `PATCH /realms/{ref}/members/{account}/label`: `{"label_id":"uuid"}` or explicit `{"label_id":null}`. Missing `label_id` is rejected instead of accidentally clearing an assignment.
- `GET /realms/{ref}/members?label_id={uuid}&offset=0&take=20`: bounded label filter with `X-Total`. Ordinary unfiltered member listing remains with the existing social handler. Existing list, `members/me`, and membership-change responses include the same canonical label and optional `progression` object when the feature is enabled.

Name/description/color/icon retain the authored 1024/4096/64/256 UTF-16-unit limits; whitespace-only names and NUL are rejected. Icon is stored display text, never interpreted as a file ID or fetched URL. No media entitlement, usage grant or provider request is created. Unknown/duplicate/case-aliased JSON keys, extra query keys, spoofed identity headers, invalid supplied tokens, and invalid pagination are rejected.

Owned extensions are explicit:

- `GET /realms/{ref}/members/{account}/progression` returns `recorded_experience`, `experience_complete:false`, source names and optional configured level information.
- `GET /realms/{ref}/members/{account}/progression/history?take=50` returns the supported ledgers together with `X-Realm-Experience-Scope: realm.boost.purchase,realm.tenure.daily` and `X-Realm-Experience-Complete: false`.
- `POST /realms/{ref}/progression/tenure`, body `{"take":100,"after":"optional-account-uuid"}`, is an opt-in bounded owner maintenance operation. Omit `after` for the first page; follow a returned `next` cursor. At most 200 members are examined per transaction. It awards only the current UTC day, with no backfill, date override, caller delta, arbitrary reason or externally trusted event. It requires current owner membership, `realms.members.manage`, and canonical `can_manage_members`. It is neither a scheduler nor an unauthenticated award endpoint.

The original experience endpoint keeps its exact path and array shape. With realm members enabled, its headers identify the two known sources and incomplete coverage; disabling this module retains boost-only scope. Supported XP remains recorded after boosts expire. `recorded_experience` is never presented as total realm XP. Level calculation uses only the configured supported ledgers; `level_policy` is `owned.configured.scoped`. Empty thresholds give null level, floor, next threshold and policy. Configured levels are zero-based indexes into cumulative floors; the last configured level has no next threshold. This is an owned opt-in policy, not upstream equivalence.

## Current authority, transactions and durable state

No actor or realm membership is trusted from a request field. Each mutation joins canonical authority to its database transaction. Accounts lock in sorted order before realm rows; observations and authorization are rechecked after waits and again before commit. Active disable-account punishments, unknown/deleted role definitions, stale role generations, forged owner ranks and departed memberships fail closed. Assignment also respects the current canonical member-management permission, strengthening the authored numeric-role guard.

Label gates use the existing paid boost ledger and **the same explicitly configured cap array**, never stale stored `boost_level`. A realm lock serializes create/count races. Eligibility and reduced caps are checked again before commit. Expiry blocks create/update/delete/assignment, including create replays, but preserves label reads and assigned labels, matching the source's distinction between write eligibility and existing content.

Assignments reference both the canonical membership and same-realm label. Their join timestamp binds each assignment to its membership incarnation: leave/reinvite/rejoin hides stale labels. Deleting a label cascades to assignments atomically. Create requests, mutations and audits share the transaction. An audit failure cannot consume a replay key or leave a label/assignment/tenure award behind.

Daily tenure has a durable `(realm_id, account_id, UTC day)` uniqueness constraint and preserves historical records through membership changes. A new join today earns nothing today. After all planned account and realm locks, current membership and the current UTC day are re-evaluated. Perk selection uses the existing explicit canonical internal subscription map, with a distinct owner-maintenance authority boundary; boost checkout's payer-only boundary remains unchanged. Row/document identity, active non-test state, begun/unexpired times and enabled internal definitions are mandatory. Subscription/definition secondary locks use NOWAIT and request whole-transaction retry. Perks and date are checked again before commit; changes roll back the batch. No money is created or transferred by tenure.

The aggregate/history reads sum the canonical paid boost receipt ledger and the owned tenure ledger. They do not copy, mutate or re-award boost XP. Scoped progression/history requires a current active target member and canonical realm visibility; disabled-module boost-only history retains its existing behavior.

## Installation and standalone configuration

Apply `internal/realmmemberpg/schema.sql` explicitly after authority, social (including permissions) and realm boosts. Construction performs no I/O, migrations, account enrollment, quota grants or subscription creation. `application.Config.RealmMembers` requires canonical `RealmBoosts` to be configured. Readiness checks the new schema. Disabled label/progression routes return 503.

The standalone host requires both `--enable-realm-members` and `--realm-member-config=/absolute/path/policy.json`, plus the existing explicit boost flags/policy. No configuration file is discovered implicitly. The member JSON is:

```json
{"version":1,"level_thresholds":[],"enable_daily_tenure":false}
```

All three fields are mandatory. `level_thresholds` must be non-null; `[]` explicitly disables levels. A nonempty array starts at zero, is strictly increasing, contains at most 10,000 integer floors, and every floor is at most 1,000,000,000,000,000. `enable_daily_tenure` explicitly chooses whether the maintenance route may award source-defined tenure XP. Neither XP payouts nor multipliers are configurable here. Price/currency/boost cap/perk policy stays in the existing boost configuration.

The regular, non-symlink policy file is capped at 64 KiB. Unknown fields, duplicate decoded keys, case aliases, trailing JSON, nulls, fractional thresholds and partial activation are rejected. Normal no-`--serve` invocation validates configuration without connecting or opening a listener. Error messages do not print policy contents or account/credential values.

## Verification and handoff

The owned source/tests and disjoint shared host/social/CLI/registry wiring are delivered separately. `docs/realm-member-integration.json` lists mandatory cases and the restart marker. The grouping draft is unactivated: the coordinator must regenerate and independently review it against the final combined tree.

No-socket checks include all-package normal/race tests, build, vet, strict codec/config/policy tests, pinned source/license integrity, PostgreSQL-16 SQL/PLpgSQL grammar, static SQL bind arity, and migration wiring checks. PostgreSQL integration tests compile and skip without a marked disposable database; package `ok` is not a database pass. Actual locking, constraints, transactions, perk projections, full-host composition and restart must pass coordinator synthetic PostgreSQL CI before this slice is considered verified.

The restart test's before phase saves the original realm/member label assignment, current XP values, original synthetic proof, public verification key, policy and timestamps in private `realm-member-record.json`. The after phase reconstructs only adapters, uses the original proof, replays the original label request and daily award, and checks unchanged IDs/timestamps/XP/audit counts. It does not create a fixture, seed accounts, issue new credentials or replace observed resources. The disposable marker is not an artifact and must never be published.

## Combined authored experience route

When the realm-member module is enabled, the original `/api/realms/{ref}/members/{account}/experience` and `/passport` alias return the same experience array using both known canonical boost and tenure ledgers. Default take remains 50, capped at 200; ordering is creation time descending, then source kind and stable receipt ID. No boost rows are copied or counted twice. The target must currently be an active member and current realm visibility still applies. Headers identify these two sources and incomplete history. Disabled member configuration retains the earlier boost-only handler. Automatic tenure scheduling and unevidenced chat/post XP are separate unsupported flows.

Restart authority uses original synthetic credentials issued before target creation with a one-hour bounded lifetime, sufficient for the fifty-minute CI job. Only public verification material and original proof are stored in the private disposable marker. The after verifier advances by actual elapsed wall time and neither issues nor extends credentials.
