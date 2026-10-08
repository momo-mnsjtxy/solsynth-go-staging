# Placeholder transition projection

This source-informed AGPL-3.0-only port implements actual in-memory domain
transitions over explicit snapshots. It is not a messaging service, database
adapter or complete wire contract. Modified 2026-10-07.

## Evidence

Only the pinned, licensed local `source/ChatService.cs` at DysonNetwork revision
`4ee2d0cebb9da8284a92483fee6dce795bf6947c` was used for implementation:

- Lines 2794–2868: 60-minute TTL, kind validation, prior-placeholder finalization
  versus deletion, and initial streaming/uploading state.
- Lines 2875–2912: concatenated streaming chunks, clamped upload progress,
  refreshed expiration and deleted-record rejection.
- Lines 2918–2961: final text resolution, nil-versus-empty attachment replacement,
  removing placeholder keys before merging extra metadata, and empty-map cleanup.
- Lines 3008–3045: unconditional explicit expiration and inclusive millisecond
  stale selection for active placeholder records.

The source SHA-256 is
`75a18068893f878c7a5d24529d88415e632f0947894ceea971620ebf1558fb9a`,
also recorded in `source/manifest.json`. License text is `source/LICENSE.txt`.
No upstream requests, production probes, NeTo/Spec definitions, descriptors or
shared generated types were used.

## Deliberately explicit boundary

`ResolvedPlaceholder` is an internal projection supplied by a future adapter.
The shared `ChatMessageHelpers` implementation and its wire constants are not
available in the permitted evidence. Consequently enum values are internal;
metadata decoding, malformed-input fallback and placeholder recognition are
unimplemented. A nonnil projection is the caller's resolved recognition result,
not evidence of authorization. Send and stale selection additionally enforce
`Type == "placeholder"`, as the licensed source queries explicitly do.

`OtherMeta` represents metadata apart from the four projected placeholder keys.
Finalize removes those keys defensively, then merges caller-provided extra keys;
reintroduction of a removed key is intentionally permitted by the source order.
Opaque attachment and metadata bytes are copied without interpreting any shared
contract or silently manufacturing cloud-file data. No HTTP JSON DTO is exposed.

Send consumes at most one caller-selected matching live record, matching the
source's FirstOrDefault selection. It returns the changed previous record and
an unsaved new record. This does not enforce uniqueness under concurrency or
model the source's separate commits and partial-failure behavior. Input values,
pointers, metadata bytes and attachments are not mutated or aliased in results.

Updates can revive an elapsed TTL before the expiration job deletes the record.
Unknown projected kinds still refresh TTL. IEEE NaN survives progress clamping,
as in the source; downstream JSON serialization policy remains unresolved.
Explicit expiration has no type/deleted guard and can replace a deletion time,
as in the source. Time conversion uses caller-supplied `time.Time` and Unix
milliseconds; matching NodaTime's representable-date limits is not established.

## Remaining integration work

No persistence, transaction handling, audit hooks, ID/sequence allocation,
metadata hydration, room/member lookup, HTTP routes, authorization, scheduler,
WebSocket/gRPC delivery, queues, link-preview processing or sync-message
persistence is implemented. Finalize returns the pre-save text snapshot; source
post-save audit times, sync events and escaped display text remain outside this
projection. The caller must execute those independently before claiming full
placeholder functionality. Authentication remains fail-closed elsewhere.

## Validation

Seven focused synthetic tests cover creation/replacement, Unicode whitespace,
invalid kinds/scope/lifecycle, streaming concatenation, clamping including
infinities/NaN, elapsed-TTL refresh, unknown kinds, final-content precedence,
nil/empty attachments, metadata merge order, deep-copy isolation, inclusive
millisecond expiry and unconditional repeated expiration. They are not a .NET
differential oracle, PostgreSQL integration test or existing-client acceptance
test. Commands: `go test -race -cover ./ports/messager` and
`go vet ./ports/messager` with the workspace's real Go toolchain.

Executed successfully on Go 1.26.8 linux/amd64: race-enabled package tests
(98.7% aggregate package statement coverage) and package vet. This coverage
includes the concurrently added independent Messager domain modules; it is not
an isolated placeholder coverage measurement.
