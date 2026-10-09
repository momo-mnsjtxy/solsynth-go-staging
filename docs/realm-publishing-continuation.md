# Organization publishers and canonical realm privacy

The separate individual post association is added by the later [post realm continuation](post-realm-association-continuation.md). That delta replaces only the independent post `realm_id` gap described below.

This slice extends verified base `726485cc2668b3ad9e2b5aff9e715ec9c182c5a0`. It adds the organization publisher flow over existing canonical publisher and realm tables. It does not add a second identity store or require a schema migration.

## Supported flow

`POST /api/publishers/organization/{realmSlug}` accepts the existing publisher name/nick/bio request. It requires the signed `publishers.create` grant, current joined realm Moderator or Owner, and available publisher quota. The creator receives a separate joined Publisher Owner membership; other realm members gain no publisher editing rights. Owned quota counts individual publishers and organizations with that account's explicit Publisher Owner membership. Names remain reserved after deletion.

The existing publisher update/delete and invite/accept/member-role routes work for organizations. Publisher membership writes lock and recheck the realm; new invitations and acceptance require the recipient's current realm membership. A retained publisher role becomes ineffective when realm membership ends. Realm removal does not automatically assign or transfer publisher ownership.

Organization posts support public, unlisted and private visibility; draft/create/edit/publish/delete; existing reply/forward/chain projection; media attachments; reactions/bookmarks; follows; timeline/search and surveys. The existing friends/close-friends modes remain unsupported for organizations because they rely on an individual publisher account's relationship graph. Collections and organization sticker management remain unsupported.

## Read boundary

Realm visibility is an outer gate for publisher metadata and content. An active public realm permits ordinary public reads. An active private realm requires the viewer's current joined realm membership. Pending invitations, historical membership, publisher membership alone and identity headers do not satisfy this gate. A deleted realm or disabled realm owner suppresses its organizations. Private/draft posts additionally require current Publisher Editor membership and current realm membership, including when the realm is public.

The application installs `socialpg` as the optional canonical realm provider. Account HTTP uses an additive actor-scoped publisher reader for details, managed/account publisher lists and member lists. Old unscoped methods represent anonymous access. Standalone repositories without the provider continue to exclude organizations.

The central post predicate is reused by timeline/search, nested reply/forward/thread projections, interactions and the attachment download adapter. A public individual reply can remain visible while its private realm target ID and nested payload are redacted. Following an organization never grants realm membership; a departed member's private subscriptions disappear, while unsubscribe cleanup remains available. Survey introductions, linked-post checks, answers and results use the same outer publisher gate. Imported organization sticker packs are hidden from nonmembers, including list/search/identifier projections.

Private media remains available through an owner-authorized attachment only while the parent post is currently visible. The file owner's independent access and an explicitly public file's independent public access are unchanged. No file is silently made public or private by attaching it.

Organization federation remains rejected by the existing explicit individual-only actor resolver. Actor, object, outbox and durable reconciliation tests exercise this boundary even if an imported organization row has federation enabled. Developer/app enrollment and public projections, and publisher-owned taxonomy, now reject/exclude organizations until those integrations have their own realm-aware contracts. Ordinary global unowned tags remain available; an imported organization-owned tag cannot expose its publisher through global taxonomy or a post tag projection.

## Transaction ordering

Writes retain actor/proof locks, then acquire all relevant realms in UUID order, all relevant publishers in UUID order, then posts and associations. The existing audited publisher lock modes remain UPDATE for the source and SHARE for referenced publishers. Realm locks precede publisher locks consistently with realm deletion. Source selectors, publisher-to-realm ancestry and post-to-publisher ancestry are rechecked after waits; unexpected ancestry changes fail instead of acquiring a late lock. Current realm membership and target visibility are checked again under the held locks. An audit failure rolls back organization/owner creation or post/relation/taxonomy mutations together.

## Authored evidence and owned choices

The retained [PublisherController](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherController.cs#L502) provides the organization route, permission and Moderator membership check. [PublisherService](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Publisher/PublisherService.cs#L260) creates an organization with `RealmId` and a separate Publisher Owner member. [RealmService](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Realm/RealmService.cs) defines active membership by joined/not-left state. Pinned service blob hashes and the retained controller/caller paths are recorded in `realm-publishing-source-evidence.json`.

Private realm gating of every organization content path, current realm membership for publisher editing, the creator quota rule and fail-closed unsupported integrations are explicit owned policies. They do not claim reproduction of unseen legacy middleware or full model serialization. Publisher output retains the existing bounded projection with `realm_id`; it does not hydrate the full legacy realm entity.

Individual posts carrying an independent `realm_id` are a separate source-evidenced feature in PostActionController and the authored Solian compose settings. That association remains unsupported here, as do realm post moderation/custom per-role permissions, organization collections/sticker creation, organization ActivityPub actors and app/taxonomy ownership.

## Validation

Seven guarded PostgreSQL cases are added in `tests/integration/realm_publishing_test.go`: creation/roles/quota/metadata and audit rollback; post/timeline/nested privacy; media/survey/sticker/federation privacy; lock-wait rechecks; opposite-direction realm replies and late audit rollback; restart persistence; unsupported public integrations. They use signed synthetic identities, explicit quotas/grants, independent SQL pools, observed lock waits and NOWAIT checks. Failure constraints are NOT VALID so prior fixture rows do not invalidate setup. Synthetic deadlines and federation eligibility use the fixture clock.

Full `go test ./...` and focused race checks passed locally; PostgreSQL cases compile and skip without the guarded disposable DSN. Actual SQL, concurrency and restart execution remains required in the parent's integration runner. No local database/socket, live provider, real-user action or third-party asset was used.
