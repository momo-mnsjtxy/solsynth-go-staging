# Individual posts associated with realms

This delta is based on the frozen organization-publishing delivery (`realm-publishing.patch` SHA-256 `99eca5da94b77b254fe7338e67ce734855863efdc1d6a10119d73a3092574a18`). Organization publisher behavior remains separate: an organization's implicit publisher realm is not the individual post association implemented here.

## Contract

Individual publisher posts accept `realm_id` on creation and partial update. A UUID attaches or moves the post. An omitted field preserves its current value. Explicit JSON null detaches it. Empty strings, malformed IDs and duplicate fields fail validation. Direct Go callers use `Patch.RealmID` or `Patch.ClearRealm`; an ordinary marshalled nil RealmID is omitted rather than accidentally becoming a detach request.

Every write still requires the original signed post permission and current Publisher Editor role. Creating an association requires current joined Normal-or-higher membership in the target realm. Editing, publishing, moving or detaching a linked post also requires current membership in its existing realm; moves check both old and new realms. Pending invites and asserted realm headers grant nothing. Organization publishers reject an additional independent realm association.

The canonical post projection exposes the optional `realm_id`; it does not claim the complete legacy hydrated realm DTO. `GET /api/posts?realm={slug-or-id}` filters the post association and retains normal pagination, ordering and visibility. The standalone post list/draft adapter accepts the same filter. `GET /api/timeline?realm={slug-or-id}` is an explicitly owned feed extension using the same predicate and existing cursor behavior. A private, deleted or unavailable realm is not disclosed to an ineligible viewer. An organization's publisher realm alone does not place its posts into this association filter.

## Privacy and deletion

Realm visibility is an outer post gate. Public realms allow otherwise-public post reads; private realms require active joined membership. Private/draft post access additionally requires Publisher Editor membership and current realm membership. Nested reply/forward/thread projections, post lists, timeline/search, watch recipients, bookmarks, reactions and attachment download authorization share the canonical predicate.

Realm deletion leaves individual posts' realm IDs intact. There is no `ON DELETE SET NULL`, detach side effect or automatic publication. Their details, feeds, nested payloads and media grants remain unreadable. Ordinary edit/detach/move stays blocked after realm deletion. A currently authorized publisher editor can still soft-delete the post; this returns no content and retains the realm anchor. The file owner's independent media access remains valid, and attaching an explicitly public file never makes that file private.

Linked survey reads, responses and edits now check the linked post even for editors. Survey create/relink operations plan old/new post realms before publisher locks, and recheck the survey link and referenced post scope after waits. Surveys anchored to an unavailable linked post remain unavailable through these flows. Existing collection-add and interaction writes also retain realm/post scope checks; visibility is not inferred from publisher membership alone.

Every realm-linked post is excluded from federation, including posts in public realms. Actor identities remain their individual publisher identities, but linked objects and outbox entries are hidden. Attaching a previously public post changes its canonical audit revision; reconciliation changes its durable federation state to nonpublic and emits a content-free Delete activity. A pending Create/Update is cancelled by the delivery-time realm/audit recheck. The test transport is in memory; this slice performs no external delivery.

## Storage and ordering

`postpg/schema.sql` adds nullable `owned_text_posts.realm_id`, including an additive upgrade for existing tables. Install `postpg/realm_schema.sql` after the post and social schemas. It adds `owned_text_posts_realm_fk` and a realm/date/ID feed index. The backend readiness check requires the validated foreign key. The guarded runner and offline wiring checks include this exact ordering. No table or identity store is duplicated.

The optional association prelock hook receives an observed `MutationScope` containing source publisher/post IDs, current realm and deletion intent. The social provider locks the union of old, new, referenced-post and organization-publisher realms in canonical order, then the existing code locks publishers and posts in canonical order. Source and target realm/publisher ancestry are rechecked under those locks. Unexpected moves fail with conflict instead of acquiring a late realm lock. Soft deletion can lock a tombstoned explicit realm without reopening its visibility.

## Evidence and compatibility

The authored [PostActionController](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostActionController.cs) provides nullable RealmId, Normal membership checks, create association and update attach/clear behavior. The authored [PostController](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostController.cs) filters post RealmId through a realm slug. The authored [Solian realm screen](https://github.com/Solsynth/Solian/blob/ffd0838e5f04cb40bea311a39118ebeedad210c1/lib/realms/screens/realm_detail.dart#L2110) requests `PostListQuery(realm: slug)`; its pinned blob is `7e924fa9d58a68909cd3f0c18f6a003cf7f7b994`.

The retained Solian composer sends realm_id only when selected. Its unset update currently omits that field. Under this owned PATCH contract, omission preserves the association, so that caller must send explicit null to detach. This deliberate distinction is not advertised as unchanged legacy behavior. Strict realm gates, old/new membership checks, retained tombstone links, cleanup-only deletion and blanket federation exclusion are owned policies; unseen middleware, custom realm permission overrides, moderation, pinning and full realm model hydration are not reproduced.

## Validation

Seven new guarded cases cover association/null lifecycle and realm feeds; nested/media/survey/interaction privacy; revocation and actual realm-deletion ordering; a referenced post moving realms while a reply waits; opposite-direction moves and final-audit rollback; pending and previously delivered federation withdrawal; and database restart persistence. Tests use signed synthetic identities, explicit quotas/grants, independent pools, observed PostgreSQL waits, NOWAIT lock assertions and NOT VALID failure constraints. Frozen federation clocks produce the corresponding publication timestamps and synthetic deadlines.

The full Go suite passed after the code/test changes. Targeted race and vet checks and the offline migration-wiring check are recorded in the delivery manifest. PostgreSQL cases compile but require the parent's guarded disposable runner for actual SQL, concurrency and restart evidence. No local database/socket, live provider or real-user operation was used.
