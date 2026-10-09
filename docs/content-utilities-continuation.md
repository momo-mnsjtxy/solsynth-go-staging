# Translation and autocomplete continuation

Base: `fd7a177d07888cbd014af16845c8873d9783c2af`, with the frozen Reader patch `75859a4f0ebd785f1ef39e1ada8b5f840a0b05b26fa053042a7b48e7aa4d6e47`, frozen chat-relations patch `c390cacea47f051d0804d6af60df117f6a5c5589a163dd27651b5e1a13ffa290`, and chat authority-preflight correction `6d267706df42d4d6631ed22c8e064da56024c599d4f4317fc53144ca88da5b62` as dependency overlays. This slice does not modify those delivered files. Shared host, CLI and harness wiring remains with the integration owner.

## Authored contracts

[Source evidence](content-utilities-source-evidence.json) records pinned authored files, exact blobs, primary Tencent signing documentation and the independent synthetic signing-vector provenance.

Translation accepts authenticated `POST /api/translate?to=fr&from=en` with a JSON string body and returns a JSON string. The authored controller requires a positive upstream account PerkLevel and caches translations for 24 hours. Its authored provider supports Tencent TMT `TextTranslate`, version `2018-03-21`, region/project configuration and optional source language. Solian sends `/sphere/translate?to=...` with an encoded JSON string.

The canonical owned account/commerce model does not contain the upstream PerkLevel projection. The approved owned entitlement seam therefore maps explicitly configured **system subscription identifiers** to access. A current canonical subscription must be active, already started, unexpired, marked active and non-testing; its enabled definition must have no app association. Arbitrary subscriptions, custom-app catalog entries and request headers grant nothing. This is a documented mapping, not a claim of PerkLevel parity.

Autocomplete accepts authenticated `POST /api/autocomplete` with `{"content":"@name"}`. Authored prefixes are `@name`/`@u/name` for users, `@p/name` for publishers, `@r/slug` for realms and `:pack+sticker` for stickers. Results are at most ten `{type,keyword,data}` entries. Solian also calls `POST /messager/chat/{roomId}/autocomplete`; the authored controller verifies room membership and restricts user mentions to current joined room members. The canonical handler accepts that route after the standard gateway rewrite, plus the source controller's `roomId` query parameter on the general route. No tag, chatroom, article or generated-gRPC operation is invented.

## Translation behavior

`translation.Entitlement` is a typed transaction seam; `translationpg.NewSystemSubscriptions` is the concrete canonical mapping. Requests are bounded to 2,000 Unicode characters / 8,000 UTF-8 bytes and valid language tags. The optional source language is honored, matching the authored provider interface; the original controller accepted it but failed to pass it through. The cache key includes account, provider namespace, source, target and exact text, avoiding upstream omission of source language and delimiter ambiguity.

`owned_translation_cache` stores only account-scoped hashes, bounded output and timestamps. Source text and provider credentials are not stored. The account/proof and subscription entitlement are rechecked before every cache hit and before starting provider work. Provider work holds no SQL transaction or lock. Fresh checks run again before returning or caching its output, so a revocation, cancellation or disabled definition while the provider is running yields no result or cache write. Already transmitted work cannot be recalled. Per-account capacity is 1,000 entries by default; expired rows prune atomically with a successful fill.

`tencenttranslation.New` is an explicit outbound opt-in with no construction-time I/O. It requires operator-provisioned secret ID/key, region and project ID. It uses fixed `https://tmt.tencentcloudapi.com/`, verified TLS, public-address DNS validation and numeric address pinning. It has no proxy, redirect following, ambient cookies or credential forwarding. Defaults are five seconds, two simultaneous requests and at most five attempts per second per process; failures are not retried automatically. Response headers/bodies and output are bounded. Provider errors, unexpected languages and malformed responses never become successful translations. Nil provider returns unavailable.

The signing fixture ID `AKIDSYNTHETIC0001` and literal key `synthetic-secret-material-for-tests` are independently invented, not real credentials or Tencent-issued accounts. The fixed expected signature was computed with Python's standard `hashlib`/`hmac`, independently of the Go adapter. Unit tests replace DNS and HTTP with synthetic in-memory functions. No provider request was sent and no credential was read from a user or environment.

## Autocomplete privacy and ordering

Every request revalidates the original signed proof in a same-pool transaction. Prefix matching is literal and deterministically ordered, with bounded candidate queries. Disabled/punished accounts, both directions of blocks and current owned timeline account/publisher/realm exclusions are respected. Those exclusions are the existing owned suppression policy; no separate legacy muted-account store is inferred.

Account, publisher and realm data reuse canonical safe public projections through three additive same-transaction helpers. Private organization publisher and realm suggestions require current realm visibility. Pending invitations grant nothing. Excluded legacy SDK serialization, profile-picture fields and other unavailable model internals are not represented as complete client parity.

