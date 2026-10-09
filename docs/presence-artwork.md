# Presence artwork over canonical media

This bounded slice implements the authored `POST /api/presence/artworks` multipart
upload and `GET /api/presence/artworks/{hash}` byte retrieval. The complete pinned
Passport controller, service, authored artwork model and startup files, together
with the repository license, are retained in `ports/passport/artwork/source/`.
Their manifest records immutable Git blobs and SHA-256 digests. No Shared,
generated binding, excluded repository, provider credential or live account is
used. Imported permission constants are implemented as the explicit owned
`presences.artwork.manage` permission; this is not a claim about excluded enum or
constant definitions.

The multipart field is `file` (ASP.NET-style case-insensitive matching also accepts
`File`). The complete request is bounded to one MiB, and only one nonempty image
part is accepted. Response fields are `hash`, `mime_type`, `size`, and `url`, with
the authored `sha256:` prefix and lower-case digest. GET also accepts a bare or
upper-case digest, supports ranges and HEAD, and never renews retention merely
because a viewer downloaded a file. Image responses carry nosniff, a restrictive
CSP sandbox, and no-store so current removal and authorization remain observable.

Storage uses existing owned media objects, quotas, account proof and transaction
guards. Upload requires both `presences.artwork.manage` and `media.files.create`.
An authenticated download must also satisfy the existing canonical
`media.files.read` policy. Anonymous hash reads are an explicit owned public
artwork policy, consistent with the authored GET and standard Passport
authorization registration. Invalid supplied or bound credentials never become
anonymous access.

The backing canonical file remains private. The artwork route checks a current
unexpired hash-to-file association after acquiring the file lock, verifies its
digest metadata and image MIME type, and respects current file deletion and
owner disable state. Owner policy is re-read after object-store waits for both
reads and dedup renewal. A hash response conveys neither a canonical file ID nor ownership. A
viewer cannot attach a hash or foreign file through this endpoint: every upload
requires the complete bytes. Dedup reuses the original uploader's file and quota
charge; it does not transfer ownership, set the file's public flag, or charge a
second uploader. An inactive owner's hash cannot be taken over by another
account. After canonical deletion, uploading the bytes again creates a new owned
file and association.

The source default retention of 30 days is persisted per association. A repeat
full upload renews that timestamp. This slice does not yet implement activity
objects or their reference-touch calls, so no activity endpoint is claimed.
`Backend.CleanupMedia` invokes bounded artwork expiry before the existing durable
media cleanup. Expiry detaches the hash and marks the backing file for deletion;
bytes are removed before quota is released. Interrupted uploads and deletion
failures remain in the existing durable media lifecycle for retry. Constructors
and readiness never migrate or schedule work.

Install `internal/mediapg/artwork_schema.sql` explicitly after the base media
schema. Readiness checks the new relation when media is enabled. Lock order is
canonical actor, hash advisory lock, quota, file for upload/expiry. Reads take
their existing canonical viewer/file locks and query association policy without
additional write locks. Credentials and retention are checked again after waits
and object reads; timestamps come from the database wall clock. Hash reservation
and quota reservation commit together before object I/O. Publication performs a
fresh check of both upload permissions and retains the canonical failure cleanup.

Socket-free host tests cover real proof binding, multipart bounds, duplicate
parts, rejected hash-only JSON, public and authenticated reads, ranges, HEAD,
invalid credentials and disabled viewers. Five guarded PostgreSQL tests cover
composition, dedup/ownership/deletion, expiry/renewal/quota recovery, concurrent
upload and rollback, current policy after database/storage waits, upload grant
expiry after byte persistence, and process restart. Fixture images have unique
content to prevent cross-test global dedup. The restart test uses the shared
phase and marker helpers and reads the pre-restart file, owner and quota without
seeding new state in the after phase. Those
tests require the parent's disposable PostgreSQL runner; local package success
with skipped PG tests is not database validation.

## Affiliation research boundary

Complete authored `AffiliationSpellService`, purchase options and both Solian
affiliation screens were successfully read at their pinned licensed revisions;
complete bytes, licenses and Git/SHA-256 manifests are retained in
`ports/passport/affiliation/source/`. Generic tracking results store the supplied
resource identifier. Registration-invite consumption separately checks expiry
and `max_usages`, records `account:{accountId}` attribution, and marks a
single-use invitation as affected. Paid fulfillment matches the purchase and
order, creates a one-use invitation for the purchaser, and records fulfillment.
No referral payout or XP reward is defined. The controller and callers depend on an excluded Shared/SDK model for
the wire spell type/default and `isRegistrationInvite` interpretation; these
numeric meanings were not invented here. Activation also needs an explicit
canonical authority policy and a verified-contact requirement, beyond merely
recording an affiliation result or a pending activation request. No affiliation
route, financial effect or activation success is claimed by this artwork patch.
