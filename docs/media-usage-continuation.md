# Known post usage metadata

This follows the frozen post-media attachment slice. It closes the source-known `usage: 'post'` gap in the authored Solian composer while preserving private default, file ownership, canonical quotas, and attachment audience checks.

## Behavior and persistence

`owned_media_files.usage` is an ordinary descriptive label, constrained to the empty string or `post`. Existing/no-usage uploads preserve an empty storage default and the legacy nullable `usage: null` DTO. Canonical file JSON omits the empty optional field. Labeled uploads return `usage: 'post'` through direct-file, info and post-attachment projections.

Direct multipart accepts this label both before and after the file part. The parser reports validated metadata only after EOF; the repository stores it in the same transaction as measured size/SHA-256, quota conversion and ready-state publication. Invalid/duplicate/unsupported trailing metadata causes the same whole-stream rollback and cleanup as any other failed upload. No successful object is published with omitted trailing usage.

Resumable creation persists the label on its canonical pending file reservation; completion preserves it. The entire normalized create request, including usage, is already bound into the task fingerprint. Raw repository idempotency now compares usage too. Omitting usage on an otherwise repeated previously labeled request is a conflict. Existing no-usage callers continue to work.

The label does not grant reads, make a file public, select or create a pool, change quota, attach a file to a post, or imply processing. Unknown nonempty usage values remain MEDIA_FEATURE_UNSUPPORTED. Nonempty pool_id, bundle_id, hierarchy, encryption and application_type remain unsupported pending their own contracts. The unchanged full Solian/SDK upload path still depends on those requested options; no full-client parity claim is made.

## Integration

Apply `docs/media-usage-existing.patch` after the frozen post-media patch. Copy the new `ports/media/usage.go`, `ports/media/usage_test.go`, `internal/mediapg/usage.go`, `internal/mediapg/usage_test.go`, `internal/mediapg/usage_schema.sql`, and `tests/integration/media_usage_test.go` files.

Fresh installs receive the column in the canonical `internal/mediapg/schema.sql`. For an existing owned schema, explicitly execute the additive `mediapg.UsageSchema` before constructing/serving the new runtime. It is idempotent and preserves existing empty defaults. Runtime constructors and requests never migrate. Media readiness now checks the usage column, and the post-media metadata scanner reads that same canonical field.

Add these tests to the GuardedPG required-before set:

- TestPGMediaUsageDirectAtomicMetadata
- TestPGMediaUsageResumableAndPostProjection
- TestPGMediaUsageRestartPersistence
- TestPGMediaUsageAdditiveMigrationDefaults

Add TestPGMediaUsageRestartPersistence to the required-after set. The additive migration test uses a transaction-local pg_temp synthetic table and rolls back; it does not remove or alter the canonical fixture column.

## Verification

Passed locally: race-enabled unit tests for ports/media, internal/mediapg, internal/postmediapg, ports/sphere/postmedia, ports/sphere/posts and internal/postpg. The full integration package compiled with `go test -c -race`. Affected-package vet also passed. PostgreSQL execution is reserved for the parent's GuardedPG workflow; no local listener or socket was started.

Guarded tests cover both multipart metadata orders, default/null compatibility, unknown usage and pools, duplicate field rejection, no byte/quota leaks after trailing metadata or SQL publication failure, resumable fingerprint conflicts/completion, pending attachment denial, attachment-only draft/publish flow, metadata hydration, persisted privacy, and direct/resumable/default metadata across database/process restart.