Sticker suggestions use a new bounded same-transaction helper in stickerpg. It filters current public ready image metadata, enabled owners, publisher/realm visibility and current exclusions. It plans the complete sorted realm, publisher and pack lock sets before hydration, then retains original ancestry/visibility/media rechecks. Imported organization packs can be read when allowed; this does not add unsupported organization pack creation. Bytes remain behind the canonical media adapter and are never copied or fetched externally.

Room suggestions require a current joined, unbanned membership and the canonical chat repository's `CanReadInTransaction` relation/privacy gate in the same transaction. The gate runs again before return. Missing room policy fails closed even for ordinary room IDs. The same configured chat instance enforces DM block/current peer state and linked-realm visibility; realm membership never substitutes for chat membership.

## Integration owner wiring

1. Install `internal/translationpg/schema.sql` after canonical authority and commerce. It adds only `owned_translation_cache`; autocomplete adds no table. Register its schema in the disposable runner/readiness inventory.
2. Build `gate, err := translationpg.NewSystemSubscriptions(configuredSystemIdentifiers)`, then `translationpg.New(db, guard, translationpg.Config{Provider: provider, Entitlement: gate, Now: c.Authority.Now})`. Enabled configuration requires the exact same domain/authority pool. Wire `translation.NewHandler(repo, postIdentity{identity{v}})` under `bindIdentity` and `bindTransactionAuthority`. Route only canonical `/api/translate` and sphere gateway scope. Disabled/default configuration returns 503. Readiness calls `repo.Ready`.
3. Suggested explicit CLI configuration: translation enable/send flags, an eligible system-subscription identifier list, Tencent region/project, and names of environment variables supplying the operator's secret ID and key. Reject partial configuration and feature-dependent settings without enablement. Construct the real provider only when outbound enablement is explicit, copy key bytes through the constructor and clear temporary buffers. Do not provision credentials or log their values. In tests use only a synthetic provider.
4. Build `autocompletepg.New(db, guard, autocompletepg.Config{Stickers: canonicalStickerRepo, Rooms: canonicalChatRepo})`. Reuse the sticker instance already connected to canonical social/media, and the chat instance already configured with `WithSocial`. Route canonical `/api/autocomplete` for sphere/API and `/api/chat/{id}/autocomplete` for messager/API before generic chat routing. The general source `roomId` query parameter uses the same room policy. Bind identity/proof exactly as for translation. Call autocomplete readiness and existing chat-relations readiness. No fallback to account headers, an unconfigured room policy or another SQL pool is valid.
5. Add host recorder cases for both gateway routes, JSON contracts, disabled translation, missing entitlement, absent identity/forged headers, subscription cancellation, and room-specific restrictions. Host files are deliberately outside this delivery.

Register these required PostgreSQL cases before restart:

- `TestPGTranslationEntitlementCacheAndIsolation`
- `TestPGTranslationRejectsIneligibleCanonicalGrants`
- `TestPGTranslationProviderWaitRechecksAuthorityAndEntitlement`
- `TestPGTranslationCacheCapacityRollbackAndConcurrency`
- `TestPGTranslationFreshProofWhileAccountLocked`
- `TestPGTranslationRestartPersistence`
- `TestPGAutocompleteCanonicalVisibilityAndPreferences`
- `TestPGAutocompleteStickerVisibilityAndMediaChanges`
- `TestPGAutocompleteFreshProofAndBoundedQueries`
- `TestPGAutocompleteStickerRealmLockOrderingAndRecheck`
- `TestPGAutocompleteChatRelationAndRealmPrivacy`
- `TestPGAutocompleteRestartPersistence`

Register both restart cases in the after phase. They use persisted synthetic timestamps to issue fresh synthetic proofs rather than rely on short wall-clock deadlines. Fixtures explicitly seed isolated canonical entitlement rows, which are not real purchases or provider success claims. Schema and exact-case harness checks remain pending the integration owner's registrations.

## Validation status

Full `go test ./...`, full vet, affected race checks, the original transaction-guard regression and source-evidence checks pass with all three exact dependency overlays. PostgreSQL cases compile and explicitly skip without the guarded disposable runner; no local database or socket is used. The chat owner's separate preflight correction resolves the dependency's entry-order regression while preserving its original assertion. Final shared CI must include that correction, the schema/required-case registrations and all twelve new cases. No live translation was performed.

The optional translation host/CLI and canonical autocomplete are composed. Translation requires explicit eligible subscription identifiers, with an independent outbound opt-in and explicitly named operator credential inputs. No provider is contacted during construction or configuration validation. Typed host fixtures cover string JSON, gateway/cache identity, entitlement cancellation, canonical account suggestions and room-specific membership. Actual PostgreSQL/restart results remain separate from local verification.
