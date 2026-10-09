# Resumable media upload continuation

This independently authored extension uses the retained, licensed
`ports/media/source/DRIVE_API.md` and `FILE_UPLOAD_API_CHANGES.md`, plus the
already retained authored `drive_wiring.dart`. No excluded SDK, generated model,
protocol repository or remote storage implementation was consulted.

## Supported contract

The same media service and canonical host now handle:

- `POST /api/files/upload/create`
- `POST /api/files/upload/chunk/{taskId}/{chunkIndex}`
- `POST /api/files/upload/complete/{taskId}`
- `GET /api/files/upload/progress/{taskId}`
- `GET /api/files/upload/resume/{taskId}`
- `GET /api/files/upload/tasks`
- `GET /api/files/upload/tasks/recent`
- `DELETE /api/files/upload/{taskId}`

The newer upload-change document explicitly supersedes the older complete
response: completion returns 202 with `message`, camel-case `taskId`, and
`status: "processing"`. Clients poll progress until `Completed`; `file_id` is
an additive field naming the existing owner-protected file/open/info resource.
The task and file share one opaque server-generated UUID. Completion replays
also return 202; progress remains the authoritative result.

Create always returns `file_exists: false`. MD5 is the documented whole-file
transport checksum, not a deduplication or authorization primitive. SHA-256 is
calculated for every chunk and the final object. Optional `Idempotency-Key` on
create is an owned extension bound to the complete normalized request. Without
it, each create request creates a separate task. Same-byte chunk and complete
retries are safe; changed bytes/request meaning produce a conflict. Replaying a
chunk after completion validates its bytes without recreating staging files.

All files stay private. Permissions use the existing `media.files.create` and
`media.files.read` rules and fresh same-pool transaction authority. Cancel
requires create permission and cannot delete a completed file. Existing file
deletion remains the separate `media.files.delete` operation.

## Persistence and recovery

Install `internal/mediapg/resumable_schema.sql` explicitly after the existing
media schema. `ResumableSchema` embeds the same SQL. It adds only task and chunk
metadata; no credentials or serialized authentication proofs are stored. Startup
readiness checks these tables for a chunk-capable store and does not run DDL.

Creation inserts the task and existing file reservation in one transaction using
the shared `reserveTx` helper. Lock order stays canonical account, quota, file,
then task. Every chunk must match its exact expected length and valid index.
The default chunk size is 5 MiB, the maximum is 20 MiB, and each task has at most
10,000 chunks. At most 100 pending tasks per account prevent unbounded live task
creation independent of the existing byte quota. Positive file size and the
existing 255-byte safe filename/MIME limits apply. These are owned bounds, not
claims of upstream pool-policy parity.

Chunk bodies stream to private descriptor-confined files, with durable atomic
publication, no symlink following, and no overwrite. Multipart framing and any
extra part are checked before a chunk can commit. Partial and orphaned writes
remain bounded by their task reservation; an identical retry can adopt a chunk
whose SQL commit was interrupted. On-disk chunk bytes are rehashed when merged.

Completion has at most four workers per repository instance. The SQL Processing
state is durable; the worker verifies authority again before publication. After
process loss, a fresh authorized complete, progress or resume request starts the
pending work again. Read-only progress access never authorizes a worker. A full
worker queue similarly relies on subsequent polling to dispatch pending work;
there is no autonomous credential-bearing queue or claim of automatic restart
without the owner's new request.

The object is fsynced and chunk staging removed before a single transaction
moves quota from reserved to used and exposes the ready file. A verified final
object left by an interrupted metadata commit can finish without its removed
chunks. Uncertain successful commits are never deleted by failure cleanup.
MD5/integrity or authorization failure hides the still-pending task, then reuses
the existing replayable cleanup. Cancellation and expiry use that same object
deletion/quota-release path. Transient storage/SQL failures retain Processing
and its reservation for retry or expiry. Existing `Cleanup` removes expired
chunks and abandoned parts before releasing quota.

Quota counts logical file bytes. During assembly, chunks and a final staging
object can temporarily use twice the reserved bytes, plus filesystem/metadata
overhead. Operators must provision this bounded working space. Completed and
canceled task metadata is retained for retry meaning; retention/archival policy
remains future work.

## Limits and verification

Progress uses 0–50% for received chunks and 100% only for committed local files.
It never invents S3 optimization/upload phases. No remote upload, cross-account
hash deduplication, thumbnail/metadata processing, pools, bundles, folders,
hierarchy, E2EE, expiry policy, usage/application metadata, task stats/details or
bulk failed-task cleanup API is claimed. Unsupported option values and routes
fail explicitly. The existing file projection still has nullable MD5/remote
fields and is not a full shared-model serializer.

Passed locally: focused media unit tests with the race detector, including
strict multipart/JSON handling, replay conflicts, boundaries, real filesystem
restart/recovery, integrity failures and unsafe filesystem entries. Integration
tests compile and skip without the guarded disposable DSN. No sockets, external
providers or real credentials were used; no actual PostgreSQL pass is claimed.

Require these seven tests in the disposable CI before-phase manifest:

- `TestPGMediaResumableLifecycleAndIsolation`
- `TestPGMediaResumableQuotaAndIdempotencyConcurrency`
- `TestPGMediaResumableBoundsHashFailureAndCleanup`
- `TestPGMediaResumablePublicationRecovery`
- `TestPGMediaResumableExpiryCancellationAndFreshAuthority`
- `TestPGMediaResumableHTTPMultipartAndAsyncContract`
- `TestPGMediaResumableRestartPersistence`

Require `TestPGMediaResumableRestartPersistence` after the database restart too.
It persists only inert actor/task identifiers alongside actual chunk files,
issues fresh synthetic proof after restart, finishes a partial upload and
restarts a previously accepted Processing task, then checks bytes and quota.
