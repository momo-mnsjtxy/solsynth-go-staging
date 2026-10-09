# Account history reads

This AGPL-3.0-only implementation is grounded in the pinned, licensed DysonNetwork
and Solian authored files under `source/manifest.json`. It reads no excluded Shared
implementation, Socommon, generated protocol, unlicensed SDK, or cancelled source.
The retained license notices apply. No provider, Steam or night-owl execution is added.

## Source-backed contracts

- AccountController.GetTimeline and AccountEventService.GetTimeline merge existing
  account status and presence activity rows, descending by creation time. The
  authored Solian profile timeline explicitly checks `event_type == 0` for status
  and `event_type == 1` for activity. Both cleared statuses and ended/expired
  activities are history; deleted activity rows are excluded.
- AccountCurrentController.GetActionLogs is self-only, defaults to take 20/offset
  0, normalizes their lower bounds, returns `X-Total`, and projects action, metadata,
  nullable user agent, IP, location, session, and creation/update timestamps.
  ActionLogService's request-aware method demonstrates the request provenance of
  IP, user-agent and session fields. The owned audit sources did not capture those
  facts: this projection truthfully returns null rather than inventing them.
- GetSocialCredit, GetCreditHistory and AccountPublicController.GetSocialCredits
  establish the private/public scalar and self-only paginated ledger reads.
  SocialCreditService establishes base 100, including an empty ledger. Its call
  into the excluded model's GetEffectiveDelta is not inspected or assumed.
  Instead the complete authored SocialCreditValidationJob independently specifies
  the same profile-credit computation: expired-at-or-before-now contributes zero;
  a permanent record contributes delta; a zero-duration future record contributes
  delta; otherwise delta times max(0, 1 - elapsed seconds / total seconds).
- GetConnections is self-only with `account.connections`, and provider filtering.
  Solian's authored settings caller consumes id, provider, provided_identifier,
  meta, is_public and last_used_at. It does not turn auth sessions, OAuth grants or
  live WebSocket connections into provider links.

## Explicit owned architecture and boundaries

The composed host implements GET/HEAD `/api/accounts/{name}/timeline`,
`/api/accounts/me/actions`, `/api/accounts/me/credits`,
`/api/accounts/me/credits/history`, `/api/accounts/{name}/credits`, and
`/api/accounts/me/connections`, with `/passport` and `/stargate` gateway aliases.
The existing frontend is not changed. Existing lawful status/activity fields and
canonical mutation contracts remain grounded in the licensed authored EF snapshot
`ports/passport/board/source/DysonNetwork.Passport__Migrations__AppDatabaseModelSnapshot.cs`
and `ports/passport/activities/source/manifest.json`; no Shared implementation was
imported for this read subset.

Timeline is fed by the canonical `presencepg` and `activitypg` mutations, with no
second status/activity authority. Current account disabled/punishment state,
existing status-visibility preferences, invisibility, mutual friendship and both
blocking directions gate public reads. Historical private/friends activities are
filtered before count and pagination. Historical invisible statuses are owner-only.
Public history omits arbitrary status/activity metadata and manual session labels;
no nested account, message, device or media entity is constructed. Canonical artwork
association checks handle media ownership and retention. The entire page's
association hashes are acquired in global order before the first media-file lock,
so different event orders cannot invert association lock order. Historical events do not
pretend the account is currently online. Original proof and audience are rechecked
after account and artwork waits. If friendship changes with wall-clock expiry
while a page is prepared, the read fails closed instead of exposing a partial page
with an inaccurate count. These privacy measures intentionally harden the source's
unfiltered public merge.

History is intentionally distinct from current presence or live friend activity.
The authored AccountEventService.GetTimeline (retained excerpt lines 7–19 and
49–55, upstream lines 1833–1845 and 1875–1881) selects nondeleted account events
without a session or activity-lease liveness test. AccountController lines 368–390
calls that historical merge. Accordingly, source logout, authentication-session
expiry, or an ended/expired activity lease alone does not erase a historical
public event or change its count. The original current reader proof is still
required for authenticated reads. Current account disablement, blocking, account
privacy, per-record audience, deletion/redaction and artwork rights remain live
checks. Historical records do not acquire an ongoing live-session eligibility
requirement, and statuses are not assigned invented original session provenance.

