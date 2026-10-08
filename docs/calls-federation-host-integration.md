# Calls and federation in the unified host

This integration composes the completed call and ActivityPub adapters into the
canonical backend. Its base is `82afbbd1128f52430cc6ff691015b91e364ed596`.
It does not enable either provider by default, generate keys, create a remote
account, contact a provider, change a publisher's federation setting, or claim
media-plane/client parity.

## Application configuration

`application.Config.Calls` accepts an explicit provider, participant token TTL
(15 seconds–5 minutes) and call TTL (at most 24 hours). The canonical room's
unencrypted-mode policy and existing notification namespace are reused.

`application.Config.Federation` accepts the explicit federation policy, pinned
key provider, and optional transport. A nil transport leaves durable deliveries
pending while permitting configured public/inbox/management operations. Both
modules require the authority and domain to share the same pool and transaction
guard; the non-atomic-authority escape hatch does not apply to them.

Unconfigured families return explicit `CHAT_CALL_PROVIDER_UNCONFIGURED` or
`FEDERATION_UNCONFIGURED` 503 errors. Configured calls route before ordinary room
routes at `/api/chat/realtime/...` and `/messager/chat/realtime/...`. Federation
public routes bypass bearer middleware and path rewriting so HTTP signatures
cover their original host, request target and body. Authenticated management
routes use the private verified host identity and same-transaction proof binding.
Public federation routes have no gateway aliases.

`Ready` checks the enabled optional schemas without contacting external
providers. `ProcessDue(ctx, limit)` includes call maintenance/event projection,
federation post reconciliation and, only with a transport, outbound delivery.
Every batch is limited to 1–100. Errors are joined rather than making one
module's failure skip unrelated modules.

`ProcessExternalDue` supports a faster independent provider cadence. The
executable invokes it every five seconds by default when either module is
configured; `--external-maintenance-interval` is bounded to 1–30 seconds. The
existing settlement interval remains unchanged. This separates revocation
maintenance from a slow settlement batch. Each run has a 20-second context;
individual provider operations have their existing bounded contexts. A
settlement run's combined `ProcessDue` may overlap the independent worker;
durable module idempotency and locks handle repeated runs.

## Explicit executable setup

Calls require all of:

- `--enable-calls`
- `--livekit-endpoint=wss://the-provisioned-endpoint`
- `--livekit-api-key-env=NAME_OF_EXPLICIT_VARIABLE`
- `--livekit-api-secret-env=NAME_OF_EXPLICIT_VARIABLE`
- `--livekit-cloud-revocation`

The last flag is an operator assertion that the configured deployment supports
the LiveKit Cloud revocation contract. Self-hosted participant removal is not an
equivalent guarantee. Keys/secrets are read only from the explicitly named
environment variables, not flags/config payloads or discovered credentials.
The transport disables ambient proxies, uses TLS 1.2 or newer and has bounded
response headers/timeouts. A validation-only invocation does not open a socket.

Federation requires `--enable-federation` and
`--federation-config=/absolute/path/config.json`. Its strict JSON structure is:

```json
{
  "origin": "https://local.example.test",
  "allowed_origins": ["https://remote.example.test"],
  "max_attempts": 3,
  "delivery_timeout_seconds": 5,
  "local_keys": [
    {
      "publisher_id": "00000000-0000-4000-8000-000000000001",
      "private_key_file": "/explicit/private/publisher-key.pem"
    }
  ],
  "remote_keys": [
    {
      "actor": "https://remote.example.test/actors/example",
      "inbox": "https://remote.example.test/inbox",
      "key_id": "https://remote.example.test/actors/example#main-key",
      "public_key_file": "/explicit/trust/remote-public.pem"
    }
  ]
}
```

These are illustrative names/IDs, not working credentials. Existing local keys
are bound by canonical publisher ID, never account ID. Private-key files must
be regular, absolute paths with no group/other permissions. Remote actor, inbox
and key owner/origin bindings are checked against the explicit allowlist. No
remote discovery occurs. Duplicate/unknown JSON fields and duplicate key/actor
bindings are rejected. Configuration is capped at 1 MiB, 128 remote origins and
1000 local/remote key entries. No credential or provider response is logged.

Only `--federation-send` constructs the pinned HTTPS outbound transport. Without
it, queued activities are not sent. Construction is still offline with that flag:
network work begins only when the explicitly served host runs its worker.
Existing per-publisher management opt-in remains necessary before an actor or
its posts become available to federation.

## Explicit migrations and CI

The disposable runner installs:

- `internal/federationpg/schema.sql` after authority/accounts/posts
- `internal/callpg/schema.sql` after authority/chat/notifications

Schema-order checks reject missing, duplicate or reordered dependencies. No
startup migration was added. Required-result inventory adds all six existing
call tests, seven existing federation tests, the composed host test and the
combined persistence test. The two federation/host persistence cases are also
required after a real PostgreSQL stop/start.

New composed tests:

- `TestPGBackendCallsFederationComposition`: explicit optional provider
  activation, canonical publisher quota/creation and named post selector,
  authorized private federation settings, unchanged signed public inbox routing,
  synthetic Accept/Create delivery, call admission, notification replay
  projection, revoked-session cleanup, owner termination and wrong-pool denial
- `TestPGBackendCallsFederationRestartPersistence`: both optional modules'
  durable state before and after the disposable database restart

Fixture repairs are limited to `tests/integration/calls_test.go` and
`federation_test.go`: synthetic name prefix, explicitly fixture-only publisher
constructor, deterministic post/audit timestamps pinned to the injected clock,
and microsecond-truncated provider observation timestamps. Call ban equality
already compares PostgreSQL-read timestamps. `JoinedAt` from the fake provider is
observational metadata, not an authorization or expiry input. No runtime clock
was replaced by a client timestamp.

Executed locally: focused eight-package race tests, `go vet` for these packages
and integration tests, all-integration compilation, and offline wiring/required
result guard checks (192 before, 29 after in this isolated base). The wiring
check verified 30 schemas/128 tables. No PostgreSQL or HTTP listener was started,
and no real provider was contacted. Actual disposable SQL, loopback and restart
execution remain required in parent CI; package success is not a SQL pass.

## Merge boundaries

The delivery manifest separates domain dependencies from this host integration.
Import the calls/federation domain deliveries first, then apply the host patch.
`backend.go`, `router.go`, `readiness.go`, the executable worker block and required
schema/test inventories overlap other active integration work. Merge their new
external-module fields/hooks with existing location and progression wiring;
do not replace those files wholesale. In particular, retain all existing
`locationRepo.Expire` and `progressionRepo.ProcessDue` calls when merging the
combined worker. No location/presence/progression source is included here.
