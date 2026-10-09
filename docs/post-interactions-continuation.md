# Post interactions and canonical associations

This continuation is an independently owned PostgreSQL implementation of bounded
licensed post interactions. It uses the existing `owned_authority_accounts`,
`owned_publishers`, publisher membership and `owned_text_posts` authority. It does
not create a second post, publisher, session or permission identity. It does not
claim full Sphere parity or legacy database compatibility.

## Implemented behavior

- `POST /api/posts/{id}/reactions` toggles one actor/post/symbol tuple. A new
  reaction returns 200, removal returns 204. The symbol allowlist is the pinned
  controller's 15 built-in symbols. Positive=0, neutral=1, negative=2 are evidenced
  by the licensed authored Solian reaction sheet. Custom reactions require a
  missing subscription/sticker provider and return 501.
- `GET /api/posts/{id}/reactions` supports symbol, order, offset, take and X-Total,
  plus a bounded public account projection. The source's Take-then-Skip behavior
  is retained: the returned limit is `max(0,take-offset)`. Unlike the source's
  unfiltered read, inaccessible, deleted or disabled-publisher posts do not
  disclose reaction identities or counts.
- `POST/DELETE/GET /api/posts/{id}/bookmark` adds idempotently, removes an existing
  actor-owned bookmark, and returns an actor-private record or JSON null. A
  bookmark can still be removed after its post becomes private or deleted.
- `GET /api/posts/bookmarks` returns currently visible canonical posts, ordered
  by bookmark creation descending with a stable ID tie-breaker. X-Total and the
  page come from one repeatable-read snapshot. Invisible posts do not contribute
  to either. Stored status reveals no post body or other actor's bookmark.
- `GET /api/collections`, and list/create/detail/update/delete beneath
  `/api/publishers/{publisher}/collections`. Slugs are normalized with existing
  tested .NET-compatible trim/lower helpers; metadata has the source UTF-16
  limits. Creation returns 201 and Location. Update replaces both nullable name
  and description as the service does. File fields are accepted only as null;
  actual background/icon requests require a missing file provider and fail 501.
- Collection item list/add/remove, batch-add and batch-remove use
  `.../{slug}/posts`, `.../posts/{id}`, `.../posts/batch`, and
  `.../posts/batch/remove`. Lists use published/created/ID descending and filter
  visibility before counting. Individual duplicate additions return the source
  failure; batch-add deduplicates idempotently. Batch writes are atomic.
- Manual order requests and `PUT .../posts/reorder` return the pinned controller's
  explicit unsupported-order errors. Reorder rejection is not counted as a
  substantive mutation implementation.
- The post lifecycle accepts `tags`, `categories` and create-time `collection_ids`.
  Supplied tags/categories replace links, omitted/null values preserve them,
  and empty lists clear them. Missing tags are auto-created; categories must
  already exist. Protected tags require the same canonical publisher and event
  tags must not be expired. Creator-selected collections must belong to the same
  publisher. Lifecycle, auto-created tags, links and local audits commit together.
- Loaded post projections contain tags, categories, publisher collections and
  `reactions_count`/`reactions_made` maps. The current viewer alone determines
  `reactions_made`; anonymous readers receive an empty map. Every relationship
  refers to the canonical post row. There is no fabricated delivery notification.

## Authentication and host composition

`interactions.NewHandler(repo, posts.Auth)` uses the host's private-proof adapter.
The standalone `NewVerifiedHandler` validates optional bearer proof, rejects
asserted identity and credential-shaped query/header channels, then removes raw
credentials before domain dispatch. Authentication failures do not become anonymous
fallback. HTTP JSON bodies are bounded; unknown and duplicate members fail.

Use `interactionpg.NewWithAuthority(db, guard)` in the authenticated host. It
revalidates the same immutable signed proof, session epoch, account version,
scopes and current grants in the mutation's own ReadCommitted SQL transaction,
under the canonical account lock. A nil guard is an unavailable guard, never a
fallback. `New(db)` is a trusted direct-repository/test composition and must not
be exposed as a replacement for the guarded host constructor.

The registered permission strings come from retained authored
`internal/auth/source-docs/PERMISSIONS.md`: `posts.react`, `posts.bookmark`, and
`post.collections.posts.manage`. Metadata collection writes have only Authorize
in the pinned controller and therefore use proof authentication plus current
canonical editor membership, without inventing an additional permission check.
Post associations use the enclosing post create/update authority.

Composition with the canonical social provider is additive:

```
ir := interactionpg.NewWithAuthority(db, guard).WithVisibility(socialProvider)
pr := postpg.NewWithAuthority(db, guard).WithAssociations(ir).WithVisibility(socialProvider)
ih := interactions.NewHandler(ir, hostPostIdentity)
ph := posts.NewHandler(pr, hostPostIdentity)
```

The parent host must bind the same guard proof in each module request context.
`interactions.RoutePatterns` lists exact routes for host dispatch ahead of the
post slug fallback. No local command or HTTP listener was added.

