# Livestream egress and authorized HLS playback

This optional slice adds the four authored egress/HLS management operations, durable provider cleanup and an owned authenticated HLS projection. It is based on `950167d0ef617bf1a3125cd9fd0783134b813219`; PIN/setup assurance, refresh continuity, paid awards, domain collections and existing host maintenance are preserved. No provider request, storage upload or real credential/account creation was performed during implementation.

## Evidence and compatibility

The fully retained authored source is pinned to DysonNetwork `4ee2d0cebb9da8284a92483fee6dce795bf6947c`: `inventory/operation-sources/DysonNetwork.Sphere/Live/LiveStreamController.cs` (egress/HLS actions), `ports/sphere/livestreams/source/DysonNetwork.Sphere__Live__LiveStreamService.cs`, `DysonNetwork.Sphere__Live__LiveKitLivestreamService.cs`, and `DysonNetwork.Sphere__Models__LiveStream.cs`. Their exact hashes/license are already recorded in `ports/sphere/livestreams/source/manifest.json`. No Shared/generated protocol definitions, NeTo/Spec/SoSYS source, excluded repositories or unlicensed content were read/imported. The earlier bounded licensed Solian caller search found no current authored HLS client; this does not claim an existing player integration.

The source requires a current publisher Editor and the relevant management permission; starting either output also requires a superuser. It accepts RTMP URLs/file paths, and HLS playlist/duration/layout fields. Its `segment_count` and `hls_base_url` fields are unused. These known fields remain in the DTO, but this implementation rejects arbitrary URLs/paths, nonzero segment counts and custom base URLs. Operator destination IDs are an owned replacement. Layouts are limited to the configured renderer's supported grid/speaker variants; the owned default is grid. Source stop/delete paths sometimes ignore provider failures; this implementation retains incomplete work.

