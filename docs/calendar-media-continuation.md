# Calendar media and activity continuation

This owned extension is isolated from coordinator `106c35a0e860add9d58d40737ff27cc4743e5cc1`. It completes calendar event image associations and adds a useful explicit activity projection while retaining the existing calendar CRUD, date, recurrence, catalogue and subscription implementation. Full backend support for the wider source family is not claimed: the legacy merged activity numeric codes and a lawful regional seed corpus remain open.

## Source and contract

`calendar-media-source-evidence.json` records ten full retained authored controller/service/client/document inputs with exact SHA256, Git blob, pinned URLs and AGPL licenses. Its Go provenance test verifies those identities without network access. No new upstream fetch, excluded generated client/model/protobuf implementation, NeTo/Spec, unlicensed Suki, canceled SoSYS/Socommon or mirror, omitted fortune-bearing AccountEventService, regional prose or image asset was used.

The retained event controller and Solian editor establish image selection with icon/background file IDs and rendered references. The caller delegates wire serialization to an excluded SDK, so SDK omission/null behavior is not asserted. This implementation explicitly defines omission as retain, JSON null as remove and a UUID as select/replace. Empty strings and arbitrary URLs are invalid; UUID case is normalized. Catalogue `notable-days` has no evidenced media fields, so none are invented.

The authored search decoder maps user events to 0 and notable days to 1. The authored RPC countdown converter maps user events to 0 and notable days to 3 in its distinct protobuf-bound output. Neither establishes HTTP merged check-in/status values. Existing `unavailable_components` continue to report those two legacy gaps; the new `activity_events` array uses independently authored `kind` values `check_in` and `status`. Each entry includes ID, safe title, start/end timestamps and all-day flag. It does not include device metadata, secrets, raw status metadata, reward details or fortune prose. Check-ins remain owner-only; status visibility remains the canonical public/friends/private policy. Invisible statuses preserve the existing default-type redaction for other viewers. Activity entries sort by instant, then kind and ID and respect the requested monthly time zone. An uncleared status has the existing 24-hour display interval; a cleared status uses its clear instant, clamped to its creation instant. Check-ins are point entries. The numeric merged array and the independent activity array are separate contracts.

## Canonical media lifecycle

`internal/calendarmediapg` stores only `(event_id, slot, file_id, attached_at)` references in `owned_calendar_event_media`. It uses existing media ownership, upload readiness, charge/quotas and object storage. Associations do not copy file data, mutate public flags, create new ownership or recharge bytes. Only ready passive-image MIME types are eligible. Image decoding/re-encoding is not added.

Create, replace, remove, event mutation and audit commit together. Referenced UUIDs are locked in sorted order after the canonical actor/event locks; the caller revalidates its original proof after all writes/waits before commit. Invalid/foreign/deleted/pending/nonimage references fail closed, including references that a caller can read through public or realm/post grants. Idempotency hashes bind normalized slot IDs, preserve existing hashes for media-free creates, and replay current projections. A deleted original does not revive.

Event detail, lists, recurrence expansion, monthly daily/merged views, search and countdown carry current references. Deleting an event removes its references transactionally. Deleting a canonical file suppresses its projection and downloads; it does not corrupt the event or duplicate a quota release. Detaching the last event grant removes that grant to the private file. Independent owner, public, profile, room, chat, ticket or post/realm grants remain governed by their existing authorities.

The new media read policy calls the same canonical calendar audience predicate as event reads, checking current ownership, deletion, mutual friendship, bilateral blocks and account disable/punishment state. Subscriptions do not bypass the audience. Anonymous reads require a public event or an independent existing grant. Authenticated reads retain the original proof, including after a file-lock wait; expired or revoked authentication never downgrades to anonymous access. Media revalidates the original proof after file, composed-policy and object-store waits. It rereads owner state and composed parent grants after opening the object, closes it on failure, and returns no file metadata on any read error. Calendar projections revalidate complete event/version/attachment sets and activity privacy after adapter waits using fresh ReadCommitted statements, including anonymous requests. A concurrent change yields not-found or conflict before any response body. The observation boundary is the final authorization/projection validation before response emission; bytes already streamed cannot be recalled.

There is no newly invented realm calendar model. Canonical realm/post media grants remain composed, and a realm member's ability to read a file cannot become attachment ownership.

## Runtime and configuration

Apply `internal/calendarmediapg/schema.sql` explicitly after the existing authority/social/media/calendar schemas. The composed backend enables references when its existing media filesystem and positive size limit are configured; it shares the canonical authority clock. No separate provider, credentials, listener, URL fetch or migration-at-startup is added. Without media configuration, media mutations return the existing explicit unsupported response; ordinary calendar reads and writes continue.

The calendar and composed media readiness probes fail when configured association schema dependencies are missing. The existing backend media options remain the operator configuration surface. Regional records can be supplied using permissioned notable-day CRUD under operator control; no unverified seed assets or corpus are bundled.

## Verification and mandatory PostgreSQL coverage

The new tests are registered in the required PostgreSQL before phase; both restart tests also belong in the after phase. The coordinator must reconcile the grouping/dependency audit after combining concurrent deliveries.

- `TestPGCalendarMediaComposedAudienceAndProjection`
- `TestPGCalendarMediaStorageWaitRevalidatesAudienceAndProof`
- `TestPGCalendarMediaProjectionWaitRevalidatesCompleteResponse`
- `TestPGCalendarActivityProjectionWaitRevalidatesPrivacy`
- `TestPGCalendarMediaPolicyWaitReturnsNoMetadataOnFailure`
- `TestPGCalendarMediaOwnershipReplayQuotaAndRollback`
- `TestPGCalendarMediaFileWaitRevalidatesStateAndAuthority`
- `TestPGCalendarMediaReadWaitObservesVisibilityWithdrawal`
- `TestPGCalendarMediaComposedReadProofWait`
- `TestPGCalendarMediaRealmGrantCannotRedistribute`
- `TestPGCalendarMediaRestartPersistence`
- `TestPGCalendarActivityStringProjectionPrivacyAndTimeZone`
- `TestPGCalendarActivityRestartPersistence`

The media restart test exclusively saves `calendar-media.json`, reopens the same `calendar-media-objects` directory and uses saved canonical actor, event and file IDs after the real runner restart. It verifies public/private/deleted state, recurrence keys and actual saved image bytes without reseeding those rows. The activity restart test saves `calendar-activity.json` and reads the same status, check-in, friendship and privacy records afterward. After phases only issue new synthetic authentication sessions; they do not recreate the tested identities or resources.

No local PostgreSQL/socket execution was attempted. Local integration package success means compiled tests skipped without a DSN, not database acceptance. Full Go build/vet/normal/race and pinned source/offline SQL results are recorded in the delivery receipt once complete. The offline PG16 syntax/type-placeholder checks do not prove database constraints, typing, concurrency or transaction behavior. Current mandatory PostgreSQL and genuine restart acceptance remain pending the authorized private runner.
