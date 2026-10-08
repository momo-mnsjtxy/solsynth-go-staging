# Bounded authenticated text-post lifecycle

This continuation implements a reviewable PostgreSQL-backed text-post lifecycle:
create, detail by ID or slug, edit, soft delete, list, list drafts and publish a
draft. It uses the shared owned authority and canonical account/publisher store.
It is not a full DysonNetwork post service, a legacy database adapter, or a
production-ready compatibility claim.

## Evidence and licensing

`ports/sphere/posts/evidence-inventory.json` inventories **all 21 operations in
PostController and all 21 in PostActionController**, preserving source action
signatures, route attributes, line ranges, literal errors, authorization
attributes, dependency signals and immutable source links. Eight routes have a
bounded implementation; the other 34 require further providers and semantics.
Source hashes and retained license files are included.

The server observations are from AGPL-3.0-only Solsynth/DysonNetwork revision
`4ee2d0cebb9da8284a92483fee6dce795bf6947c`, retained under
`inventory/operation-sources/DysonNetwork.Sphere/Post/`. PostController provides
listing/detail/drafts; PostActionController provides the request envelope,
create/update/delete/publish, publisher-editor checks, and lock/draft guards.
The source calls `FilterWithVisibility`, PostService, shared models, RPC services
and side-effect providers whose behavior is not implied by a controller call.

Licensed Solian composer files already retained by the interrupted predecessor
under `ports/sphere/posts/upstream/` are pinned to
`ffd0838e5f04cb40bea311a39118ebeedad210c1`. In particular,
`compose_settings_sheet.dart` shows Public=0, Friends=1, Unlisted=2, Private=3,
CloseFriends=4 and QuietPublic=5. The retained composer code supports the observed
Moment=0, Article=1 and Blog=2 wire numbering and snake_case request fields. Those
numbers are not taken from the excluded Shared model package. This continuation
retrieved no new remote source and no NeTo/Spec, SoSYS, Socommon or Stargate code.

## Implemented HTTP surface

The standalone handler serves the following paths; the unified host separately
owns any `/sphere` gateway alias.

| Method/path | Owned behavior | Permission |
|---|---|---|
| POST `/api/posts?pub=<name-or-id>` | Create text Moment/Article, optionally draft | `posts.create` + accepted publisher editor |
| GET `/api/posts/{id}` | Visibility-filtered detail | Public or fresh optional identity |
| GET `/api/posts/{publisher}/{slug}` | Case-insensitive canonical publisher/slug detail | Same |
| PATCH `/api/posts/{id}` | Partial text/type/visibility/language/draft update | `posts.update` + accepted publisher editor |
| DELETE `/api/posts/{id}` | Soft delete | `posts.delete` + accepted publisher editor |
| GET `/api/posts` | Filtered paginated list with `X-Total` | Public or fresh optional identity |
| GET `/api/posts/drafts` | Editor-accessible drafts with `X-Total` | `posts.view` + accepted publisher editor |
| POST `/api/posts/{id}/publish` | Atomic draft-to-published transition, empty body | `posts.create` + accepted publisher editor |

Successful create/update/publish/detail/list return 200; delete returns 204.
`POST .../publish` returns 400 `POST_NOT_A_DRAFT` for a second publication.
Locked posts reject edit, delete and publish with 423. The additional locked
publish guard is an owned safeguard, not a claim about the original controller.

Supported request fields are `content`, `title`, `description`, `slug`, `type`,
`visibility`, `language`, `drafted_at`, and `published_at`. Text content must be
nonblank. Meaningful values for attachments, taxonomy, embeds, realms, replies,
forwards, chains, locations, thumbnails, collections and metadata fail with 501
`POST_FEATURE_UNSUPPORTED`; known neutral null/empty values are accepted. Unknown
fields fail rather than disappear. Identity, publisher IDs and server-owned
timestamps in the body are rejected. Duplicate keys, malformed JSON, multiple
JSON values and duplicate query parameters are rejected.

Lists support `offset`, `take` (1–100, default 20), `pub` (one canonical publisher
name or ID), `type` (0/1), `order` (date aliases), and `orderDesc`. Drafts accept
only pagination and `pub`. Sorting is deterministic by publication/draft time
then ID. Search, popularity, multiple publishers and all other filters are
explicitly unsupported. Publisher transfer through PATCH `?pub=` is unsupported.

## Owned privacy and persistence rules

These rules are intentional, reviewable local policy. They do not assert the
unavailable FilterWithVisibility implementation's exact behavior:

- Published Public is readable and listed for everyone.
- Published Unlisted is readable by exact ID or slug; it appears in lists only
  for accepted publisher editors.
- Private and drafts are readable only by accepted, active publisher editors.
  Drafts never enter the normal list. Unauthorized detail requests return 404,
  and list totals are computed from the same visibility-filtered snapshot.
- A revoked or pending membership gives no editor access. Disabled actors,
  disabled individual publisher owners, deleted publishers and deleted posts
  cannot expose private data. Organization publishers are now supported through the later [realm publishing continuation](realm-publishing-continuation.md); independent post `realm_id` associations remain unsupported.
- Relationship-dependent visibility (Friends, CloseFriends, QuietPublic),
  gatekept subscriptions, blocking and realm policy have no synthetic fallback.
  The owned publisher schema does not import those states. Legacy data must not
  be copied into this schema while discarding them.

