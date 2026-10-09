# Server actor and bounded discovery continuation

This addition starts at exact Go commit `bc8e57409d54623c20c88c052dcc4bffff1807a9`.
It adds first-party discovery endpoints and an explicitly configured remote
profile observation flow. Existing publisher actor anchors, retained aliases,
signed inbox verification, quote grants, follow relationships, and delivery
semantics remain the authority boundaries.

## Licensed evidence and observed compatibility

The complete authored `ActivityPubDiscoveryService.cs` was retrieved at
DysonNetwork `4ee2d0cebb9da8284a92483fee6dce795bf6947c`, read, and verified against
Git blob `172d428a1c4e0f2c5e2d6a33cb670bc7930d5e48`. Exact byte count/SHA-256, URL,
and the pinned AGPL-3.0-only license are retained in
`ports/federation/discovery-evidence/manifest.json`. Complete already retained
`ServerActorController.cs`, `ServerActorResponse.cs`, `ServerSigningKeyService.cs`,
`WebFingerController.cs`, `FediverseKeyController.cs`, `FediverseKeyAdminController.cs`,
and `FediverseActorController.cs` were also inspected; their existing operation
and quote manifests supply exact upstream blobs/hashes. No Shared declarations,
generated bindings, excluded repositories, or live federation peers were read.

The source exposes an Application server actor, public key document and empty
collections; server/publisher WebFinger, XML host-meta, and publisher main-key/
publickey aliases. Discovery callers resolve handles, retrieve actors, import
profile/key fields, retrieve counts and instance metadata, and sometimes fetch
remote outboxes. The authored service follows returned URLs, imports public
keys from unsigned documents, creates/restores canonical publisher rows, and
can remove actors/posts on a generic 4xx. Those are observations of the source,
not acceptable trust or deletion policies for this rewrite. Server signing-key
generation/rotation and signed remote GET callers are evidenced but not activated.

Official references consulted on 2026-10-09:

