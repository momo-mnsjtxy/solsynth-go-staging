# Persistent room sync/read projection provenance

Modified 2026-10-08. AGPL-3.0-only; repository root license and the original
source license remain included. This implementation is independently authored Go
informed by the following eligible owned code and client sources.

## Pinned server evidence

DysonNetwork revision `4ee2d0cebb9da8284a92483fee6dce795bf6947c`:

- `ports/messager/source/ChatController.cs`: `GetReadableRoomAsync` at line 87;
  `GetTotalUnreadCount` at line 255; list/single reads at lines 545/585;
  room sync at line 1649. Room sync requires an identity before room lookup.
- `ports/messager/source/ChatService.cs`: unread at lines 1958–2042;
  `GetSyncDataAsync` at line 2094; sequence normalization/predicate immediately
  following; reactions/thread hydration at lines 3169–3243. Mutation service
  inserts distinct `messages.update` and `messages.delete` events.
- `internal/messagerpg/source/ChatRoomService.cs`, Git blob
  `9cf43ace02eac8a03749b157a16e890c102cafc4`: account and realm hydration is
  remote-service behavior, including cache-backed missing-account detection.
  Reading its source does not supply the remote model definitions or authority.
- Owned EF migration snapshot
  `DysonNetwork.Messager/Migrations/AppDatabaseModelSnapshot.cs`, Git blob
  `170bc4adff3fbaf97b1b720b26fe99f0d75aff16`, pinned before SQL construction.
  `internal/messagerpg/schema-evidence.json` records tables, columns, SQL types,
  hashes, and source positions. `source-manifest.json` pins both newly retained
  files; a test verifies exact Git blob and SHA-256 integrity.

The original `AppDatabase.cs` explicitly invokes shared soft-delete helpers,
uses unique room/sequence and room/account constraints, and names PostgreSQL
columns. This does not establish the shared helper implementation or enum
numbers. No excluded shared model/protocol repository was fetched.

## Expanded authored client review

Solian revision `ffd0838e5f04cb40bea311a39118ebeedad210c1`:

- Existing licensed `ports/messager/contract-evidence/` pins message_repository,
  messages_notifier, message_handler, local message conversion and E2EE handling.
  Recovery sends explicit missing integer sequences/ranges; per-event sequences
  must not be conflated with the target message's client ID.
- Authored `ios/SolianWatchApp/Models/Models.swift`, retained and independently
  pinned in `inventory/product-client-evidence/manifest.json`, was additionally
  inspected at lines 1201–1267 and 1372–1461. It explicitly declares message,
  member, nullable metadata and date keys. Its lenient defaults are client
  resilience, not a server-side promise. It lacks newer sequence/thread/E2EE
  fields, so it is not an exhaustive modern message serializer.
- Authored `lib/chat/models/redirect_data.dart`, blob
  `85c09148f9ccc7e7327fe0a9268987bc2c1cbd4e`, lines 1–100 were retrieved from the
  pinned eligible repository. They establish history-segment/source-message maps
  while importing the shared SDK; they do not establish the full message DTO.
- Existing authored `docs/BOT_CHAT_API.md` broadens the public chat/authorship
  context but does not supply complete room-sync message serialization.

Public factual documentation was also sought through official web pages and
three domain-restricted searches (chat sync, messaging, encryption). The web
reader returned inaccessible/cache-miss responses for the messaging/developer
pages and searches returned no results. These failed lookups are not evidence
that documentation does not exist. No Suki repository content, generated
OpenAPI export, NeTo/Spec schemas, descriptors, generated bindings, canceled
SoSYS retrieval or Stargate/Socommon workaround was inspected.

## Distinctions and deliberate design

Source-informed selection, sequence recovery, unread predicates and content
formatting reuse established policies. Reactions-made and thread counters use
real persisted state. The raw JSON projection preserves encrypted data and
large number tokens; no fabricated sender/account DTO is emitted.

Read-only repeatable-read transactions, bounded/strict HTTP binding, loopback
hosting, fixed fixture identity, raw JSON shape checks and explicit visibility
are independently authored design choices. They are not represented as upstream
byte-for-byte behavior. See `docs/messager-continuation.md` for boundaries and
remaining external contracts. Full application-client compatibility is not
claimed for this partial message projection.
