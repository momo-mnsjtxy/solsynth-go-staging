# Durable friend activity and status events

This separate change follows the frozen friend overview. It connects canonical
activity and presence mutations to the existing first-party notification event
stream, including `/api/realtime/replay`, WebSocket and SSE consumers. It does
not create a second client endpoint or an external notification provider.

## Source contract

The complete retained Dyson activity broadcast method proves the payload
`account_id`, `activities`, `timestamp`. The complete authored Passport startup
status broadcast method proves `account_id`, `status`, `device_id`, `timestamp`.
The complete pinned Solian friend_status_listener.dart consumes only
`account_id`/`status` or `account_id`/`activities`; it does not inspect device_id
or timestamp. Its event names are `account.status.updated` and
`account.presence.activities.updated`. The overview caller also invalidates its
cache for those exact names. These snapshots are hashed in the existing friend
source manifest and retain the AGPL license evidence.

Status events deliberately omit device_id. No canonical authored device
binding is available in this port, and credential session IDs are not device
identities. The retained first-party listener is compatible with this omission;
a third-party consumer that requires device_id needs an explicit adaptation.
No excluded Shared implementation, generated protocol, Socommon or Suki code
was read or copied to establish these dictionaries.

## Local durable flow

An activity mutation commits its source event and recipient admissions with the
canonical activity revision in the same transaction. Status create/update/clear
and authenticated heartbeat/disconnect operations likewise capture the original
source session, account version and durable authorization generation. Each
admitted recipient records both accepted friendship policy revisions. There are
no payload snapshots, inbox messages or provider delivery attempts in this
outbox. A failed outbox write rolls back the canonical mutation.

The existing notification worker imports finite batches into its configured
NotificationApp account stream. Source and recipient account locks are acquired
in sorted order before the admission row. The worker rechecks friendship,
blocks, current account/punishment eligibility, visibility, source admission,
activity revision and current artwork before appending an idempotent marker.
Stream sequence assignment remains serialized under the recipient account
lock. A failure rolls back marker, sequence increment and processed receipt;
another worker can retry. The worker has no new external sender or provider.
This deployment uses its existing single configured NotificationApp; this
change does not claim multi-app fanout from one source admission.

Replay freezes one bounded committed stream page, discovers every friend owner,
acquires its complete account lock set in sorted order, and then revalidates the
original reader proof. Friend markers are hydrated into current authorized
payloads in that same transaction. Later artwork contention is followed by a
second complete friend projection and original-proof check. Private activities,
private/invisible statuses and revoked/expired sources never become a default,
null or historical payload. Superseded activity revisions are suppressed.

A durable presence generation changes on preference, status and connection
writes, even when hidden. Together with both friendship policy revisions it
fences queued packets through hide/restore and unfriend/refriend transitions.
Private activity mutations create no recipient invalidation or cursor change.
Hidden transitions suppress old packets and create no hidden-state invalidation;
already cached client data cannot be recalled and is removed on the next
authorized overview refresh.

Before each WebSocket/SSE event write, the stream obtains another bounded,
current replay projection with the original proof. That transaction commits and
releases every SQL lock before the transport can block. This is a documented
last-observation boundary, not a claim that authorization cannot change after
observation or that bytes already in flight can be recalled.

Source fanout is limited to 1000 accepted friends, imports to 100 receipts per
call (32 in the existing worker), replay to its existing 100-event bound, and
payloads to the existing 64KiB stream limit. The limit applies to the final
encoded dictionary for both status and activity events; JSON escaping can make
valid status input exceed the event limit. A snapshot above the row or payload
bound is suppressed and its local admission is completed, so one
oversized source cannot poison the shared queue. A later bounded mutation can
emit a new event. No truncation or guessed partial activity dictionary is used.

## Verification boundary

Seven named PG cases cover canonical HTTP replay and exact dictionaries, source
and audience withdrawal/ABA, private invalidation absence, duplicate concurrent
imports, oversized snapshot suppression and recovery, mutation/import rollback, account and artwork waits, original reader
and source expiry, and saved restart replay. The oversized case also covers
status encoding expansion and subsequent bounded recovery. A single-connection
offline driver exercises the real source/status/queue methods, original source
admission and post-projection withdrawal, presence/friendship incarnations,
encoded payload suppression, stream/receipt rollback, retry and idempotency.
These tests do not establish PostgreSQL predicate or physical lock correctness.
A unit sink test verifies that a
queued packet is reprojected before writing, a later revoked packet is skipped,
and the validator has returned before transport execution. Tests start no new
socket, server, live provider or external notification.

The restart fixture saves original pre-target credentials, public-only verifier
keys, elapsed clock and exact stream page. Its after phase reuses those proofs
and existing durable rows without issuance, refresh, reseeding or authority
rewrite. Offline Go build/vet/unit/race and SQL checks are distinct from actual
PostgreSQL and process restart. Group adoption still requires independent
review; actual PG acceptance belongs only to coordinator private CI.