- [RFC 7033](https://www.rfc-editor.org/rfc/rfc7033.html), sections 4 and 9:
  HTTPS WebFinger, JRD subject/self links, and information reliability.
- [W3C ActivityPub](https://www.w3.org/TR/activitypub/), actor and collection
  properties. These bounded routes do not claim full protocol conformance.

The new safety policies, request routes, queue representation, public-address
restriction, immutable pins, and authorization gates are independently authored
owned behavior. They are not represented as C# differential parity.

## Public routes and permanent identity

With optional `server_actor` configuration and explicit initialization:

- GET `/activitypub/actor` exposes the configured Application actor
- GET `/activitypub/actor/main-key` exposes only its public RSA key
- GET `/activitypub/actor/outbox`, `/followers`, `/following` return empty ordered
  collections; `?page=true` returns the corresponding empty page
- GET `/.well-known/webfinger?resource=acct:configured-server-name@local-domain`
  resolves that server actor, including case-insensitive handle lookup

The source advertises an inbox while implementing no server inbox controller.
This continuation also advertises its canonical inbox, but returns 422 for that
unsupported surface; it does not accept anonymous server activities.

GET `/.well-known/host-meta` emits XML pointing to the canonical local WebFinger.
GET `/activitypub/actors/{name}/main-key` and `/publickey` use the existing
publisher actor checks and return that actor's anchored key ID/owner. A renamed
publisher's old and new names continue resolving to the same stable actor/key.
Disabled/deleted/shadowbanned publisher behavior stays with the existing actor
repository, and private/realm posts are never projected by these routes.

`InitializeServerIdentity` stores an immutable actor URI, key ID, public-key
fingerprint and exact username. It does not generate, import or store private
credentials. Configuration changes cannot silently rewrite the anchor.
The server handle is permanently reserved against all existing and future
publisher handles, including retired aliases. Server initialization and
publisher-name insertion share a per-name transaction advisory lock; SQL guards
reject collisions in either direction, even for direct maintained-table writes.
Changing a server display name/summary leaves its immutable identity intact.
Disabling the optional configuration does not release its reserved handle.

## Configured remote observations

POST `/api/activitypub/publishers/{uuid}/discovery` accepts exactly
`{"resource":"acct:alice@remote.example.test"}`. The request requires one application/json Content-Type and the exact lowercase
resource field. The existing bearer proof,
same-pool transaction guard, `publishers.update` permission, current enabled
publisher/owner and joined manager membership are mandatory. Requests are
limited to the exact configured target handles. They enqueue an observation
without network I/O. GET of the same route with one `resource` query parameter
returns that publisher's job and, after success, its bounded projection.

There is one retained job per publisher/handle, at most 128 per publisher.
Repeated requests return the same job; they never reset terminal failures or
its retry budget. This initial observation API does not promise profile refresh.
Results are explicitly labelled `unsigned_document_matches_configured_pin`.
They are separate from canonical accounts/publishers/posts, `RemoteKey`, inbox
proofs, peer enrollment, follow consent, and quote ownership. Discovery grants
none of those forms of authority and does not change the key provider.

A worker performs precisely two GETs: the configured handle's WebFinger URL and
its configured actor URI. The returned subject must match the requested handle;
there must be exactly one ActivityPub self link, equal to the configured actor.
Actor ID, username, inbox, key ID, key owner, and RSA public-key fingerprint must
all match the provisioned identity. Case-shadowed/duplicate JSON, oversized or
wrong-media responses, cross-origin redirects/links, ambiguous self links and
identity drift fail closed. Names and summaries are bounded and escaped.
Contexts, aliases, remote key URLs, media, instance metadata, followers and
outboxes are never recursively fetched. No fetched document can delete data.

Construction takes no network action. Only `--federation-discover` constructs the
optional production fetcher. The host's explicit maintenance worker then calls
`ProcessDiscoveries`. The fetcher has exact URL/Accept allowlists in addition to
configured HTTPS origins, no ambient proxy/auth/cookies, no redirects, TLS 1.2+
and bounded headers/body/time. It validates every DNS answer as public unicast
and dials the validated numeric address while preserving the TLS hostname,
preventing a second-resolution rebinding. The DNS policy is shared with the
existing delivery transport; private, loopback, link-local and reserved ranges
are rejected. GET requests are unsigned in this slice; peers requiring signed
fetches fail without a fallback or key generation.

## Durable work and freshness

The worker atomically claims due jobs using `SKIP LOCKED`, increments attempts,
and persists an expiring lease. It holds no SQL transaction/lock during either
remote GET. It checks the queued owner, enabled publisher/owner, manager
membership, original requester account version/session epoch and session
ancestry, current blocks and active account/permission punishments before fetch.
Completion locks canonical account/publisher rows, repeats current policy in
fresh statements after waits, verifies the exact lease and current pinned key
binding, and atomically saves success, bounded retry, terminal failure or
cancellation. Stale results cannot become authority or change the remote pin.
GET/POST also recheck the current key binding after authority/row-lock waits.
A reconfiguration with a different allowed origin, actor, inbox or key cannot
resume a previous job as if it represented the same identity.

408/429/5xx and transport outages use the existing bounded exponential delay;
redirects, other 3xx/4xx, malformed/oversized responses and identity drift are
terminal. A crash leaves its lease recoverable with the same job ID. Attempt
exhaustion, including an expired final lease, is terminal. Cancellation during a
network request may still allow that already-started public GET to finish, but
its stale result is discarded. Monotonic history now cancels original work after relevant revoke/restore
transitions, including those occurring wholly between observations. Original
session-ancestry/policy deadlines and retry limits are immutable; refresh cannot
extend them. See `federation-discovery-review.md` for the bounded correction.
The existing delivery worker's separate locking policy is unchanged.

Apply `internal/federationpg/discovery_schema.sql` atomically after canonical
accounts/sessions/publishers, publisher-name reservations and federation identity.
It adds three owned tables and collision/immutability guards. It is deliberately
an optional explicit migration, separate from legacy `federationpg.Schema`.
`NewWithDiscovery` does not migrate or initialize it. Optional host readiness
requires the new tables and, for a configured server, a matching initialized
anchor. The disposable runner and migration-order/atomicity guards include it.

## Runnable configuration

The ordinary backend JSON federation configuration accepts these additions:

```json
{
  "server_actor": {
    "username": "solar-network",
    "name": "Example server",
    "summary": "Example federation node",
    "public_key_file": "/explicit/public/server-public.pem"
  },
  "discovery_targets": [
    {"resource": "acct:alice@remote.example.test", "actor": "https://remote.example.test/actors/alice"}
  ]
}
```

The existing origin, attempt limit, timeout, allowed origins and key entries are
still required. Every target must match an existing `remote_keys` actor/key/inbox
binding. The examples are illustrative, not working endpoints or credentials.
A server-only configuration may omit local publisher signers. Server key files
must contain public material; no private key is accepted for that field.

```sh
go run ./cmd/backend initialize-federation-server --federation-config /absolute/federation.json
# After explicit migrations, an operator can initialize the permanent anchor:
go run ./cmd/backend initialize-federation-server --federation-config /absolute/federation.json --apply --dsn-env SOLSYNTH_BACKEND_DSN
# Add these to the ordinary backend's explicit host/authority arguments:
# --enable-federation --federation-config /absolute/federation.json --federation-discover
```

Without `--apply`, the initializer validates files/configuration and never opens
a database. Normal server startup never creates the anchor. Without
`--federation-discover`, requests can be queued/read but no discovery transport
runs. `--federation-send` remains a separate outbound activity-delivery switch.

## Verification and remaining scope

Required disposable PostgreSQL cases:

- `TestPGFederationServerDiscoveryAndReservedIdentity`
- `TestPGFederationDiscoveryLifecycleAndIsolation`
- `TestPGFederationDiscoveryRetryLeaseAndAtomicRollback`
- `TestPGFederationDiscoveryRechecksOwnerSessionBlockAndPins`
- `TestPGFederationDiscoveryConcurrentClaim`
- `TestPGFederationDiscoverySavedRestartPersistence`
- `TestPGBackendFederationDiscoveryComposition`

The saved-state restart case records exact successful-job and permanent-server
rows in an exclusive marker before actual database stop/start. The after phase
reads and compares that state, without re-seeding, migrating, key generation or
job creation. Synthetic transport tests pause a fetch while actual SQL mutations
complete, covering owner changes, disablement, membership/session revocation,
blocks, key drift and competing workers. No test in this addition opens an HTTP
listener or contacts a real remote actor.

Local checks and real PG execution are reported separately in the delivery
manifest. Successful compilation/skipped integration tests are not database
validation. The combined coordinator must assign these cases in the active
group-plan/dependency audit and run the required before/after CI suites.

Unsupported here: arbitrary remote search, trust bootstrapping, automatic remote
key import/rotation, signed GETs, recursive actor/instance/media/outbox discovery,
profile refresh, realm/server inbox activity processing, private federation,
media forwarding, key administration, and full C# or ActivityPub parity. Existing
NodeInfo does not advertise these as completed capabilities.
