# Explicit first-contact public federation admission

This isolated continuation starts at reviewed commit
`bf7a7b7e3831081336e2129553c3627e8afcf419`. It adds runtime code and synthetic
verification fixtures only. No live actor discovery, remote enrollment, provider,
production service, HTTP/DB socket, real credential or remote write was used.
Actual PostgreSQL execution remains a coordinator gate.

## Authored scope and evidence

The licensed DysonNetwork revision is
`4ee2d0cebb9da8284a92483fee6dce795bf6947c`, AGPL-3.0-only. Relevant authored methods
in the already retained complete `ActivityPubDiscoveryService.cs` were inspected:
`DiscoverActorAsync`, `SearchActorsAsync`, `GetActorFromWebfingerAsync`, WebFinger
JSON/XML parsing, `FetchActorDataAsync`, and `GetOrCreateActorWithDataAsync`.
The complete authored `ActivityPubFollowController.cs` was also inspected.
Their observations are discovery-to-actor resolution, key import, stored actor
lookup and federation relationships. The source also restores deleted actors,
imports changing unsigned keys, crawls metadata and removes remote data after
some HTTP errors. Those behaviors are deliberately not reproduced.

Exact retained source provenance:

- `ports/federation/discovery-evidence/ActivityPubDiscoveryService.cs`: 50,591
  bytes; SHA-256 `70a4f7de59a3399c5a07668e970f0e37bc999108889c1d9bbc2c998f44d0333e`;
  Git blob `172d428a1c4e0f2c5e2d6a33cb670bc7930d5e48`;
  [pinned authored source](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/ActivityPubDiscoveryService.cs).
- `inventory/operation-sources/DysonNetwork.Sphere/ActivityPub/ActivityPubFollowController.cs`:
  SHA-256 `c4911f75f87d88658d7cad1f46bb7fca3712d604d1702251dfaf6f0caf7fd525`;
  [pinned authored source](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/ActivityPub/ActivityPubFollowController.cs).
- Pinned root license: SHA-256
  `0d96a4ff68ad6d4b6f1f30f713b18d5184912ba8dd389f86aa7710db079abcb0`;
  Git blob `be3f7b28e564e7dd05eaf59d64adba1a4065ac0e`.

