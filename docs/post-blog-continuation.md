# Owned blog posts and publisher domain verification

This continuation is based on `b5221d15306dfcd8f363831be3e86d2c35cccce4`.
It adds the previously absent blog flow, preserving the existing canonical post,
publisher, media, realm, authority and scheduling boundaries. It does not establish
full legacy serializer, migration, global-filter or live-provider parity.

## Source and route evidence

`ports/sphere/blogs/source/manifest.json` pins thirteen complete source/license
inputs with revision, SHA-256, Git blob identity and byte length. Evidence includes
the licensed root-owned `PublisherVerifiedDomain` model and full
`DomainVerificationService`/`WebReaderService`, the retained post/publisher/admin
controllers, and complete authored Solian blog composer, domain screen and link
renderers. The integrity test pins the manifest itself. No excluded Shared model,
NeTo/Spec, generated SDK, canceled repository or unlicensed corpus was retrieved.

Supported routes, with the coherent host's `/sphere` aliases, are:

- `GET/POST /api/publishers/{name}/domains`
- `DELETE /api/publishers/{name}/domains/{domainId}`
- `POST /api/publishers/{name}/domains/{domainId}/recheck`
- `POST /api/posts/blog/check-permission?pub={name}`
- Existing create, update, draft publication, delete, detail, list and timeline
  search routes now support type `2` when blog support is explicitly configured.

The permission response retains `can_create`, `permission_node_granted`,
`permission_granted`, `domain_verified`, `domain`, `publisher_id` and
`publisher_name`. The node diagnostic reports active owned account/group grant
rules without superuser fallback; actual eligibility also checks the signed
session's scopes and current punishment policy. No permission is granted by this
diagnostic.

Original post-admin CRUD, lock, visibility, shadowban, realm-removal and statistics
routes were already implemented in the baseline. A separate bounded admin delta
adds the source's `publisherId`, `realmId` and `shadowbanReason` query spellings,
rejects duplicate mixed aliases, searches title/description/content using trimmed
ILIKE patterns, and sorts by publication date with creation-date fallback and a
deterministic ID tiebreaker. No duplicate statistics implementation is added.

## Authority, ownership and lifecycle

Domain reads require an active publisher editor. Changes require an active
publisher manager plus fresh `publishers.domains.manage` authority. All operations
use canonical account, realm/publisher and domain locks; provider waits hold no
SQL transaction. Initial admission records the verified session identity,
authorization epoch and domain revision, never bearer credentials.

Adding a domain persists pending state then synchronously checks its
`https://{domain}/.well-known/dyson-domains.txt`. The response records verified or
failed state. Rechecks advance a revision and invalidate prior eligibility before
the provider runs. Every result is fenced by the current authority, membership,
publisher name, exact record and revision. Removal soft-deletes the record and
cannot be undone by a late response or by re-adding the same domain.

Interrupted pending work can be recovered by `Repository.ProcessPending`, also
called by configured backend due-work processing. Recovery rechecks the originally
admitted stored session and current manager role before and after provider work.
Failed/revoked records need an explicit new recheck. A stale or replaced intent
cannot establish verification. Multiple workers may fetch the same pending proof;
only the current pending revision can apply a result.

Blog writes require `posts.create.blog`, publisher editor eligibility and an exact
verified domain. They first authorize the prospective URL, release all locks, and
retrieve a bounded preview. The final canonical post transaction repeats current
checks, matches the preview to the effective publisher and URL, and commits title,
description and a single server-produced link embed together with ordinary post
associations and audit. Metadata lives in `blog_metadata` and is projected as
`meta.embeds`. Blank title/description fields fall back to the preview; explicit
author text is retained. Updates replace the link embed, and conversion to a
non-blog type clears it. A failed media/taxonomy association rolls everything back.
Client link-embed round trips are accepted only on effective blog posts and are
replaced by the server preview. Other meaningful embed/meta inputs remain explicit
unsupported errors.

Scheduled blogs remain drafts until due, and require their saved preview, current
domain verification, original session permission and unchanged domain/policy
incarnations at publication. Blog stamp extensions are generated in PostgreSQL to
match persisted jsonb bytes; ordinary post stamps are unchanged. No preview fetch
occurs in scheduled publication. Existing current media, owner, realm, related-post
and moderation gates remain in place. When composing the independent automod delta,
apply server blog title/description/metadata before automatic moderation evaluates
the effective post.

