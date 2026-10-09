# Bounded federation withdrawal receipts

This owned continuation adds an opt-in, content-free Tombstone lane. Normal
publication still uses the unchanged `deliveryAllowed` authority. Publisher
shadowban invalidates the original publication intent and cannot be reversed to
revive it. Unshadowban requires a fresh authenticated settings operation before
new publication; existing normal jobs remain fenced.

## Explicit activation

`Config.CleanupPolicyID` is empty by default. `New`, `Ready`, settings and worker
calls never activate it. The repository-only
`ActivateCleanupPolicy(ctx, actor, publisherID)` operation requires the original
canonical same-pool root ordinary proof, the publisher owner with active manager
membership, `publishers.update`, and a current matching
`federation.cleanup.activate` grant. Canonical wildcard grant and deny semantics
apply, but a grantless superuser cannot satisfy the separate activation grant.
Root type, no parent/client, owner, membership and explicit grant are checked
again after policy writes and their possible waits in the same transaction.
There is no HTTP activation route or automatic configuration adapter in this
patch. Activation has occurred only in synthetic test code.

Policy incarnations are permanent per publisher and UUID. Activation records
an exact normalized binding of local origin, allowed origins, timeout, attempt
cap and owned cleanup protocol version. A change must be activated using a fresh
UUID, including a restoration of earlier configuration. Activation retires the
previous incarnation; neither its ID nor its receipts can be reactivated. The
same still-active exact ID/binding is idempotent. Configure a new ID before
activating a changed transport policy; operating with changed config but an old
ID suppresses receipts and cleanup. Omission also suppresses them. A transient
configuration change entirely outside any observation and without the required
explicit activation cannot be detected; operators must follow the lifecycle.

## Historical authority and its limits

The short normal dispatch-observation transaction records an immutable receipt
at the same commit as the live dispatch lease. Only Create/Update of a canonical
local Note can produce a receipt. No receipt is reconstructed from status,
attempt counts or the mutable `dispatch_started_at` field. Existing deliveries
and earlier sends receive no backfill. The receipt means bytes may have left,
including acceptance followed by a lost reply, crash, or local cancellation.
It does not prove delivery or peer acceptance. A crash before the transport call
is indistinguishable from an uncertain send after this observation boundary.

The receipt references the immutable source delivery binding and fixes the local
actor, key identifier, object, publisher name, transport incarnation, authority
history and database-clock deadline. Update/delete triggers prevent receipt or
policy rewriting, and the queue INSERT trigger permits only its exact
content-free Tombstone shape with matching original destination/key/budget.
Cleanup is one stable Activity ID per receipt, with a unique queue reference.
Replays, hidden edits and retries cannot reset its attempts or deadline.
Receipt-backed queue rows also reject deletion, retaining their unique receipt
reservation after terminal results so reinsertion cannot reset the attempt cap.
The deadline is no later than the original source intent deadline and 24 hours
after dispatch. It may be much shorter because of a session or policy boundary.

The separate publisher cleanup generation excludes only shadowban transitions.
Owner/type/realm/name/deletion changes, active membership changes and federation
enable toggles remain fenced, including change-and-restore cycles. Original
requester and owner account epochs retain grant, punishment, group and session
history. The original ordinary session and its ancestry, scopes, current policy,
owner, manager membership and deadline are still required. Thus credential
revocation can intentionally leave cleanup undelivered. Cleanup is not an
impersonation or permission-recovery mechanism.

Current configured egress, exact original local signer fingerprint, remote
actor/inbox/key fingerprint/incarnation and durable public-key reservations are
checked before transport. Relationship/block history remains mandatory. A
renamed publisher, rekeyed peer, retired transport policy, operator disable,
revoked session, private-to-public restoration or expired receipt suppresses the
old Tombstone. Static key providers have no historical key generation; an
unobserved key change away and back retains the pre-existing limitation.

Private post bytes are never used to construct cleanup. Only receipt object and
actor identifiers enter the Tombstone. Never-dispatched posts grant no cleanup
authority. Legacy post Delete jobs without a receipt fail closed. Quote withdrawal
has its existing separate authority and is unchanged.

## Execution and validation

The receipt and cleanup migrations are additive in `delivery_schema.sql` and
remain explicit; `federationpg.Schema` includes them for disposable fixtures.
Readiness checks the three new tables, queue reference and six new triggers.
All transactions close before transport. Existing lease ownership, stale-worker
acknowledgement fencing, fixed attempt cap, backoff and at-least-once ambiguous
semantics apply to cleanup too. No network transaction, recall or exactly-once
claim is made.

Receipt insertion can itself wait. The dispatch observation and provisional
receipt are enclosed in a savepoint, and the original normal intent is checked
again after insertion. Denial rolls both back before committing cancellation;
it cannot retain a receipt stamped with newer authority history.

New required PostgreSQL regressions cover ambiguous accepted dispatch versus
never-dispatched/default-off/unactivated cases, content-free stable retry and
replay, tamper rejection, deadlines and stale lease results, session revocation
during transport, authority/name/key/policy changes, and explicit activation
proof/grant/scope/delegation/wait failures. All transports are synthetic local
adapters. Local package success without `PG_TEST_DSN` is compilation and skip
evidence only; real PostgreSQL execution belongs to coordinator private CI.
No production, external recipient, operator activation, real credential, or
excluded protocol/source material was used.
