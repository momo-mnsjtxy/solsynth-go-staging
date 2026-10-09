# Account activity leases and catalog candidate

This AGPL-derived port implements the ten authored activity operations in
`PresenceActivityController.cs` at DysonNetwork `4ee2d0cebb9da8284a92483fee6dce795bf6947c`.
The complete retained controller is
`inventory/operation-sources/DysonNetwork.Passport/Account/PresenceActivityController.cs`
(blob `2f502003c925e32c7e59e19d44cd592be099755b`). The complete authored
AccountEventService blob `1f9891d9bfc97ffbc7306213e0d7f5a10d63b4ce` was read through
an authorized GitHub connector. Only its activity methods were retained here;
fortune prompts/corpus and unrelated prose were not retained.

Source evidence includes the complete authored EF snapshot in
`ports/passport/board/source/DysonNetwork.Passport__Migrations__AppDatabaseModelSnapshot.cs`
(activity, catalog and tag models at lines 2415–2700), the existing canonical
artwork service source, and pinned licensed Solian HTTP caller excerpts/parser
fixture. The source manifest distinguishes excerpt hashes from original blobs.
No excluded Shared implementation, generated protocol definition, Socommon,
Suki, sensor integration or canceled retrieval was used.

## Routes and preserved behavior

- GET `/api/activities`: authenticated active list; filtered/history requests use
  `includeExpired`, `offset`, `take`, `query`, `type`, `provider`, `referenceId`,
  `term`, repeated `tags`, `visibility`, `catalogId`, `from`, `to` and `X-Total`.
- GET `/api/activities/{identifier}`: active account-name/UUID projection only.
- POST `/api/activities`: manual-ID upsert or generated-ID legacy activity.
- POST `/api/activities/sessions/start`: manual-ID session start/renewal with
  optional stable catalog and tags. Active renewal retains the session ID/start.
- POST `/api/activities/sessions/end`: closes a live session once; replay is 404.
- GET `/api/activities/catalog`: owner-only accumulated catalog, conjunctive
  tag filters, provider, literal query, 24-hour last-completion `isActive`, paging.
- GET `/api/activities/stats`: owner-only per-catalog floor-second sums of ended
  rows, with inclusive `from`/exclusive `to` on end time and catalog tag filters.
- GET `/api/activities/tags/stats`: per-session-tag ended-duration sums.
- PUT `/api/activities/{id}`: owner-only partial content/lease update.
- DELETE `/api/activities?id=…` or `manualId=…`: active expiry first; an already
  expired UUID can be tombstoned. Deleting does not fabricate session completion.

`/passport/activities` aliases are supported from the authored Solian caller.
Bodies and projections use snake_case; query names preserve controller spelling.
The source uses strings for activity type and provider. Provider and terms are
normalized; tags use distinct trimmed case-sensitive slugs and first names.
Leases are 1–60 minutes with a five-minute HTTP default. Statistics count only
explicit successful session ends. Expiry, deletion and a failed end add no time.

## Explicit owned differences and limits

The independently owned tables and account locks replace source EF/cache races.
End, catalog count/time/tag accumulation and durable activity event commit in one
transaction. These events contain IDs/actions/revisions only; they are local
state evidence, not a claim of original WebSocket/event-bus delivery. There is
no live provider polling, fake observed activity, imported provider statistic,
background sensor start, or implicit elapsed-time accrual.

Every request retains the original opaque authority proof and rechecks it after
lock waits. Account locks use the same sorted order as canonical social writes.
Active projections additionally check the originating canonical session,
account version, durable authorization generation, revocation, expiry and account
punishment. Ordinary refresh advances the access-token generation without
invalidating an existing activity; source scope changes invalidate its recorded
authorization generation. The prior access token still fails current proof checks. A new current proof
cannot act as another account. PUT/DELETE use the explicitly owned permission
`presences.activities.manage`; POST/start/end retain the authored authentication-
only owner contract. Scope narrowing is still enforced for permissioned writes.

Cross-account activity responses enforce current account eligibility, effective
block/friend relationships, existing status visibility and invisibility, plus
per-activity visibility. No private history/catalog/statistics route accepts a
target account. Public visibility = 1 is proven by the authored Solian test;
Private = 0 and Friends = 2 are declared owned encodings, not a recovered full
Shared enum. Unknown values are rejected. Legacy POST accepts the current
caller's tags and honors optional visibility rather than silently publishing an
explicitly private request. Both manual-ID renewal routes retain existing type and visibility when those
fields are omitted. A new row still requires a type. Null visibility is rejected
instead of applying the public default. This also applies through Passport aliases. PUT retains its original visibility; use session
renewal for session visibility changes. The caller's title/subtitle URL inputs
are accepted as HTTPS metadata only, and never fetched. Unsafe URLs and inline
image data are rejected. Opaque `discord:` image markers remain caller metadata.

Hash artwork writes require the same account's canonical ready, unexpired image
association and recheck it after waits. They cannot attach another uploader's
private file or revive an expired retention grant. Reads hold the canonical association/file locks and omit unavailable or
foreign-owned hash references. Current proof, audience, source-session eligibility
and lease time are checked after artwork contention. Session renewal and end
roll back if the original activity lease expires during the final wait. This is stricter than the original global hash touch method.

Changing an active session's catalog identity or crossing between legacy and
session modes is a conflict. Ended/expired sessions cannot be revived through
PUT. Session tag renewal replaces the submitted set; catalog completion merges
by slug. This fixes the retained service's kept-removed-tag behavior. Catalog
metadata is initially authored; existing catalog metadata is not silently
rewritten by each session. Deleting an ended row excludes it from row-derived
stats while historical catalog totals remain, matching the original distinction.

Paging is bounded to 1–200, offsets to 100000, terms/tags to 64 and metadata to
16KiB. Unfiltered active lists fail closed above 1000 rows. Payloads are bounded,
unknown body fields are rejected, and exact duplicate JSON key behavior is not
claimed as an MVC compatibility oracle. Current-client compatibility is covered
by HTTP wire fixtures, not a complete existing-client end-to-end run.

`GET /api/friends/overview`, provider sessions, full account timeline/admin
history, original protobuf/gRPC internal calls and friend WebSocket broadcasts
remain outside this bounded ten-route delivery. Canonical online/status presence
is retained unchanged.

## Verification

Offline source hashes, Go compile/unit/race, SQL parameter checks and PostgreSQL
16 grammar checks are separate from real PostgreSQL execution. Named integration
cases are registered in the fail-closed runner and both restart phases. The
restart fixture saves original pre-target credentials, public verification keys
and an elapsed wall clock in the private disposable marker; it does not reissue
credentials or rewrite authority after restart. Only coordinator private CI may
establish actual-PG or process/restart acceptance.

Independent review keeps catalog participation owner-only: names, union tags,
artwork, completion timestamps, counts and duration never become global catalog
search results. No account-target parameter or nested public catalog projection
is introduced. Private/friends session tags remain visible to the owner in
catalog and statistics routes only.
