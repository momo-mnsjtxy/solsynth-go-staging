# Authorized media and filesystem storage

The new module implements real bounded upload, owner/private and public download,
metadata lookup, owner deletion, quotas and crash recovery. PostgreSQL metadata
references canonical `owned_authority_accounts`; there is no independent account
directory or header-based authority. Object bytes live in one explicitly supplied
private local directory. No S3, cloud credentials, network provider, signing
endpoint, database seed or startup migration is invented.

## Runtime wiring

1. Install `internal/mediapg/schema.sql` after `internal/authoritypg/schema.sql`.
   Provision `owned_media_quotas(account_id,limit_bytes)` explicitly; absent quota
   denies upload. There are no default grants, billing tiers or automatic accounts.
2. Provision a dedicated absolute, non-symlink directory with mode 0700. Construct
   `mediastore.NewFilesystem(root,maxBytes)` and close it on host shutdown. Linux
   is supported; other OS constructors fail with `MEDIA_FEATURE_UNSUPPORTED`.
3. Construct `mediapg.New(db,store,guard,Config{MaxFileBytes:maxBytes,
   PendingTTL:time.Hour})` using the same DB pool as canonical authority.
4. Use `(&media.Service{Repository:repo,Authority:proofIdentity{identity},
   MaxFileBytes:maxBytes}).Handler()`, wrapped by `bindIdentity` then
   `bindTransactionAuthority`. Separate authority pools/non-atomic fallback are
   intentionally unavailable for this module.
5. Route `/api/media/*` and `/api/files/*`, plus `/drive/*` rewritten to `/api/*`.
   `repo.Ready` checks the root and required relation columns without mutation.
   Invoke `repo.Cleanup(ctx,limit)` from the host's existing explicit worker;
   it starts no scheduler itself. Keep bounded request-read deadlines in the host.

Replicas must use the same DB and the same backing filesystem namespace. This
adapter does not synchronize private disks between machines. The root must only
contain this module's objects and staging files and must not be served directly
by an external static-file handler. Filesystem permissions and storage durability
remain deployment responsibilities. Construction never creates or changes roots.

## Owned streaming API

- `POST /api/media/files`: raw bytes, mandatory nonnegative Content-Length,
  Content-Type without parameters, X-File-Name, lowercase X-Content-SHA256 and
  Idempotency-Key (1–128 printable ASCII characters). Optional X-File-Public is
  exactly `true` or `false`; default false. Success 201, same-request replay 200
  with Idempotency-Replayed, conflicting key 409, active upload 409, deleted key
  410. Keys belong to the canonical account. Public/read state is immutable.
- `GET|HEAD /api/media/files/{uuid}` streams bytes and supports standard range,
  conditional and HEAD behavior. `GET|HEAD .../{uuid}/info` returns metadata.
- `DELETE /api/media/files/{uuid}` removes owner bytes and returns deleted
  metadata. Retry is idempotent. A cleanup failure returns 503 and remains durable.
- `GET /api/media/quota` returns configured limit, used and reserved byte counts.

Named owned permissions are `media.files.create`, `.read` and `.delete`.
Request writes check an opaque verified proof while holding the canonical account
lock inside the domain SQL transaction. Final upload authorization is rechecked
after streaming so wall-clock expiry also fails closed. A raw account UUID never
grants authority. Public downloads need no identity; a supplied invalid bearer
never falls back to anonymous. Private and deleting files appear absent to others.
Disabled owners' public files are also hidden.

All downloads use attachment disposition, `nosniff`, a sandbox CSP and no-store
caching. Client MIME overrides, thumbnails and transformations are unsupported.
Names never become storage keys. UUID-only keys, descriptor-rooted operations,
no-follow opens, exclusive staging, regular-file checks, hard-link checks and
private permissions bound filesystem access. These guarantees assume the
configured directory remains private to the service's trusted OS account.

## Existing documented Drive routes

- `POST /api/files/upload/direct`: bounded multipart with one `file` part,
  maximum 20 MiB or the lower configured maximum. It streams directly into the
  durable lifecycle. Content-Length is required and the envelope is capped at
  max file bytes plus 64 KiB. It reserves a conservative bounded request size,
  then charges only measured file bytes. Near quota, framing may cause a refusal
  even when exact bytes fit; raw upload uses exact byte reservation.
