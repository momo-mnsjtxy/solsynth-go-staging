# Messager parity status

Implemented bounded policies include sequence recovery selection, message
heading formatting, unread aggregation, placeholder state transitions and
notification body construction. See the per-module provenance documents. The structured
inventory at `docs/messager-parity.json` catalogs 74 HTTP action declarations,
9 database sets, 12 symbolic packet constants, inbound event listeners and
3 scheduled jobs. All HTTP, transport, persistence and job entries remain
unimplemented. Counts describe the selected pinned source, not API completeness.
Route authorization attributes were extracted mechanically; inline checks and
middleware must also be considered. An absent attribute is not evidence of an
unauthenticated operation.

## Exact room-sync boundary

`POST /api/chat/{roomId:guid}/sync` first requires a current authenticated
account, then looks up the room. Missing identity returns 401; missing room
returns 404. For a private or encrypted room the caller must have joined and
not left; a public unencrypted room allows any authenticated caller. The
shared middleware's permission behavior remains an external dependency.

The body accepts an int64 millisecond timestamp, optional exact sequences and
optional inclusive ranges. A last-message GUID also exists on the shared
request class but this room-specific method does not use it. Recovery mode is
selected only after normalization leaves at least one valid selector.

Queries always constrain room ID and require ThreadId to be null. Timestamp
mode uses strictly later creation times. Results sort by room sequence and
then creation time, with a 1000-record controller limit. The header X-Total
and response total_count represent the prelimit count. The response cursor
is the creation instant of the last ordered result, or current server instant
if empty, rather than necessarily the maximum creation instant in the result.
The existing response fields are messages, current_timestamp and total_count.

## What prevents a faithful endpoint today

The selected source relies on SnChatMessage and shared base database types in
the NeTo submodule. Its complete field definitions and global query filters
have not been inspected here. It would be incorrect to guess deleted-row
visibility, add a guessed deleted_at filter, or advertise a minimal message
struct as the existing wire schema. Edits, deletion events, reactions and
thread hydration need golden fixtures and licensed shared-model evidence.

CurrentUser verification, ApiError serialization, account hydration and remote
permission middleware also live outside this port. A fake identity header or
successful no-op hydrator would not establish compatible authentication or
behavior. No such adapters were added.

## Other observed contracts

Inbound WebSocket event dispatch includes send, read, typing, subscribe,
unsubscribe, test, placeholder update and placeholder finalize. The packet
listener is non-JetStream with parallelism 8. Account deletion, presence and
bot configuration listeners use durable JetStream consumers with three
retries. Consumers rely on trusted account/device event context plus service
membership checks; this port does not establish that trust boundary.

The declared gRPC rewind service computes yearly summaries. Its registration
has not been established by the inspected middleware, which explicitly maps
controllers and gRPC reflection. A declared class alone is not proof of a
reachable endpoint.

PostgreSQL room counters allocate contiguous blocks during SaveChangesAsync;
room/sequence uniqueness and active-placeholder uniqueness are schema
contracts. Go's in-memory selection helper does not implement them.

## Next cohesive candidate

A read-only room-sync service/handler is a sensible next slice only after the
shared model, deletion filters, error encoding, auth provider and hydration
contracts are resolved. Then use a persistent adapter and full request/response
golden tests for private/public/encrypted rooms, left members, threads,
edits/deletions, invalid selectors, timestamp equality, empty results and the
1000-record boundary. A local prehydrated-record demonstration could be useful
but would be a separately labeled subset, not API parity.

## Source integrity and license

All code-source snapshots in `source/manifest.json` were checked byte-for-byte
against Git blob IDs at commit
`4ee2d0cebb9da8284a92483fee6dce795bf6947c`; SHA-256 digests are also recorded.
The repository's AGPL-3.0 license is included. No implementation from the
unlicensed ElecPostal repository was copied or translated.
