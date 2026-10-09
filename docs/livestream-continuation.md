# Canonical livestream ingress and lifecycle

Base: `d64aa11c45a80b151f40a99f85d93f4a7d2f30f0`. The delivery includes the already approved chat preflight implementation in `internal/authoritypg/transaction.go`; an integration that already has that identical correction should retain it. Host, CLI and migration-harness composition remain with the integration owner.

## Evidence and compatibility boundary

`ports/sphere/livestreams/source/manifest.json` identifies exact Git blobs, SHA-256, revisions and retained AGPL license. The full authored DysonNetwork controller, cleanup job, `LiveStreamService`, `LiveKitLivestreamService` and `Models/LiveStream.cs` were read at `4ee2d0cebb9da8284a92483fee6dce795bf6947c`. The latter is a licensed root-repository model, not a Shared/generated definition. Referenced excluded protocol/model sources were not opened or imported. The two service snapshots preserve their original lack of a final newline.

The authored model supplies status Pending/Active/Ended/Error = 0/1/2/3, type Regular/Interactive = 0/1, visibility Public/Unlisted/Private = 0/1/2, metadata bounds and the serialized resource identifier. Known stream fields are included. Canonical publisher and media projections are explicitly owned replacements for unavailable cross-service DTO definitions. Egress/HLS fields remain null and award counters remain zero because those effects are outside this ingress slice.

Pinned Solian searches at `ffd0838e5f04cb40bea311a39118ebeedad210c1` found only a generated capability artifact for the Dart `livestream` query. It was not opened or reused. Metadata-only search results and checked directory trees establish a caller-evidence gap, not a claim that every historic client was searched. There is no claim of complete legacy UI/API parity.

