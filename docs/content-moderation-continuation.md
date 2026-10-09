# Canonical content moderation continuation

This slice is based on `31710ae1907017446a7c1b5c836cc202a60aa4be` plus the frozen content-utilities overlay (`eee6722e716df3b3897f4362985218178de81002d2115f698cc598b30f5e12f1`). It adds source-backed post, publisher and realm moderation over the existing canonical identities. Host routing, CLI workers and migration orchestration remain integration-owned. No production moderation, live provider calls or local PostgreSQL process was used.

## Source evidence and owned policy

The pinned authored [PostAdminController](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostAdminController.cs), [PublisherAdminController](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherAdminController.cs) and [RealmAdminController](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmAdminController.cs) evidence the implemented route family. The authored [permission registry](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/docs/PERMISSIONS.md) proves the exact named permission strings. Full authored services and API/realm-permission documentation were reviewed; hashes are in `content-moderation-source-evidence.json`. Excluded model/protocol definitions were not read or inferred as legacy parity.

Global administration is an explicit owned policy: current canonical account `Superuser` plus the exact named permission and original fresh transaction proof. Explicit scope restrictions, denials, expiry and punishments still apply. Publisher ownership, realm roles and `posts.moderate` alone never authorize an admin route. This does not reproduce unseen upstream global middleware. Global status grants no bypass on ordinary post, publisher, realm, media or survey reads.

Shadowban is deliberately a stronger owned read policy: ordinary discovery and detail reads suppress the post or publisher. Current publisher Editor or higher retains management reads only while the publisher's realm and any separate post realm also permit current membership. A public realm does not replace membership for that exception. Public taxonomy projections remain strictly public, including for editors. Independent media grants/public-file state remain governed by the existing media repository; moderation removes access conferred solely by a hidden post.

## Implemented routes

All `/api/admin/...` paths require current global administration in addition to:

| Routes | Permission |
|---|---|
| GET `/api/admin/posts`, GET `/{id}`, GET `/{id}/lock`; POST `/{id}/visibility`; POST/DELETE `/{id}/shadowban`; POST `/{id}/realm/remove` | `posts.moderate` |
| POST/DELETE `/api/admin/posts/{id}/lock`; POST `/{id}/lock/batch`; DELETE `/lock/batch` | `posts.lock` |
| DELETE `/api/admin/posts/{id}` | `posts.delete` |
| GET `/api/admin/publishers`, GET `/{name}`; POST/DELETE `/{name}/shadowban` | `publishers.moderate` |
| PATCH `/api/admin/publishers/{name}` (`name`, `nick`, `bio`) | `publishers.update` |
| DELETE `/api/admin/publishers/{name}` | `publishers.delete` |
| GET `/api/admin/realms`, GET `/{slug}`, GET `/{slug}/members` | `realms.moderate` |
| PATCH `/api/admin/realms/{slug}` (`slug`, `name`, `description`, `is_public`, `is_community`, `account_id`) | `realms.update` |
| PATCH `/api/admin/realms/{slug}/members/{accountId}/role`; DELETE that member | `realms.members.manage` |
| DELETE `/api/admin/realms/{slug}` | `realms.delete` |

POST `/api/posts/{id}/realm/moderate` remains separately scoped: current joined Moderator/Owner of the associated realm, `posts.moderate`, and the current ordinary post read gate. It does not require Superuser or confer publisher editing. It sets the post private, detaches it, durably records the original realm and optional reason, and forbids reattachment to that same realm. This follows authored `PostService.RemovePostFromRealmAsync` and the authored post API's removal rule. An account-owned publisher receives a durable local notification intent; organizational publishers have no invented account recipient.

Lists expose `X-Total`, default to 50 and cap at 200, with bounded offset and literal case-insensitive text search. Source-backed snake_case filters are accepted where implemented. UUID-shaped publisher/realm selectors resolve IDs only; other names remain case-insensitive. Lists and projections use canonical owned DTOs, not unseen full legacy models. Post visibility accepts the documented names or existing canonical integers (public 0, friends 1, unlisted 2, private 3, close_friends 4). Shadowban accepts documented `spam`, `advertising`, `harassment`, `hate_speech`, `misinformation`, `illegal`, `other`; DELETE clears it and the admin projection reports `none`.

Batch lock/unlock is an owned atomic contract: 1–100 unique post IDs, sorted scopes, and full rollback on any missing target or invalid transition. Post locks continue to reject ordinary author edits; authorized admin transitions can change a locked post. All deletes are canonical tombstones. Deleting a realm preserves individual post realm links, so those posts never become public; canonical publisher-authorized cleanup still works.

Owner transfer requires an active joined target, demotes the former owner to Normal, promotes the new owner and changes `realm.account_id` in one transaction. The last owner cannot be removed/demoted without transfer. Moderator promotion retains the canonical boost-level-2 requirement. Realm ownership never changes publisher memberships. Renaming a federation-registered publisher is rejected, even when federation is disabled, until an actor-URI migration exists.