- `GET|HEAD /api/files/{uuid}[.extension]`, `.../{uuid}/info` and
  `DELETE /api/files/{uuid}` use the same storage and authorization paths.
- Direct upload and legacy info/delete return documented fields with truthful
  nulls/false values: no remote uploaded_at, no MD5 hash, no thumbnail/compression,
  no storage URL, folder, bundle or processing claim. SHA-256 is an added field.

Direct multipart defaults private. Only an empty legacy option or matching
content_type is accepted alongside the file. Nonempty pool_id, bundle_id,
encryption fields, expiry, hierarchy, usage and application_type are 501, including
when supplied after file bytes; the upload then rolls back and cleans its bytes.
Multiple file parts fail. Raw upload provides durable client idempotency; legacy
direct upload has no declared body hash/key contract and is not deduplicated.

Chunked/resumable upload, asynchronous processing, S3/direct-provider upload,
share links/passcodes, E2EE metadata, folder/bundle/permission sharing, metadata
editing, pool APIs, quota purchase/tier billing, media extraction/transcoding and
full Solian UI acceptance remain unimplemented. Unsupported matched file routes
return 501. Unmatched routes remain 404. The document-level compatibility above
does not establish full frontend or provider parity. See
`ports/media/PROVENANCE.md` for exact permitted evidence and exclusions.

## Durable state and quota invariants

A reservation transaction locks canonical authority and quota, inserts a pending
row and reserves bytes before touching object storage. The streaming transaction
again locks authority, quota and file. The adapter uses a fixed 64 KiB buffer,
checks exact raw length/digest or measured multipart bounds, fsyncs bytes, publishes
without overwrite and fsyncs the directory. Only then does one SQL transaction
mark ready and move reserved bytes to used bytes. Readers only see ready metadata.

This is a recoverable two-resource protocol, not a distributed ACID transaction.
A crash may leave pending rows and staged/published objects, but never reports a
ready upload before durable bytes. Ordinary failures conditionally move only
still-pending rows to deleting and attempt bounded cleanup. Uncertain successful
commits remain ready and are never removed by that conditional failure path.
Pending rows abandoned by process loss become eligible after PendingTTL.

Delete first commits a hidden deleting state while retaining its quota charge.
Cleanup locks account, quota and file in the same order; it removes final/staged
objects and then atomically releases quota and marks the row deleted. Errors or
crashes retain the charge and replayable row. Concurrent cleanup cannot release
quota twice. Tombstones preserve request-key meaning, so a deleted upload cannot
be resurrected by retry. Tombstone archival is a future explicit retention policy.
An already-authorized in-flight download can finish using its open file descriptor
after unlink; quota measures linked storage objects, not open descriptor lifetime.

## Verification and CI additions

Passed locally: `go test -race ./ports/media ./internal/mediapg
./internal/mediastore`, including HTTP/range/HEAD/identity-channel rules, multipart
trailing-field rejection, size/digest/cancellation, real temporary filesystem
roundtrip/reopen, isolation and concurrent publication. No network sockets were
started. Eight real PostgreSQL tests compile and explicitly skip without the
existing guarded disposable runner; this is not a PostgreSQL pass.

Add the media schema install to the disposable runner before domain tests. Add
these exact cases to its required before-phase manifest:

- TestPGMediaLifecycleAndIsolation
- TestPGMediaQuotaAndIdempotencyConcurrency
- TestPGMediaRollbackCleanupAndRecovery
- TestPGMediaHTTPDirectAndRange
- TestPGMediaFreshAuthorityAndCancellation
- TestPGMediaAuthoritySerialization
- TestPGMediaMetadataFailureAfterObjectWrite
- TestPGMediaRestartPersistence

Add TestPGMediaRestartPersistence to the after-phase manifest. It writes an inert
file descriptor JSON and actual bytes under the marked disposable run directory
before PostgreSQL restart, then verifies bytes and quota from a new pool/adapter
after restart. Other cases cover canonical proof rejection, policy lock ordering,
concurrent quotas/idempotency/cleanup, interrupted staging, injected storage
failure and SQL rollback after object publication. Tests never discover an
arbitrary DSN, seed persistent accounts, contact cloud storage or start listeners.
