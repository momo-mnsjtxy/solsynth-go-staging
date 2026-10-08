# Messager persistent sync/read continuation

This is an independently authored, runnable PostgreSQL read family informed by
licensed DysonNetwork and Solian sources. It is not a complete replacement for
Messager or its full `SnChatMessage` response. All production constructors remain
disabled until a coherent verified identity/permission integration exists.

## Implemented behavior

- `POST /api/chat/{roomId}/sync`: strict timestamp selection, exact sequence and
  inclusive range recovery, normalization through the retained recovery policy,
  top-level selection, ascending sequence/date order, 1000-row cap, full matching
  count, `X-Total`, and last selected record's timestamp (clock time when empty).
- `GET /api/chat/{roomId}/messages`: descending sequence/date, take/offset and
  full count. Public unencrypted rooms allow anonymous reads.
- `GET /api/chat/{roomId}/messages/{messageId}`: room-scoped lookup, including a
  thread reply when requested directly.
- `GET /api/chat/unread`: independently gated aggregate of active memberships,
  self/placeholder/mutation exclusions, last-read strict comparison, notification
  and mention policy. A real explicit fixture notification-enum mapping is required.
- Persisted event rows remain events: `messages.update` and `messages.delete`
  are included by recovery; target references and JSON values are preserved.
- Actual SQL room/membership checks, caller-specific reactions and nondeleted
  thread reply counts. There is no fabricated member, account, reaction count,
  sender profile, or membership grant.
- Stored nested JSON uses `json.RawMessage`, including attachments, arbitrary
  metadata, reaction counts and encryption metadata. Large integer tokens are
  not rounded through float64. Encryption is opaque; this server does not decrypt,
  invent cryptographic fields, or escape encrypted content.

## Authored semantics versus upstream evidence

Source-observed client-visible selection, count/cursor, membership predicates,
reaction attribution, thread counting and formatting are preserved for the
supported projection. The implementation deliberately uses a read-only
repeatable-read transaction for access, count, page and hydration. This stronger
snapshot is our design, not a claim about upstream transaction behavior.

Candidate visibility explicitly excludes deleted rooms, memberships, top-level
messages and selected senders. Sender room IDs must match their messages. The
unread aggregate applies only member/message deletion policy and adds no thread,
sender, or room filter absent from its source query. The shared filter hook is
not reconstructed from its name. These choices are explicit fixture policy,
not production authority or a permanent technical restriction on further work.

The HTTP adapter accepts canonical hyphenated GUIDs, bounded request bodies and
selectors, and a portable year 1–9999 timestamp range in timestamp mode. It
requires an explicit non-null timestamp, rejects unknown/duplicate top-level
JSON keys, and has a small documented error envelope. These are deliberate
candidate binding rules, not asserted ASP.NET error/binding parity. Sequence mode
does not convert the ignored timestamp, so full int64 sequence recovery works.

## Construction and running

Production defaults `messagerpg.New`, `messager.NewSyncService`, and
`messager.NewSyncHandler` deny without querying PostgreSQL. Use the explicitly
named `NewSyntheticProjectionRepository`, `NewSyntheticProjectionSyncService`,
and `NewSyntheticProjectionSyncHandler` only with disposable data. The handler's
optional fixture identity is fixed at construction; request headers, cookies,
query strings and bodies cannot grant identity or membership.

`cmd/messager-sync` is a loopback-only host. Without opt-in it serves denied
responses and does not read a DSN or open a database. Synthetic operation needs
`--synthetic-projection`, `--postgres-dsn-env=PG_TEST_DSN`, and
`--synthetic-none-encryption-mode=77`. The value 77 is a fixture choice, not an
upstream enum. Add `--synthetic-account=<fixture-guid>` for room sync; omit it for
anonymous public message reads. Incoming Authorization/Cookie/account headers
are rejected even in fixture mode. To enable unread, also provide all three
`--synthetic-notify-all=11`, `--synthetic-notify-mentions=22`, and
`--synthetic-notify-none=33`; these are distinct fixture enum values. DSNs are read from named environment variables
and never printed. Responses identify `synthetic-room-sync-projection-v1` in
`X-Solsynth-Projection` and are not cacheable.

Schema is `internal/messagerpg/testdata/schema.sql`, a disposable projection
fixture, never a production migration. It is derived from the pinned owned EF
snapshot recorded in `internal/messagerpg/schema-evidence.json`.

## Durable test handoff

The parent integration runner must load the fixture SQL, build
`./cmd/messager-sync`, require every `TestPGMessager*` result in its before phase,
and include `TestPGMessagerRestartPersistence` after the actual PostgreSQL restart.
The test suite uses the existing disposable-cluster marker/DSN guard.

Durable test names:

1. `TestPGMessagerTimestampSequenceAndRangeRecovery`
2. `TestPGMessagerMembershipAndVisibility`
3. `TestPGMessagerHydrationAndMessageReads`
4. `TestPGMessagerLimitAndExactInt64`
5. `TestPGMessagerReadOnlyFailuresAndIndependentPool`
6. `TestPGMessagerRestartPersistence`
7. `TestPGMessagerUnreadMatchesRetainedPolicy`

They cover event recovery, gaps, reversed/overlapping/invalid selectors, strict
time edges, foreign rooms, threads, tombstones, encrypted/private access,
invited/left/deleted membership, anonymous/public reads, >1000 rows, MaxInt64,
large nested JSON, plaintext/encrypted formatting, reaction attribution,
thread counters, read-only SQLSTATE, independent connections, cancellation,
sanitized failures, and durable restart recovery.

Local PostgreSQL and HTTP listeners are not executed in this continuation because
the environment disallows sockets. Recorder tests exercise SQL ordering,
parameters, transaction policy and rollback without pretending to be PostgreSQL.
HTTP tests use `httptest.ResponseRecorder`, not a listener. Actual PostgreSQL
verification and live command HTTP verification remain parent-CI responsibilities.

## Remaining client-visible contracts

The projection intentionally omits complete `sender`/account/profile/realm
hydration, related message objects and the full model's serializer policy. The
licensed `ChatRoomService` shows that sender loading calls remote account/realm
services and converts remote records; it does not establish their complete wire
contract. The authored Swift model broadens field evidence but includes lenient
fallbacks and lacks newer sequence/thread/encryption fields. Dart conversion and
redirect-history models still delegate the complete message model to an excluded
shared SDK. A fallback sender in a client is not authority to fabricate a server
account. These clients therefore cannot be advertised as drop-in compatible with
this partial response.

Production authentication/permission decisions, encryption and notification
enum numeric values, complete remote-account hydration, exact omitted/null/time
serialization, and the unknown shared row filter contract remain open external
contracts. Message mutation/send, websocket delivery, thread mutations, account
services, MLS state and production migrations are outside this read-only slice.
No excluded protocol schemas, descriptors or generated bindings were used.
