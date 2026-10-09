# Publishing settings and bounded post batches

This owned continuation is based on `2482cb6cd002b7c2d4ba4bba4e42a8311378bb41`.
It adds authenticated `GET/PATCH /api/account/publishing` (also composed at
`/sphere/account/publishing`) and `POST /api/posts/batch/delete` and
`POST /api/posts/batch/visibility` (also under `/sphere/posts`). Nothing is
installed or migrated by a constructor, and no provider is contacted by these
operations.

## Evidence and contract boundary

The retained complete authored Sphere settings/action controllers, Solian
settings screen and permission documentation are indexed with byte counts,
Git blob IDs and SHA-256 in `publishing-controls-evidence.json`. The licensed
Sphere EF snapshot establishes settings identity/account/timestamps and three
nullable publisher IDs. The complete settings DTO belongs to excluded Shared
and the current SDK to canceled Socommon; neither was inspected. The response
is explicitly an owned scalar projection, not a claim of complete original DTO
or navigation-property parity. It contains `id`, `account_id`, `created_at`,
`updated_at`, `deleted_at`, `default_posting_publisher_id`,
`default_reply_publisher_id`, and `default_fediverse_publisher_id`. Unset values
are JSON null; navigation objects and private publisher metadata are absent.

GET lazily creates one settings row for the authenticated account. PATCH retains
both omitted and explicit-null choices, as the controller does, and always
updates `updated_at`. Non-null values must be canonical UUIDs, refer to active
publishers, and have current joined membership. A joined viewer may save a
choice but does not gain permission to post. The Fediverse choice additionally
requires the explicitly composed canonical federation adapter to observe an
already enabled registered local actor. It creates no actor, key, opt-in,
consent, subscription, or delivery authority. A missing provider rejects that
choice with `PUBLISHER_FEDIVERSE_NOT_ENABLED`.
The configured federation origin, immutable identity handle and key-ID anchor
must match the canonical local actor. A disabled or moderated actor is unavailable;
an arbitrary actor URI with a matching key suffix is insufficient. The adapter
uses the caller's transaction and keeps a shared federation-row lock through the
final post-wait check, without loading or provisioning private key material.

An explicit post publisher selector still wins. Original posts use the posting
choice; replies use the reply choice. Choices are observations for planning
locks and are resolved again under those locks. Invalid/stale/noneditor defaults
fall back to the existing owned single-eligible-publisher rule. Multiple eligible
publishers require an explicit choice; the source controller's arbitrary first
individual fallback is deliberately not reproduced. Editor proof, canonical
realm membership/actions, active owner and all existing creation checks remain
necessary.
Fallback candidates use the same current membership, visibility and active-owner
filters as defaults, including time-dependent owner disable punishments.

Batch envelopes retain `post_ids`, and visibility batches additionally accept
nullable `visibility`, `drafted_at`, and `published_at`; null means no change.
Both operations require `posts.batch.delete`, following the controller's shared
`PostsBatchDelete` attribute even though permission documentation also names
`posts.batch.visibility`. Success is HTTP 204 with no projection. Batches contain
1–100 distinct UUIDs; duplicates, unknown keys, malformed JSON, unsupported
visibility values and combined draft/publication timestamps fail before writes.
These bounds and strict duplicate/error handling are owned hardening. A missing
post returns `POST_BATCH_NOT_FOUND` with a requested ID, without exposing a
publisher name or private post fields. Locked posts, missing membership and
invalid past-publication changes fail the entire transaction. Explicitly
publishing or scheduling also requires the current original `posts.create`
authority, an owned restriction preventing batch rights from becoming creation
rights. Future timestamps join the existing durable schedule workflow instead of
making a future post prematurely visible.

Pin/unpin remain unavailable: retained authored sources identify symbolic pin
modes but do not establish their numeric mapping. Boost and navigation work is
outside this slice.

## Transactions and storage

The additive account schema keeps `default_publisher_id` as the established
internal posting column, makes it nullable, and adds reply/Fediverse choices,
UUID identity and timestamps. Existing rows retain their posting choice; new
identity/timestamp defaults are explicit owned migration metadata. Applying the
schema again does not rewrite identities or choices. The authenticated host
checks the new columns in readiness.

Settings lock the original account proof, all observed realms, then the sorted
publisher set, then settings. Membership, realm ancestry, moderation, active
owner and original proof are rechecked after possible settings/audit waits.
Concurrent single-field patches merge atomically. Account/publisher and realm
normal/admin deletion paths clear only matching columns and preserve row
identity, other defaults and future settings columns. Member removal/leave
clears only that member's matching choices.

Batch writes require the composed relation-aware adapter. It observes the
complete set of source and related posts, acquires all realms before all sorted
publishers, then all sorted source/related post locks in their final exclusive
mode. Per-post lifecycle work therefore cannot acquire a new late publisher or
upgrade a previously shared related-post lock. All required rights are checked
before mutation. The canonical lifecycle preserves associations, removes media
links on deletion, evaluates automod, writes post audit, and updates/cancels
scheduled intent in the same transaction. Existing federation reconciliation
consumes those committed audit/lifecycle rows; no new direct-send mechanism or
outbox replacement is introduced. Post/owner/realm/media/reference/blog gates
and original proof are checked again after later batch members have waited.
Failures roll back the entire batch including audit and schedule events.
Repeated delete requests fail once a source is deleted and create no additional
mutation or audit. Visibility changes retain the existing lifecycle semantics:
each accepted request is audited, and explicit replacement scheduling supersedes
the prior intent. No new request-idempotency key contract is claimed.

## Validation status

The added unit cases exercise the actual HTTP envelope, permission, scalar
projection, null intent, bounds, duplication and malformed-body rejection.
Thirteen real-PostgreSQL declarations cover composed routes, distinct default
post/reply selection, explicit override, current membership and realm access,
Fediverse opt-in, all cleanup paths, concurrent patch merging, proof/member waits,
cancellation, audit rollback, atomic batch privacy/deletion, reversed batch lock
order, media-owner loss during a later batch wait, creation-right isolation and
actual server-restart persistence. They are designed for the coordinator's
private disposable PostgreSQL CI only. Local package success skips those cases
when `PG_TEST_DSN` is absent and is not database execution evidence.

The restart marker `publishing-controls.json` stores only synthetic IDs and a
canonical SQL snapshot of settings/posts/audit. The after phase compares it,
reopens the composed HTTP host, confirms committed public/deleted post behavior,
and retrieves the persistent settings with newly issued authenticated proof.
No external provider, production data, credential, account or socket server was
used locally.

Independent review also added canonical actor origin/anchor/key-ID unit cases,
and expanded the existing PG declarations with inactive-owner fallback, changed
origin/moderation rejection, final owner loss after a settings-row wait, signed
scope caps, private reply/forward projections and deletion replay. The original
thirteen declaration names and restart marker remain unchanged. These expanded
PG assertions have compiled but still require the private disposable CI run.