The provider adapter is independently authored from the official [Ingress API](https://docs.livekit.io/reference/other/ingress/api/) and [access-token grants](https://docs.livekit.io/frontends/reference/tokens-grants/) documentation, checked on 2026-10-08. It sends scoped create/list/delete requests, supports RTMP and WHIP push inputs, and gives viewers explicit subscribe-only grants. Existing Cloud revocation support is required. The documentation does not supply an idempotency key for ingress creation, so uncertain creates are reconciled by their recorded room/name/participant binding.

## Implemented HTTP surface

`livestreams.Service.Handler()` handles `/api/livestreams` with strict JSON, duplicate-key/depth/size limits, exact field names, bounded query parameters and no-store responses. Original signed proofs come from the existing verifier and `guard.Bind`; caller-supplied identity/role headers are rejected.

| Route | Behavior |
| --- | --- |
| `POST /api/livestreams?pub={selector}` | Create canonical publisher-owned metadata; optional `Idempotency-Key` replays an identical request |
| `GET /api/livestreams` | Bounded public active-stream discovery |
| `GET /api/livestreams/publisher/{id}` | Bounded canonical publisher listing; private/unlisted entries require current Editor authority |
| `GET /api/livestreams/{id}` | Current publisher/realm/relationship visibility and sanitized stream projection |
| `PATCH /api/livestreams/{id}` | Current Editor updates title, description, slug, type, visibility or metadata |
| `PATCH /api/livestreams/{id}/thumbnail` | Link or clear an actor-owned, ready, explicitly public canonical image file |
| `POST /api/livestreams/{id}/start` | RTMP, WHIP or `no_ingress` native publishing, with durable provider intent |
| `GET /api/livestreams/{id}/token?streamer={bool}&tool={bool}` | Fresh scoped participant grant; publishing additionally requires current Editor role |
| `GET /api/livestreams/{id}/details` | Confirmed provider participant count plus observed viewer/peak counts |
| `POST /api/livestreams/{id}/end` | End once, revoke participants and clean provider resources |
| `DELETE /api/livestreams/{id}` | Soft-delete immediately and retain durable cleanup until acknowledged |

List limits are 1–100 and offset 0–100000. Metadata is bounded to 16 KiB and 64 top-level keys. Configured stored-stream, participant and lifetime caps are explicitly owned policy; no publisher grant or quota is provisioned by construction. Initial metadata creation is local; provider room creation occurs on start. Each restart uses a distinct generation/room, so cached tokens cannot join a restarted broadcast.

Private streams require current publisher Editor authority. Unlisted streams are accessible by ID under canonical publisher visibility but are omitted from public lists. Public streams inside private organization realms remain subject to realm membership. Canonical owner disable/punishment, social blocks and publisher visibility rules apply to reads and admissions. The new publisher transaction wrappers reuse existing `visiblePublisher` and `require(..., Editor)`, so central shadowban/realm policy changes apply without a parallel policy store.

Thumbnails retain the canonical file ID and fresh metadata/hash/size. Reads re-resolve public visibility and omit a deleted or no-longer-public file. Bytes continue through existing canonical media routes; this slice creates no object store or private-media bypass. MIME metadata must be an image and excludes SVG; it does not claim to validate image encoding.

Ingress URLs/keys are returned only by a successfully authorized start/retry for its initiating session. Keys are not persisted in the SQL tables, audits or restart artifacts. General stream reads always leave ingress keys/IDs null, including Editor reads. This deliberately narrows the legacy owner's metadata response while preserving the usable encoder setup response.

## Authority, locks and captured eligibility

Every write begins with same-pool, same-transaction `TransactionGuard.Preflight`, before domain observations. It then locks the complete actor, publisher/realm owner and existing run initiator account set in sorted order. Realm, publisher and stream locks follow. Observed ancestry/generation is rechecked, canonical Editor policy is applied, and the original opaque proof is revalidated after locks and before commit. The transaction is ReadCommitted; it opens no second connection for authority checks.

New stream mutations and tokens require a root Type-0 session. Delegated OAuth and unsupported child-session resource routes fail closed; no bot or OAuth principal is invented. Named owned permissions are `livestreams.create`, `livestreams.update`, `livestreams.thumbnail`, `livestreams.start`, `livestreams.view`, `livestreams.end` and `livestreams.delete`. Streamer token issuance uses `livestreams.start`; viewer issuance uses `livestreams.view`. Current publisher Editor role is an additional requirement for management/publishing.

Run records capture the initiating account/session, version, epoch, exact permission, owner ID/version, provider binding and request hash. Participant admissions capture the same session/version/epoch and exact view/start permission. A query parameter never grants publish capability. Short-lived tokens have explicit publish/subscribe/data permissions, opaque identities and no room-admin or ingress-admin grant.

The approved `StoredSessionEligible` helper is read-only, uses the caller's transaction, and returns only bool/error. It constructs no Principal, proof or credential. It shares the existing policy evaluator, checks captured versions/epochs, current grants/punishments and every bounded Type-0 parent scope/expiry/revocation, and denies delegated or malformed sessions. It can only retain/revoke previously recorded downstream admissions/runs. New stream/token authorization independently requires the original-proof guard. The extracted `evaluatePolicy` retains existing authorization semantics; the ordinary Type-0 verifier behavior is unchanged.

Background reconciliation uses sorted account and domain locks with NOWAIT to avoid inverted waits. It never starts a new ingress or issues credentials. Current sessions, publisher roles, owner versions, privacy and parent state are re-read before retaining admissions. Lost Editor/view permission, account/session invalidation or owner-version changes trigger durable removal/ending. Provider removal uses the existing explicit Cloud cutoff contract, including identities already disconnected.

## Provider intent and recovery

The live adapter extends the existing validated `rtcprovider.LiveKit` instance in a new file. It reuses its TLS endpoint/configuration, signer, bounded transport and strict revocation declaration. No new API key or service account is created. RTMP requires transcoding; WHIP may disable it. URL-pull input and arbitrary destinations are unsupported. Provider responses must match the recorded room/name/identity/input type, and ingress lists reject pagination or excessive results rather than assuming an incomplete listing is complete.

Start intent is committed before the first provider call. Only that invocation may create an ingress. Subsequent attempts discover an already-created resource and require the original request hash and initiating session. A lost response or failed activation commit can therefore recover without issuing another create. Discovered provider IDs are committed before deletion, so a crash after a successful delete can safely retry the same ID and accept an explicit not-found acknowledgement.

If an uncertain create is never observable, it remains unresolved. Retries do not create a replacement and end/delete does not report cleanup success. The durable ending record remains retryable and needs provider-side reconciliation by an operator if it never becomes observable. This is an explicit limitation of the provider's available contract, not a fabricated success or a timed assumption that nothing happened.

Ending records persist across failures/restarts. Participant revocation, ingress deletion and room deletion must acknowledge success before the run becomes done. Duration is added once at the logical end transition. Retry delay is bounded exponentially to 60 seconds. Only jobs matching the configured provider name/endpoint are processed; records bound to an older provider require that matching configuration to finish cleanup. New requests against mismatched bindings fail unconfigured.

`ProcessDue(ctx, limit)` accepts 1–100. It checks idle ingress after the configured grace (at least the authored five minutes), enforces maximum run lifetime, updates counts from known current participant identities, removes invalid admissions and resumes cleanup. An explicitly acknowledged missing room counts as empty; transport/authorization errors do not. Native sessions still have the configured maximum lifetime. Keep/revoke maintenance does not extend sessions or mint credentials.

## Host composition and schema order

Install canonical authority, account, social and media schemas before `livestreampg.Schema`. This creates only:

- `owned_livestreams`, with canonical publisher/media FKs and public/publisher listing indexes
- `owned_livestream_create_keys`, binding actor/request hashes to a stream
- `owned_livestream_runs`, binding durable generations to canonical accounts/sessions and provider configuration
- `owned_livestream_ingresses`, retaining discovered IDs until deletion acknowledgement
- `owned_livestream_admissions`, retaining bounded participant grants and removal state
- `owned_livestream_audit`, with canonical actor/stream FKs

The due index begins with `(provider,endpoint,next_attempt_at,stream_id,generation)` and excludes done runs. Shared canonical schema definitions are not duplicated.

Recommended composition reuses existing instances:

```go
streamRepo, err := livestreampg.New(db, guard, accountRepo, mediaRepo,
    socialRepo, livekitProvider, livestreampg.Config{
        TokenTTL: time.Minute, MaxRunDuration: time.Hour,
        IdleGrace: 5*time.Minute, MaxStreamsPerPublisher: 20,
    })
if err != nil { return err }
streamHandler := (&livestreams.Service{
    Repository: streamRepo, Authority: verifier, Bind: guard.Bind,
}).Handler()
```

`accountRepo` must already have `.WithRealms(socialRepo)`. `livekitProvider` is the same concrete validated instance created for calls when both features are enabled; it implements both interfaces. Prefer an explicit disabled-by-default `--enable-livestreams` option and permit the existing LiveKit environment-reference flags when either calls or livestreams is enabled. Stream configuration must not implicitly enable calls. Keep existing secrets in their operator-provisioned configuration; no values belong in logs, docs, tests or command arguments.

Dispatch `livestreams.RouteMatches(path)` to this handler. Add `streamRepo.Ready(ctx)` to enabled-feature readiness and `streamRepo.ProcessDue(ctx,100)` to the existing bounded host maintenance loop. No constructor starts a goroutine or migrates. A 5–10 second scheduler cadence is suitable; remote revocation delay is bounded by maintenance/backoff, while new backend admissions deny immediately. Readiness verifies configured capabilities and schema, not live provider reachability.

## Deferred original contracts

Egress/HLS creation/stop, output storage/playback, livestream chat/moderation, awards/payment settlement, subscriber notifications, provider data broadcasts and webhook ingestion return unsupported or remain absent. They are not advertised as completed. Egress needs reviewed operator-owned destination/storage policy; awards need canonical paid-order settlement and idempotent accounting; broadcasts need a durable delivery contract. This slice does not fetch media from user URLs, execute packages, contact live providers or fabricate original rewards.

## Validation

Local validation: all-package tests/build, focused race tests, affected-package vet and integration compilation. PostgreSQL cases are compiled only; the parent runs its guarded disposable harness. Required before cases:

1. `TestPGLiveStreamLifecycleTokensAndPrivateVisibility`
2. `TestPGLiveStreamIngressUncertainCreateAndDurableCleanup`
3. `TestPGLiveStreamCapturedPermissionAndPublisherRevocation`
4. `TestPGLiveStreamUnknownIngressOutcomeFailsClosed`
5. `TestPGLiveStreamConcurrentIdempotencyAndProviderRollback`
6. `TestPGLiveStreamCanonicalThumbnailAndUnconfiguredProvider`
7. `TestPGLiveStreamDisableDuringLockWait`
8. `TestPGLiveStreamParentExpiryAndIdleCleanup`
9. `TestPGLiveStreamCleanupRestartPersistence`

The last case is also required after the actual PostgreSQL restart. Saved state contains only synthetic IDs, provider binding and ingress metadata with an empty key. The after phase restores a synthetic provider projection and confirms the durable cleanup completes.

Fixtures use one matching verifier/guard/pool per repository and genuine synthetic proofs, canonical named publisher selection, explicit publisher/media quota, canonical Editor invitations and the root `fixtureJSONRequest`/`fixtureNoBodyRequest` transport helpers. DB clock values drive deadlines; SQL timestamp nulls are explicitly typed and nullable fields use nullable scans. Batch limits are 100. All provider effects use in-memory synthetic implementations or injected RoundTrippers; no local socket, live provider, real user, production credential or external publication was used.

## Integrated host candidate

`--enable-livestreams` opts into the configured adapter independently from `--enable-calls`; enabling both reuses one validated provider instance. Stream duration, token TTL, idle grace and publisher cap have separate bounded flags. Existing provider credentials remain explicit environment references. The fast `--external-maintenance-interval` worker includes streams, and constructors still perform no network or DDL. Host dispatch covers `/sphere/livestreams` and `/api/livestreams`; enabled readiness checks all six tables and supported provider binding. A typed nil media repository is not passed as an enabled media capability. The composed PG fixture exercises publisher creation, metadata/start, viewer versus publishing scopes, private visibility and acknowledged cleanup with an in-memory provider. This tests the configured runtime contract but does not establish a live provider deployment. The ten added PG cases remain pending exact-commit CI.