All write transactions lock the actor account, then the canonical publisher,
then the post. `accountpg.ResolveForPost` and `RequirePostEditor` check accepted
membership while holding the same publisher lock used by membership changes.
No account ID is fabricated as a publisher ID. Default selection uses the
canonical configured publisher, or exactly one eligible publisher; ambiguous
selection fails. Partial updates reload locked rows, preventing lost disjoint
patches. Post mutations and local audit inserts commit together. A failed audit,
duplicate slug or cancelled transaction rolls back the post mutation. Two
simultaneous draft publications produce one publication and one audit.

Reads use repeatable-read transactions for consistent filtering, data and
`X-Total`. Responses are `Cache-Control: no-store` and vary by authorization.
The post projection contains its basic canonical publisher identity; it does
not claim full SnPost enrichment, remote profile hydration or computed counters.

Slug uniqueness is case-insensitive within a publisher and remains reserved
after deletion. Title/description/language are trimmed; blank values clear
optional text. Omitted/null patch fields leave the field unchanged. Omitted
visibility on PATCH preserves the stored value. A non-null `drafted_at` marks
the post as a draft and clears publication; non-null `published_at` publishes
and clears draft state. Creating with neither publishes immediately. Future
publication scheduling is rejected until a scheduler is supplied.

Local safeguards include a 2 MiB JSON envelope, 1 MiB content, bounded pagination
and validation of NUL/invalid UTF-8 text. These are not asserted upstream limits.
The reconstructed response stores text unchanged; clients must render untrusted
text safely. Exact upstream TextSanitizer behavior was not imported or guessed.

## Authentication and hosting

`postpg.New(db)` never migrates or seeds. `posts.NewHandler(repo, auth)` accepts a
trusted adapter whose Authenticate and Authorize methods verify proof and fresh
authority state. The unified host supplies its private-context adapter.
`posts.NewVerifiedHandler(repo, verifier)` is the standalone equivalent: it
verifies a single Bearer credential, stores private proof only, strips bearer
and cookie headers, and refuses synthetic/proxy identity channels. It rechecks
session/account state for authenticated reads and scope/grants/punishments for
mutations. Caller-constructed principals are not trusted.

Create/publish `posts.create` and drafts `posts.view` correspond to retained
controller permission observations. Requiring `posts.update` and `posts.delete`
is an explicit stronger owned policy supported by the licensed permission
catalog; the original update/delete controller only shows authentication and
publisher-editor checks. Permission checks occur at the request boundary;
membership and actor-disabled checks are repeated inside the write transaction.
The work does not claim a distributed transaction across authority, events,
federation, indexing and storage providers.

The standalone executable is `cmd/post-service`. It requires explicit DSN,
issuer, audience, trusted key ID and RSA public-key file. Without `-serve`, it
only validates configuration and opens neither a connection nor a listener.
With `-serve`, it uses an explicitly configured database behind TLS ingress.
No keys, credentials, accounts, grants, quotas or schema are generated at service
startup. Ambient pgpass/service/client-certificate configuration is disabled.

## Schema and verification

Explicit migration order for a disposable or approved owned database:

1. `internal/authoritypg/schema.sql`
2. `internal/accountpg/schema.sql`
3. `internal/postpg/schema.sql`

Post-owned tables are `owned_text_posts` and `owned_text_post_audit`; publisher
foreign keys point to `owned_publishers`. There is no migration from a legacy
posts schema. Unified-host readiness must require these tables.

Local socket-free validation:

```sh
go test -race ./ports/sphere/posts ./internal/postpg ./cmd/post-service ./tests/integration
```

The real PostgreSQL tests use the existing `database(t)` guard requiring the
synthetic disposable-cluster marker and `PG_TEST_DSN`; absence skips them unless
`PG_TEST_REQUIRED=1`, when it fails. The cases are:

- `TestPGPostsLifecycleAndPrivacy`
- `TestPGPostsAtomicRollbackAndCancellation`
- `TestPGPostsVisibilityAndStateGuards`
- `TestPGPostsIndependentPoolConcurrency`
- `TestPGPostsMembershipRevocationSerializesWithWrites`
- `TestPGPostsAuthenticatedHTTPAndFreshAuthority`
- `TestPGPostsRestartPersistence`

The before phase seeds and publishes a sentinel. The after-restart phase must run
only `TestPGPostsRestartPersistence` with `PG_TEST_PHASE=after`; that branch does
no migrations, seeding or writes and asserts the original post and audit rows.
The combined harness must include the test name in both strict phase allowlists.
HTTP tests use in-process recorders and no listener. The membership race test
observes a real PostgreSQL lock wait before revoking the editor and releasing the
publisher lock.

At this handoff, targeted local tests and race checks pass; real PostgreSQL,
process HTTP and restart tests have only been compiled locally and await the
authorized isolated CI harness. No local PostgreSQL or HTTP socket was opened.

## Remaining work

Unsupported operations include featured/bookmark/sponsor/reaction/reply/thread/
chain/navigation/forward reads; batch mutations; blog/domain checks; media,
collections, taxonomy, embeds, realm moderation, pin/boost/awards/payments;
federation, post watches, notifications, search indexing, view counting, storage
validation and scheduled publication. Local audit rows are not an outbox and
must not be described as delivered events. Gatekeeping, user relationships,
blocking, legacy import, full model enrichment and upstream error/body parity
remain separate release gates. The machine-readable inventory retains every
observed operation so these gaps are visible rather than silently counted as
implemented.
