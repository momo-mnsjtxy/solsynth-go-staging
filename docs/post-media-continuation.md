# Post media attachment continuation

This slice connects canonical raw/direct/resumable file records to canonical posts. It adds no file store, identity authority, ledger, quota charge, byte copy, network provider, or background scanner.

## Supported behavior

- POST/PATCH post bodies accept `attachments`: an ordered list of at most 32 distinct canonical owned file UUIDs. Omitted/null means retain on updates; `[]` means detach all. UUID letter case is normalized; external URLs and filename suffixes are not attachment IDs.
- Creates/updates accept an empty body only when the transaction hydrates at least one active authorized attachment. Clearing the final attachment of an empty-body post fails and rolls back the entire write. The owned content constraint permits empty text; the canonical lifecycle enforces the combined invariant.
- New private-file references require file ownership. Explicitly public files may be reused by another account, but that reference grants no independent private access. An authorized publisher editor may retain or reorder a private file already attached to the same post with owner consent. Seeing a file on another post does not authorize redistributing it.
- `attachments` returns ordered, live SnCloudFile-compatible objects from the same truthful nullable projection as direct uploads. MIME type, size, SHA-256, owner, timestamps, public flag, and state come from the canonical record. Missing remote storage, MD5, processors and thumbnails remain null/false.
- Downloads preserve explicit owner/public media access. An otherwise private file can also be read through a live owner-authorized attachment to any currently visible parent post. This reuses canonical public/unlisted/private/draft, active publisher/account, friends/close-friends and block rules. No attachment mutation flips `is_public`.
- Detaching, making the last visible parent private, deleting its post, blocking a viewer, disabling a publisher or file owner, and deleting a file remove the relevant future access. Revoking a nonowner-reused file's public flag does not leave an independent grant.
- Range, HEAD and conditional downloads use the existing canonical media HTTP service. Every request is authorized before conditional handling. Existing `private, no-store`, sandbox, attachment disposition and nosniff headers remain in force.

## Transactions and bounded work

`owned_post_media` contains only `(post_id,file_id,position,owner_authorized,created_at)` and foreign keys to `owned_text_posts`/`owned_media_files`. A unique position constrained to 0–31 caps each post at 32 links. A file-first index supports audience lookup. Link replacement, post content, taxonomy/collection changes and post audits commit or roll back together through the existing post transaction.

Lock order is the canonical actor/proof and publisher/current-post locks, delegated relation and interaction locks, then file rows sorted by UUID. Content-only updates also lock retained sources. This adapter acquires no quota or other-account locks and never writes file metadata. Source readiness is checked after a file-lock wait. Projection hides deleted/deleting/pending sources and disabled source owners.

Private download audience lookup is a fresh read-only SQL snapshot inside the canonical media transaction, after its ready-file lock and active-owner check. It deliberately acquires no post locks after the file lock. A stream already opened on an authorized snapshot can finish after revocation; subsequent requests recheck authority/audience, and no cache grants a later bypass. Attachment metadata is database metadata; a missing object returns the canonical MEDIA_UNAVAILABLE on open.

## Integration

Install `postmediapg.Schema` explicitly after postpg and mediapg schemas. The postpg schema's explicit owned constraint upgrade is required for attachment-only posts. Include the new table in host readiness checks.

Compose adapters in this order:

```go
links := postmediapg.New(db, interactionRepo).WithVisibility(socialRepo)
timelineRepo.WithAssociations(links)
postRepository.WithAssociations(timelineRepo).WithVisibility(socialRepo)
mediaRepo.WithReadVisibility(links)
```

The outer timeline adapter must expose delegated capability and forward tombstones:

```go
func (r *Repository) SupportsPostAttachments() bool {
    v, ok := r.associations.(interface{ SupportsPostAttachments() bool })
    return ok && v.SupportsPostAttachments()
}
// At the existing deleted-post branch of timeline.Apply:
if p.DeletedAt != nil {
    if r.associations != nil {
        return r.associations.Apply(ctx, tx, actor, p, v, create)
    }
    return nil
}
```

This composition preserves relation handling and hydrates attachments on nested timeline projections, while retaining interactions/taxonomy. A missing attachment-capable adapter explicitly rejects nonempty attachment requests. The hook leaves the media repository's direct/resumable interfaces intact. If media storage is unavailable, hosts should not advertise working upload/download capabilities.

## Source boundary and gaps

The retained licensed authored `PostActionController.cs` at DysonNetwork revision `4ee2d0cebb9da8284a92483fee6dce795bf6947c` declares `[MaxLength(32)] List<string>? Attachments`, forwards it to create/update, and requires an attachment for a bodyless composer submission. Solian `compose_shared.dart` at `ffd0838e5f04cb40bea311a39118ebeedad210c1` sends uploaded IDs in order and allows attachment-only submissions. Retained DRIVE_API.md documents the nullable SnCloudFile fields. Provenance/hashes/licenses remain in `ports/sphere/posts/evidence-inventory.json` and `ports/media/source/manifest.json`.

This is an independently authored owned implementation, not a full reproduction of unavailable Drive internals or excluded generated Shared/external SDK model code. No excluded source or third-party assets were fetched. Source file IDs are nanoids; this provider uses its existing UUID identities. Source storage redirects, transforms/thumbnails, pool/bundle grants, E2EE, source service reference counts and thumbnail_id remain unsupported.

The authored composer also passes `usage: 'post'` and potentially `pool_id` to an excluded external uploader. In this frozen slice the existing direct/resumable adapters still reject nonempty usage/pool metadata. Therefore verified backend upload → attachment → download support is not a claim that the unchanged full Solian upload path works. A separate follow-on will persist the known 'post' usage as ordinary metadata without granting access; unknown usage and unimplemented pools must remain explicit failures.

## Verification

Passed locally: race-enabled tests for ports/sphere/posts, ports/sphere/postmedia, internal/postmediapg, internal/postpg, ports/media, and internal/mediapg. The complete tests/integration package compiled with `go test -c -race`; no PostgreSQL runtime result is claimed here and no local listener/socket was attempted.

Add these tests to the GuardedPG required-before set:

- TestPGPostMediaComposeUploadAttachDownload
- TestPGPostMediaVisibilityAndRevocation
- TestPGPostMediaAtomicOwnershipAndReuse
- TestPGPostMediaAuthorityAndFileLockOrdering
- TestPGPostMediaIndependentPoolRaceAndMissingBytes
- TestPGPostMediaRestartPersistence

Add TestPGPostMediaRestartPersistence to the required-after set. Tests use only marked disposable PostgreSQL and synthetic temporary filesystem objects. Coverage includes private direct upload → attachment-only post → projected metadata → GET/HEAD/range, empty-body detach rollback, preserved interaction tags, conditional-request revocation, audience transitions, friend/close/block rules, disabled accounts, public-media preservation, private cross-post denial, same-post editor retention, public-source revocation, atomic post/taxonomy rollback, independent pools, policy/file-lock waiters, deletion races, missing bytes and process/database restart persistence.