## Transaction and propagation boundaries

Authority-only nonlocking preflight precedes domain observations. Mutations then lock sorted canonical account IDs, sorted realm IDs, sorted publisher IDs, and sorted post IDs, retaining locks through audit commit. Source ancestry/selector identity and current authority are rechecked after waits. Scoped role checks and owner-transfer membership checks happen after realm locking. Canonical post/account/social audit rows and the new moderation audit commit together.

`accountpg.PublisherModerationPredicate`, the social realm provider, and `postpg.DetailPredicate`/`ListingPredicate` propagate current gates through publisher/member reads, timeline/nested relations, follows, post-derived media authorization, surveys, sticker metadata and autocomplete. Public collection/tag/developer/app/widget projections add narrow exclusions. App credential and owner-management behavior is otherwise unchanged. The live-stream and rewind integrations can consume the same canonical reader helpers without a second moderation identity.

Federation actor/object/outbox paths exclude moderated content. Reconciliation uses durable canonical state plus existing federation post state to queue a content-free Tombstone for already-public content. A separate cleanup-only identity lookup permits that Delete after publisher moderation/deletion or owner disable; it cannot project an actor or Note and rejects organizations. Queue processing rechecks current moderation under canonical locks, cancels stale Create/Update, retries the same Delete after hidden content edits, and cancels a Delete when the object is public again. Remote erasure is not claimed. Realm-linked and organization federation remain unsupported.

## Required integration

1. Apply additive account/post schema migrations (nullable `shadowban_reason`, `shadowbanned_at`), then social + post-realm schema, then `moderationpg.Schema`. The post-realm migration adds `owned_realm_post_moderation`; moderation adds `owned_content_moderation_audit` and `owned_content_moderation_notifications`. Existing identity, membership, post, federation and notification tables are reused. No data import or startup migration is added.
2. In the integration-owned `owned_taxonomy.posts` view, retain existing active filters and add `p.realm_id IS NULL AND p.shadowbanned_at IS NULL AND u.shadowbanned_at IS NULL`. The new metadata regression intentionally requires this agreed host seam. Updating an existing installation requires replacing that view as part of its explicit migration, not only changing initial schema text.
3. Construct `moderationpg.New(db, guard, moderationpg.Config{PostAssociations: timelineRepo})` with the same pool/guard and outer timeline association adapter as the canonical post repository; call `Ready`. Preserve the configured clock when testing.
4. Wrap `content.NewHandler(repo, postIdentity{identity})` in existing verified identity / transaction binding. Route only when `content.Handles(path)` is true, before generic post handling. It intentionally excludes taxonomy admin prefixes. No header supplies global authority or membership.
5. Explicitly run `ProcessNotifications(ctx, canonicalNotificationRepo, configuredApp, 1..100)` followed by the existing local notification processor. The first projection persists the app binding; retries retain the same durable source key/body and `External=false`. Disabled recipients are dropped. No automatic goroutine, push, mail or external adapter is started.
6. Continue existing explicit federation reconciliation/delivery workers. Register the ten `TestPGContentModeration...` cases in the before phase and `TestPGContentModerationRestartPersistence` in both phases. Shared host/harness files are absent from this patch.

## Verification and remaining boundaries

Local `go test ./...` and `go vet ./...` pass. Focused race tests and final exact-patch checks are recorded in the delivery manifest. Ten new PostgreSQL cases compile and explicitly skip locally; only the guarded parent runner can establish actual SQL, race and restart results. They exercise global denial/isolation, metadata/nested/media/survey propagation, atomic rollback, scoped removal/notification idempotency, final-owner invariants, account/realm wait rechecks, opposite-publisher batches, content-free federation retry and restart persistence. Fixtures use synthetic identities, current fixture clocks and explicit realm boost grants; no real credentials or external calls.

Verification marks, custom realm role/user permission overrides, aggregate moderation statistics, automod, external classification and unseen full model hydration remain unimplemented. Known verification routes and unsupported follow-moderation fields explicitly return unavailable/unsupported rather than claim success. Root admin post inspection does not bypass nested-object or media-byte authorization. Moderation state is exposed through admin projections; ordinary post DTOs are unchanged. No claim is made that the new internal policy reproduces unknown legacy authorization internals.

## Integrated host candidate

The host now dispatches the evidenced Sphere post/publisher admin routes and Passport realm routes, plus combined `/api` forms, ahead of generic post/account handling. Scoped realm removal uses the same fresh verified identity and transaction proof. Readiness checks the moderation extension; the explicit bounded lifecycle worker projects only local notification intents before the existing inbox delivery worker. The public taxonomy view includes both post and publisher shadowban predicates and the earlier realm-link exclusion. Eleven required PG cases include composed anonymous/author/global-admin read isolation, shadowban/restore, gateway dispatch and current Superuser revocation. SQL/process execution is pending on this exact candidate.