The independently authored adapter uses the current [LiveKit Egress JSON API](https://docs.livekit.io/reference/other/egress/api/) and its [authored examples](https://docs.livekit.io/reference/other/egress/examples/), checked 2026-10-08. `StartEgress` is the current method; the older source-specific methods are deprecated. Cloud requires explicit storage for file/HLS output, and the request uses the documented room-record grant. HLS accepts a bounded media-playlist subset of [RFC 8216](https://www.rfc-editor.org/rfc/rfc8216). Provider version/transport behavior is represented by the explicit owned contract `livekit-start-egress-json-v1`; unknown contracts fail closed.

## Runtime routes

Existing `/api/livestreams` and `/sphere/livestreams` aliases both support:

- `POST /{id}/egress`: `destination_ids`; optional Idempotency-Key. Registered destinations may combine fixed RTMPS relays with one private MP4 file output. Relay destinations require a currently public stream and canonical anonymous publisher visibility; Editor management exceptions cannot bypass shadowban/private-publisher policy.
- `POST /{id}/egress/stop`: records stop intent and returns success only after provider completion/missing-resource acknowledgement.
- `POST /{id}/hls`: `destination_id`, optional `playlist_name`, `segment_duration`, `layout`; optional Idempotency-Key. Duration is 2–30 seconds; playlist names are bounded single basenames. `recording.m3u8` is reserved internally.
- `POST /{id}/hls/stop`: the same durable stop contract.
- Owned `GET /{id}/egress` and `GET /{id}/hls`: current Editor/permission-gated operation status, including an opaque output ID, phase and non-sensitive reason code.
- Owned `GET /{id}/hls/{outputId}/{asset}`: current first-party authentication and `livestreams.view`, current stream/publisher/realm/block/privacy policy, current active output/run and recorded starter authority on every request. There are no bearer query parameters, cookie grants or permanent public private-stream URLs.

The source response fields `egress_id`, `playlist_url` and `playlist_name` remain, with owned `output_id`, `state` and `reason` metadata. Stream projections populate `egress_id`, `hls_egress_id`, `hls_playlist_path` and `hls_started_at` for active outputs. The HLS path points at the owned API, never a provider bucket, file path, credential or external URL. Output playback is currently live-only; stopped/ended recordings are not an archive/download catalog. Configured MP4 egress writes to the operator's private storage; importing that recording into canonical media is not implemented.

## Authority, transactions and recovery

New operations use the existing original verifier proof, authority-only preflight, complete sorted canonical account lock set and final same-transaction guard. Publisher ownership, Editor role and realm membership are never inferred from headers. Durable output captures record `AuthorizationEpoch`, so ordinary refresh preserves an active output while old access tokens still fail. Current scope/permission, account/version/disable, ancestor/session expiry/revocation, superuser status, publisher owner/version/role and output configuration are rechecked. Loss of any required state requests stop. Restoring a grant cannot revive an output already stopping.

An output intent commits before its one allowed StartEgress attempt. Only the creating invocation may make that call; HTTP retries, process restarts and maintenance never blindly create another recording. An explicit idempotency key remains bound to its request/session/generation and completed intent. A different key cannot silently adopt an existing slot. At most one unresolved output per stream/kind exists, with a configured total history cap.

Activation holds canonical locks around the bounded provider call and commits only after fresh authorization. If activation rolls back after observing a provider ID, a separate bounded observation write retains that exact ID for stopping, after the failed transaction has closed. This write cannot authorize recording. Audit failure, expiry and cancellation therefore do not erase known cleanup work.

HLS starts with an uncertain response can be associated only when one documented provider result contains the exact owned output UUID playlist path in the expected room. Ambiguous/missing results remain unresolved. RTMP has no documented per-attempt correlation/idempotency key; an uncertain RTMP start is never guessed from a recent room result. Its durable `uncertain_start` obligation requires operator investigation. There is no automatic retry or fabricated completion, and this slice provides no unsafe “clear pending” HTTP action. Operators must retain the recorded obligations and resolve them through a separately reviewed operational procedure.

Known IDs support ordinary stop/retry. A provider ENDING response remains pending; terminal status or a specifically acknowledged `not_found` for that known ID completes it. Generic HTTP errors, malformed JSON, wrong room/ID, unknown status and redirects never count as completion. Cleanup backs off and remains inspectable without a retry-abandonment cutoff. Provider endpoint changes do not transfer old work to a different endpoint; retain a worker with the original binding until obligations are resolved.

`ProcessDue` still runs paid award payout independently. It now also runs output reconciliation even when room cleanup fails, joining branch errors. `ProcessOutputsDue(ctx, limit)` is available for the same configured provider, with limit 1–100. Unresolved outputs block a new stream generation and visibility changes, including after output configuration is disabled. Thus disabling the optional module cannot silently bypass earlier external obligations.

## Private storage and playlist policy

This extends the existing canonical `mediastore.Filesystem`; no second object store exists. The adapter reads only `streaming/<destinationId>/<ownedOutputUUID>/<singleAssetName>` below the existing private root. Every component is opened relative to a held directory descriptor with directory/no-follow checks. Directories must be 0700; files must be regular, 0600, single-link and within the configured size bound. Symlinks, hardlinks, traversal, devices/FIFOs, absolute paths and external URI opens fail. The opener creates no directories and writes no media.

The operator must explicitly provision a private S3-compatible destination and a compatible read-only mount/projection of that exact remote prefix into the canonical namespace. LiveKit uploads to `streaming/<destinationId>/<outputUUID>/`; the matching mount must expose that prefix at the same canonical relative path. The operator's `private_namespace` assertion is required; neither constructor nor readiness configures bucket ACLs or mounts storage. Without the matching namespace/capability, HLS is unavailable. The mount must expose finished readable files with the required modes, link counts, sizes and dependable filesystem reads; a stalled underlying mount is an operational dependency.

The parser supports bounded MPEG-TS media playlists with ordinary duration/sequence/discontinuity/date metadata. It rejects master playlists, encryption/key URLs, initialization maps, byte ranges, unknown tags, malformed durations and any segment reference outside the current output. Every accepted reference is rewritten to the authenticated owned route. A segment must still appear in the current validated playlist and have 188-byte TS packet framing. Partial/unsupported segments fail; this is framing validation, not codec decoding. No external request follows a playlist reference. Responses are private/no-store and nosniff; conditional caching and range-based bypasses are not provided.

## Host/CLI composition

The integration adds `LivestreamConfig.Outputs`. `configureStreams` type-asserts the existing `Config.MediaStore` to `live.ArtifactStore`, checks media size compatibility, and calls `WithOutputs` before serving. A separate artifact store cannot be supplied through this configuration. Readiness checks the output schema and each explicitly enabled HLS namespace. Constructors only validate; no network or provider resource is created.

Use `rtcprovider.NewWithOutputs(existingRTCConfig, OutputPolicy, transport)` for programmatic composition. Plain `rtcprovider.New` leaves outputs unconfigured. The provider policy copies selected destinations/credentials, accepts fixed RTMPS endpoints and explicit private S3-compatible storage, and never accepts request-supplied endpoint/path/credential values. Configure before traffic; these setters are not a concurrent hot-reload API.

CLI activation requires existing `--enable-livestreams`/LiveKit settings plus `--enable-livestream-outputs` and an absolute 0600 `--livestream-output-config` file. The file's version is exactly 1. It contains destination IDs, kinds and environment-variable references; raw provider/storage credentials and RTMP stream keys are injected only at runtime. The loader rejects duplicate/unknown JSON, unknown versions, missing variables and unsafe namespace configuration. It never logs the configuration or secret values.

Example shape, containing no credentials:

```json
{"version":1,"destinations":[{"id":"private-hls","kind":"hls","storage":{"private_namespace":true,"bucket":"example-private-bucket","region":"example-region-1","access_key_env":"SOLSYNTH_STREAM_ACCESS_KEY","secret_env":"SOLSYNTH_STREAM_SECRET"}},{"id":"public-relay","kind":"rtmp","url_env":"SOLSYNTH_PUBLIC_RELAY_URL"}]}
```

The HLS entry requires the existing `--media-root` and a private `streaming/private-hls` namespace. Limits are explicit: `--livestream-output-limit`, `--livestream-hls-segment-limit`, `--livestream-hls-playlist-bytes`, `--livestream-hls-segment-bytes`, and `--livestream-output-timeout`. Existing external maintenance cadence schedules reconciliation. HLS segment byte limit must fit the canonical media byte limit. There is no new live-provider activation during this task and no credential generation command.

Install `internal/livestreampg/outputs_schema.sql` after the canonical livestream schema. It adds one table, `owned_livestream_outputs`, and four indexes for the active slot, exact provider ID, idempotency key and bounded due queue. No startup DDL runs. The explicit disposable harness and offline schema-order guard include it. The source generation/authorization migrations and PIN assurance remain unchanged.

## Validation

New required before-restart cases are:

- `TestPGLiveStreamHLSHTTPPlaybackAndCurrentPrivacy`
- `TestPGLiveStreamOutputsRequireFixedDestinationsAndAdminEditor`
- `TestPGLiveStreamOutputsUncertainStartNeverRecreates`
- `TestPGLiveStreamOutputsActivationRollbackRetainsCleanup`
- `TestPGLiveStreamOutputsFreshPolicyAndDurableStopRetry`
- `TestPGLiveStreamOutputsConcurrentIdempotencyAndCapacity`
- `TestPGLiveStreamOutputsDisableDuringAuthorityWait`
- `TestPGLiveStreamOutputsEndCleanupIndependentAndUnconfigured`
- `TestPGLiveStreamOutputsRestartPersistence`
- `TestPGBackendLivestreamOutputPlaybackComposition`

The restart phase repeats `TestPGLiveStreamOutputsRestartPersistence` after an actual database restart. These additions take the declared frozen-base inventory to 431 before / 57 after. They use synthetic users, keys, transport responses, private fixture files and canonical guards only. All batches obey real API limits, timestamps use injected/database clocks, SQL parameters are explicitly typed, and JSON requests use the canonical fixture helpers. Unit/race tests cover unsafe configuration, provider response binding, private descriptor reads and malicious playlists. Local build/vet/test/race and integration compilation are distinguished from actual PostgreSQL evidence; only the parent's guarded CI can establish those passes.

Remaining boundaries are intentional: no arbitrary output URL/path relay, no self-hosted RTC revocation equivalence, no archive media import/player UI, no unsupported playlist/codec variants, no automatic resolution of uncorrelated RTMP outcomes, and no server-created storage namespace, credentials or bucket policy.

Final offline SQL verification parsed every one of the 61 SQL files and 9 PL/pgSQL bodies in this exact archive with pinned pglast 6.7 / PostgreSQL 16.1, and rejected the four parent guard negative controls. This verifies syntax only, not catalog/type/constraint/locking execution.
