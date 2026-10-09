# Independent bounded discovery correction

Frozen input: exact base `bc8e57409d54623c20c88c052dcc4bffff1807a9`, core
`6d66707aa8cf539f87e3e297487dae4cefe01be4`, host
`1611813d0c798b0ca4a607571379d1d79a2b92a4`. Both original patch hashes,
commit parent links, clean application preimages, and all 28 delivered file
hashes were independently verified. No dirty source was copied.

The original worker checked enabled accounts and session epochs but did not
reevaluate current permission grants and scopes. It also retained neither the
original ancestry deadline nor policy history, so revoke/restore transitions
could admit late observations. The correction uses canonical
`TransactionGuard.StoredSessionEligible` for current scopes, grants,
punishments and bounded, acyclic session ancestry. It records authorization
continuity epochs, preserving ordinary refresh while rejecting scope/revocation
changes. No bearer bytes or new verification capability are stored.

The optional discovery migration adds a small monotonic history fence for
account policy, group policy, publisher membership/owner/visibility, federation
enablement and blocks. It is deliberately independent of the optional blog and
factor schemas: those migrations are absent from this frozen base and their
domain dependencies do not cover federation block/enable transitions. Existing
canonical authority remains the only permission evaluator. Equivalent restored
policy, empty-policy transitions, disable/enable and block/unblock cannot revive
queued admission. Publisher renaming retains its existing anchored actor/key
aliases and does not itself revoke this identity-preserving observation.

Request/read capture history before canonical lock waits. Workers check it
before each GET and again after account, publisher and job-lock waits. The short
pre-fetch transaction closes before external work. A lost lease prevents another
GET. The earliest original session-ancestry expiration, next recorded policy
time boundary, or 24-hour cap is an immutable deadline; ordinary refresh cannot
extend it. Each job retains its original retry limit. Binding includes the local
origin, allowed origins and configured execution limits, so changed configuration
cancels existing work; restoring configuration cannot revive cancelled work.
The configured production key provider remains immutable and non-network.

The shared public-address guard also rejects deprecated IPv6 site-local and
reserved/unallocated blocks that Go classifies as global-unicast. A synthetic
resolver verifies mixed public/nonpublic DNS answers fail before dialing.

Strict host configuration rejects JSON case aliases. The private discovery POST
requires one application/json Content-Type and exactly one lowercase resource
field. These restrictions have synthetic HTTP/CLI regressions.

Four additional required actual-PostgreSQL cases:

- `TestPGFederationDiscoveryPolicyAndMembershipABA`: direct/group grants,
  group membership, owner, owner disablement, manager membership, federation
  enablement, blocks and session scopes before and during transport
- `TestPGFederationDiscoveryAdmissionWaitABA`: request and read admission
  reject a restored manager role after the canonical account-lock wait
- `TestPGFederationDiscoveryOriginalDeadlineAndConfiguration`: original
  ancestry expiration survives refresh extension, configuration changes cancel,
  and persisted limits cannot be rewritten
- `TestPGFederationDiscoveryCurrentPolicyAfterJobLockWait`: completion
  observes grant revoke/restore after its final durable job-lock wait

The original seven cases and terminal saved-state restart case remain required.
The restart marker still uses the shared exclusive marker helper and compares
original job/server rows after actual restart without seeding, migration, new
keys, new tokens or job recreation. The job row now also retains original
admission history, deadline and retry limit. Fixtures use existing synthetic
helpers, UUID/text casts, current role/quota prerequisites, microsecond-capable
PG timestamp comparisons and in-memory HTTP fetch doubles.

No actual PostgreSQL, live federation discovery, HTTP listener, production
provider, real credential or remote write was used. Final local validation and
exact file/preimage hashes are recorded in the review delivery manifest. Actual
PG remains required in coordinator CI. The combined grouping plan must assign
all eleven new discovery cases; its guard is expected to reject this standalone
candidate until that separate coordinator integration.

This correction preserves the existing opt-in pinned-observation scope. It does
not activate first-time remote admission, signed GET, private/realm federation,
remote media, server inbox processing, fetched-key signing authority, or full
ActivityPub parity.