`postpg.VisibilityProvider` supplies fixed compile-time SQL predicates and
transaction-scoped social reads. Its SQL methods take no user input and refer
only to the canonical aliases p/u and actor parameter $1. With this provider,
Friends=1 and CloseFriends=4 are accepted; without it, writes using either value
return 501. Public=0, Unlisted=2, Private=3 retain existing behavior. The list,
detail, bookmarks and collection count/list queries use the same predicate in
the same snapshot. QuietPublic=5 remains unsupported. SQL/schema friendship
semantics are owned by the separate social module and require its verification.

## Schema and consistency

Install explicit fresh schemas in dependency order: authority, accounts, canonical
posts, owned taxonomy, interactions (and the social schema before social-backed
queries). Nothing migrates on application startup. The supplied
`docs/post-interactions-canonical-schema.patch` is a proposed change to the
parent-owned `internal/application/schema.sql`:

- Replace the old owned taxonomy post table with a read-only join view over
  canonical posts/publishers and enabled owner accounts.
- Change taxonomy link foreign keys to `owned_text_posts` directly.
- Keep only active posts/publishers in that view. Taxonomy repositories continue
  applying their own draft/public listing predicates.

This is a fresh-install schema change, not an in-place upgrade script. Existing
installations need a separately reviewed migration; no automatic conversion or
unreviewed deletion is attempted. Owned taxonomy's public visibility must be 0,
matching the canonical post enum, rather than a synthetic fixture value.

Reaction and bookmark unique constraints enforce actor/post ownership, and
collection item keys enforce uniqueness. Soft-deleted collection slugs remain
reserved. Collection deletion atomically removes item links and records an audit;
post soft deletion hides retained links through canonical visibility.

Mutations lock the canonical actor account first, then publisher/post or
publisher/collection rows, and retain those locks until commit. Publisher editor
checks therefore serialize with membership revocation. Tag changes use the same
SHARE ROW EXCLUSIVE table lock as the taxonomy adapter; this prevents
concurrent duplicate auto-creation and protection/claim races. SQL identifiers
are fixed and all user values are bound arguments. Read count/projections use
repeatable-read snapshots. These independent locking policies favor correctness
and need throughput profiling before large deployment.

`owned_post_interaction_audit` is a local transactional ledger. Audit failure rolls
back the mutation; successful idempotent retries do not add duplicate mutation
rows. It is not an outbox, remote action log, notification, federation event or
proof of delivered side effects.

## Evidence and exact limits

The evidence manifest records git blob IDs, SHA-256 and exact bytes for the full
licensed PostActionController, PostCollectionController, PostController,
PostCollectionService, PostService, authored permission registry and five authored
Solian caller files. Newly retained bytes match the pinned GitHub blob hashes.
DysonNetwork is pinned to `4ee2d0cebb9da8284a92483fee6dce795bf6947c`; Solian to
`ffd0838e5f04cb40bea311a39118ebeedad210c1`. The corresponding AGPL license files
are retained. No Shared/NeTo/Spec content, generated schemas or bindings, SoSYS,
Socommon or Stargate content was retrieved. A Dart SDK import is not treated as
permission to inspect the imported dependency.

The JSON is a bounded owned projection using evidenced member meanings and the
existing host's snake-case convention, not a claim to a complete excluded shared
entity serializer. Account profile images, remote publisher/instance objects,
all inherited fields and exact global ASP.NET error formatting remain outside
this result. Tag/category usage in embedded post projections is not populated,
matching the existing public detail projection convention.

Intentional limitations and stronger policies:

- Collections accept posts from their own publisher only. Foreign-publisher
  additions fail explicitly with 501; no hidden private content is accepted.
  The upstream collection service is broader and would need a separate policy.
- Global collection search implements the source ILIKE branch for queries shorter
  than three UTF-16 units. Longer queries require pg_trgm ranking and fail 501.
- Public collection lists preserve the source unpaginated publisher list;
  global lists and post pages use a bounded 1–100 take/offset policy.
- Collections subscriptions/adjacent-navigation/admin moderation, user reaction
  histories, custom reactions, scores/vote weights, interest signals, reactive
  broadcasts, watches, federated actors, paid awards/sponsorship, files, automatic
  topic inference and realm post publishing remain unsupported.
- Existing source failure/race behavior is not reproduced when that would allow
  partial writes, ambiguous identities, unfiltered private counts or duplicate
  actor state. These differences are documented independent implementation choices.

## Verification

Targeted Go unit tests cover strict request binding, actor requirements, wire
attitudes, null bookmark status, privacy headers, UTF-16 limits, paging and the
pinned source hashes. PostgreSQL tests are compiled locally and intentionally
skip without the marked disposable DSN. No PostgreSQL or HTTP socket was opened.
Actual database, lock-wait, process and restart results must come from the
parent's authorized exact-tree CI; a skipped case is not a pass.

The new PostgreSQL cases are named `TestPGPostInteractions...` in
`tests/integration/post_interactions_test.go`. The parent must append them to the
next strict before-phase inventory after review. The only restart case is
`TestPGPostInteractionsRestartPersistence`; its `PG_TEST_PHASE=after` branch
contains no migrations, seeding, credential creation or writes. It reads the
same persisted post, tag, reaction, bookmark, collection and audit sentinel.
The previously frozen 80/14 harness snapshot is unchanged by this worker.