Actions are a read-only union of five actual canonical audit sources:
owned_accounts_audit, owned_social_audit, owned_progression_audit,
owned_authority_contact_audit, and owned_authority_factor_audit. Existing atomic
mutations feed them; no successful-looking empty RPC or rewind telemetry is used.
Namespaced source-table/sequence digests provide deterministic UUID wire IDs.
Metadata contains only target ID and exact source-table/sequence provenance. Owned
action names are retained; no guessed legacy action-name mapping is claimed.
This is a supported local audit subset, not a complete historical cross-service
log migration. No raw credential, token, arbitrary action payload or foreign
account's audit is selected.

The explicit owned schema supplies immutable credit records with unique nonempty
source-kind/reference/record provenance. UPDATE and DELETE fail; correction would
require a separately authorized compensating record. No runtime reward writer,
legacy import, grant, cache, account enrollment or initialization side effect is
created here. PostgreSQL computes the authored sum from the actual immutable rows
at one database instant, with deterministic summation order and finite-value
validation. New owned accounts with no ledger rows yield the source's 100 baseline.
This does not assert that an existing user's legacy balance has been imported.
Readiness requires the real tables: missing storage is unavailable, never zero.
Ledger status=0, updated_at=created_at and deleted_at=null are the owned immutable
record projection, not recovered excluded-model status semantics. Public credit
reads use the current account status-visibility/block policy as an owned privacy
rule; hidden credits return not-found, not a zero balance.

Connection reads select only established, verified, non-revoked metadata from
owned_account_connections. IDs and provider identifiers must already exist; none
are synthesized. Only email, username and display_name can enter the metadata map.
There is no arbitrary JSON serializer, credential column, remote fetch or provider
call. The schema and query make metadata persistence/read behavior concrete;
provider linking, authentication, verification writers and legacy imports remain
absent. An empty canonical owned table means no links established in this backend;
it does not claim to reflect inaccessible external accounts. A 2,000-row safety
bound fails unavailable rather than silently truncating results.

## Consistency, extensions and installation

All private reads require the original same-pool transaction proof. Public reads
with a supplied invalid bearer never fall back to anonymous. Sorted canonical
account locks precede domain reads and lock-wait rechecks; viewer IDs, query tokens
and alternate identity headers cannot supply authority. Connections additionally
recheck their permission. Writers that establish imported connections/credit records
must follow this canonical account lock protocol; no mixed legacy writer is claimed.

Count/page queries use one materialized SQL snapshot, including empty pages. The
owned secondary order is timestamp descending, then event kind and UUID (timeline)
or UUID (private logs/credits). Offset remains the authored pagination style; there
is no promise of stable offset pages across new concurrent inserts. Inclusive `from`
and exclusive `to` RFC3339 bounds and literal case-insensitive action substring
filtering are explicit owned extensions. PostgreSQL timestamps have microsecond
precision. Pages are bounded at 200 and offset at 100,000; duplicate, malformed or
unsupported query fields fail validation. No unbounded in-memory history load occurs.

Install internal/accounthistorypg/schema.sql explicitly after canonical authority,
accounts, social, sign-in contact/PIN audits, presence/activity and progression
schemas. Constructors and readiness do not run DDL. This is an owned migration input,
not an upstream schema migration. Restoring the previous binary leaves these read
models inert; audit source rows are never rewritten. No deployed production or
legacy PostgreSQL compatibility is claimed.

## Verification

The source integrity gate verifies all 13 retained source/license entries and
mutation controls. Local build, vet, unit, race, SQL syntax/argument checks and
fixture registration are separate from actual PostgreSQL execution. The eleven
new guarded PostgreSQL cases include authored composed routes, canonical mutation
history, privacy and exact count/pagination, immutable credit provenance/decay,
self-only connection projection, audit provenance, original-proof/account waits,
artwork wait/retention/expiry, source logout/expiry historical persistence,
explicit privacy withdrawal and event redaction, failed-audit rollback preserving
prior rows, and original-credential database/process restart. Four in-process
SQL-driver regressions independently exercise globally ordered two-event artwork
locks, later-reference retention expiry, current reader/audience withdrawal after
waits, and historical survival of source logout/lease expiry. They do not certify
PostgreSQL execution.
Local PostgreSQL execution is prohibited in this workspace. Those cases compile
but skip locally; exact-commit private CI must execute before/after phases without
skips before any database acceptance claim.