## Configuration and deliberate differences

Apply the updated `internal/postpg/schema.sql` and new `internal/blogpg/schema.sql`
atomically after authority session continuity, account, social and post schemas. Runtime construction never
installs schema. The standalone post host remains unconfigured for blog mutation.

The coherent backend uses `--enable-blogs`; previewing composes its separately
configured owned reader. Domain verification requires the additional
`--blog-domain-verify` and `--blog-domain-origins` exact HTTPS origin allowlist.
Without a configured verifier, domain creation/recheck returns unavailable before
admission. Without a configured previewer, blog mutations return unavailable.
Constructing these adapters performs no DNS, HTTP or credential access.

The real verifier reuses the reader's public-address-pinned HTTPS transport with
normal TLS verification, no proxy/cookies/credentials, a maximum ten-second
timeout, bounded concurrency and a 64 KiB body bound. Every resolved address must
be public. All proof redirects are refused. Whole trimmed lines match publisher
names case-insensitively.

Compared with the original source, URLs are intentionally restricted to normalized
public DNS HTTPS origins, proof redirects and cross-domain preview canonicals are
rejected, provider errors are redacted, live domain records are capped at 128, and
removal retains an audit tombstone. Domain add completes its initial check inline
with durable recovery instead of starting an untracked background task. Owned
default-publisher and role semantics are retained; excluded legacy enum/base-model
serialization and migration equivalence are not claimed.

## Validation boundary

The reviewed delivery adds eighteen PostgreSQL cases, including one genuine saved-state
before/after restart assertion, plus in-process HTTP, policy, provider transport,
configuration, provenance and regression checks. The disposable runner applies
the new schema and requires all cases without skips. Provider fixtures use injected
synthetic responses; no live DNS, remote site, credential, socket or production
service was probed for this work.

The PostgreSQL cases are compiled but not executed locally because this assignment
forbids socket/database execution. Passing ordinary Go package tests is not an
actual database pass. Exact local results and integration merge requirements are
recorded with the frozen delivery; PostgreSQL/real restart and combined automod
integration remain acceptance gates.

## Independent review correction

The original five-patch artifact was verified against all 2,154 files in exact
Git base `b5221d15306dfcd8f363831be3e86d2c35cccce4`, tree
`8b64d26a5d04616d5e3a4e3303c0219557cd2ea1`; five patch and 50 output hashes matched.

Provider preparation now captures the exact verified domain revision and actor /
owner policy generation, and compares both again after final association waits.
A successful recheck, delete/re-add, owner/name ABA, membership restoration, or
transient deny insertion/removal cannot authorize an earlier preview. Pending
verification additionally captures a canonical PostgreSQL policy snapshot and
all original session-ancestry authorization generations and its earliest saved
expiry. Routine refresh may extend a session but cannot extend that saved intent.
Recovery checks these values both before provider work and after every SQL wait.

The blog migration adds monotonic per-account policy generations. Canonical
account policy, group policy, membership and session authorization changes bump
them; ordinary credential rotation and expiry extension do not. Publisher name,
type, individual ownership, realm identity, and realm ownership changes revoke
existing domain proofs and advance domain revisions. These triggers are installed
atomically, and readiness requires all eleven fences. Permission and membership
changes conservatively invalidate pending verification, preview preparation and
scheduled blogs even when the changed policy concerns another action. The user
must explicitly recheck the domain or resubmit/reschedule that work. Previously
verified domains are not tied to a continuing login session.

Six additional required PG cases exercise domain/policy/membership ABA across
previews, publisher owner/name ABA, current-owner policy, group policy and original
session revocation, expiry retention, policy changes during recovery provider work,
and restored policy at due publication. The deadline fixture deliberately advances
the saved intent deadline, as scheduling tests advance saved due times; it does
not wait on wall-clock time or issue replacement credentials. Restart additionally
retains the original policy-generation row in its snapshot. All PG providers are
synthetic; only coordinator CI may execute these database and real restart cases.