Official references read on 2026-10-09: [ActivityPub](https://www.w3.org/TR/activitypub/)
(actor identity, follow lifecycle, verification and localhost security),
[RFC 7033](https://www.rfc-editor.org/rfc/rfc7033.html) (HTTPS WebFinger subject
and self-link resolution), and [HTTP Signatures draft 12](https://datatracker.ietf.org/doc/html/draft-cavage-http-signatures-12)
(the already implemented bounded RSA signing profile). The last reference is an
expired draft, not RFC 9421. New trust policy, queues, audit, persistence and
incarnation rules are original owned design, not full C# or ActivityPub parity.
No excluded Shared/protocol definitions, generated bindings, canceled SoSYS or
Socommon repositories/mirrors, or unlicensed Suki implementation were consulted.

## First contact, then follow

An operator must explicitly configure a canonical HTTPS public origin and a
non-secret UUID incarnation, apply `PublicAdmissionSchema` after `DiscoverySchema`,
and initialize that exact policy. Startup never migrates or initializes policy.
Missing configuration, unsupported key provider, absent policy, revoked policy,
or mismatched incarnation fail closed. Construction performs no network I/O.

An authenticated joined publisher manager with `publishers.update` can POST to
`/api/activitypub/publishers/{uuid}/public-admissions` with exactly:

```json
{"resource":"acct:alice@public.example.test","actor":"https://public.example.test/actors/alice"}
```

The caller nominates an exact actor URI as well as the handle. This is a bounded
first-contact path, not arbitrary search. GET of the same route accepts exactly
`resource` and `actor` query parameters and returns the original job. A manager
may then use the existing `follow` relationship operation for the admitted actor.
Admission alone neither sends Follow nor marks a relationship accepted.

An enabled maintenance worker fetches precisely the resource's WebFinger and the
nominated actor, using one original timeout. It requires one exact matching JRD
subject/self link, actor ID, preferred username, same-origin inbox and owned
RSA public key. The supported initial key ID is exactly `{actor}#main-key`.
Aliases, moved actors, cross-origin links, alternate key anchors, ambiguous JSON,
wrong media and malformed/oversized documents fail closed. It does not fetch
contexts, aliases, standalone keys, images, metadata, outboxes or collections.
Fetched text is bounded and escaped.

The production fetcher has no proxy, cookies, credentials or signatures. Every
GET validates every DNS answer as public, then dials the selected numeric IP
with original TLS SNI. IPv4/IPv6 loopback, private, link-local, metadata, shared,
reserved and documentation networks are excluded. Redirect budget is zero:
all redirects fail without following or resolving their destinations. Headers,
body (1 MiB), TLS and time are bounded. Transport tests use only in-process fake
resolvers and RoundTrippers. Peers requiring redirects, alternative key IDs or
signed GETs remain unsupported.

The shared destination predicate also rejects Azure's `168.63.129.16` virtual
platform address, including IPv4-mapped IPv6 answers. This is a host-platform
destination despite its public IPv4 form; see
[Microsoft's platform-address documentation](https://learn.microsoft.com/en-us/azure/virtual-network/what-is-ip-address-168-63-129-16).
The restriction is verified before the synthetic RoundTripper can run; it does
not claim an HTTPS exploit against Azure's documented non-HTTPS services.
The review also excludes the remaining AS112/AMT special-purpose allocations
listed by the [IANA IPv4 registry](https://www.iana.org/assignments/iana-ipv4-special-registry/)
and [IANA IPv6 registry](https://www.iana.org/assignments/iana-ipv6-special-registry/),
including allocations that are globally reachable. Mixed-answer regressions
cover every listed special-purpose category, IPv4-mapped representations,
multicast and reserved/site-local ranges. These are explicit reviewed bounds;
future registry or hosting-platform changes require updating the predicate.

## Durable authority and trust boundaries

Jobs copy original owner, manager account version/session authorization epoch,
ancestry/policy deadline, monotonic policy history and retry budget. They reuse
the reviewed discovery authority evaluator without altering pinned observations.
Every GET checks current lease, origin configuration and stored admission outside
SQL locks. Completion locks canonical rows, rechecks after job/advisory waits,
and atomically saves the public key and success audit. A final check after
key/audit insert waits rolls back the tentative grant before cancellation when
policy or the original deadline changed. Reconfiguration never extends old jobs.
Transient failures have bounded retries; terminal outcomes and their retry
budgets cannot be deleted or reset through the maintained schema.

The public key ledger is separate from accounts, publisher memberships, local
signers, sessions, grants and permissions. Remote public keys never issue local
credentials or local authority. Unique permanent reservations cover actor URI,
case-folded resource, key ID, fingerprint and per-admission incarnation. Existing
peer identities also block first-time import. Concurrent claimers cannot share a
lease; competing identity/key reservations cannot overwrite a winner.

The base provider must explicitly implement `FirstContactProvider`. A lookup
error never means consent to enroll. The preliminary check has IDs only; the
completion check includes candidate key bytes and rejects current/retired pins.
The standalone provider checks configured actor/key/fingerprint collisions and
explicit retired actor/key-ID/fingerprint entries. The optional
`retired_remote_key_fingerprints` array contains lowercase SHA-256 hashes of the
PKIX DER public key, as returned by `KeyFingerprint`; it prevents retired key
material from reappearing under a different actor and key ID. Each retirement
array is limited to 1,000 entries, and duplicate, malformed or simultaneously
trusted fingerprints are rejected. Operators must retain the complete retirement
history for static pins. A durable peer row reserves its actor URI but does not
retain retired static fingerprints or all former key IDs; these remain the
provider's responsibility. Admitted keys retain their own durable reservations
regardless of file changes. Omitted or historically lost static retirement data
cannot be reconstructed by this module. Do not implement `FirstContact` by
converting all lookup errors to true.

Admitted-key lookups consult durable records and current policy on every lookup;
no cache or signature-failure-triggered fetch exists. Revoked records take
precedence over newly configured static keys, including alternate key IDs or
actors that reuse a durable actor/key/fingerprint reservation. A ledger lookup
failure cannot authorize fallback to a configured pin. Inbox verification retains an
incarnation, and its transaction rechecks it. Follow, quote and delivery paths
hold the admitted key/policy fence through their bounded mutation/send. Key or
origin revocation is audited and permanent in this slice. Re-enablement, alias
migration and key rotation require a separately designed explicit trust
transition; no restore or automatic rotation endpoint exists. A succeeded job is
a historical record and is not a claim that its key remains currently trusted.

Removing `public_admission` does not remove existing trust reservations. The
repository's verifier and internal remote lookups retain a read-only ledger
wrapper in static-only mode. If the optional ledger has never been installed,
ordinary static pins retain their previous behavior; an actual schema-query
failure fails closed. Absence is checked again on later lookups, so it cannot
hide a subsequently installed ledger. Unrelated unreserved static pins remain
usable. No constructor migration or policy initialization is performed.

## Runnable configuration without credentials

`examples/federation-public-admission.json` is a complete strict config for
validation and policy initialization. Its UUID is a public synthetic identifier.
It contains no signer, password, token or deployment credential. The example
origins are intentionally reserved, not live test targets.

```sh
go run ./cmd/backend initialize-federation-public-admission \
  --federation-config "$PWD/examples/federation-public-admission.json"
# Prints validation success without opening a database or contacting a peer.

# Only an operator in an authorized disposable/deployment environment applies:
# go run ./cmd/backend initialize-federation-public-admission \
#   --federation-config /absolute/federation.json --apply --dsn-env SOLSYNTH_BACKEND_DSN
# Backend also needs its ordinary host/authority settings and existing signers:
# --enable-federation --federation-config /absolute/federation.json --federation-admit
```

`--federation-admit` is independent of `--federation-discover` and
`--federation-send`. Without it, admitted key reads and authenticated queue/read
operations can be configured, but no remote admission GET executes. Existing
`remote_keys`, `discovery_targets`, server actor and send configuration keep their
separate meaning.

The offline-by-default `revoke-federation-public-admission` command takes the
same config, `--origin` and optional `--actor`. Adding `--apply --dsn-env ...`
revokes that exact key; omitting actor revokes the whole origin policy. Both
retain permanent reservations. Neither command generates credentials or runs
remote discovery.

## Verification gates

Mandatory actual PostgreSQL cases are registered separately from the runtime:

- `TestPGBackendFederationPublicAdmissionComposition`
- `TestPGFederationPublicAdmissionLifecycleAndIsolation`
- `TestPGFederationPublicAdmissionRevocationAndPermanentReservations`
- `TestPGFederationPublicAdmissionAuthorityABAAndOriginalDeadline`
- `TestPGFederationPublicAdmissionRetryLeaseAndLateResults`
- `TestPGFederationPublicAdmissionConcurrentClaimsAndCollision`
- `TestPGFederationPublicAdmissionAtomicAuditRollback`
- `TestPGFederationPublicAdmissionRequestWaitABA`
- `TestPGFederationPublicAdmissionCurrentPolicyAfterJobWait`
- `TestPGFederationPublicAdmissionCurrentPolicyAfterAuditWait`
- `TestPGFederationPublicAdmissionOriginalBoundsAndConfiguration`
- `TestPGFederationPublicAdmissionSavedRestartPersistence`

The last case is required before and after a real database restart. Its exclusive
saved marker records original job, key, policy and requester session rows. The
after phase reads those exact rows and resolves the original key in a new
repository. It re-verifies an unchanged signed request saved before restart at
its original synthetic verification time and rejects a changed key ID, without
migrations, reseeding, issuing a token, generating keys or replacing the marker.
This proves retained key/proof reconstruction, not indefinite request freshness.
The registry prevents a skipped case from counting as a
pass. The coordinator must assign these cases to the dependency-aware CI groups;
the inherited discovery-review cases also await that grouping rebase.

Local checks cover all-package build/vet/normal/race, focused final changed
packages, source hashes, PG16 schema/PLpgSQL syntax, static SQL argument counts,
migration ordering/atomicity and result-registry negative controls. Exact logs,
commit preimages and resulting hashes are in the separate delivery manifest.
These checks do not establish PostgreSQL catalogs, transaction behavior or
restart success; the required real coordinator run remains outstanding.

The restart signature check distinguishes persisted key mathematics at the original signing time from current admission. A second verification advances by actual elapsed wall time; expired five-minute HTTP signatures remain rejected, and an explicit expiry regression never issues a replacement request. No restart receipt claims an old signature becomes newly authorized.
