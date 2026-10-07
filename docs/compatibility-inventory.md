# Compatibility and verification inventory

Observed 2026-10-07. This workspace contains deterministic domain ports, a partial Passport board service, and a runnable loopback-only PostWatch preference slice with atomic local JSON persistence. It is not a production backend replacement. It does not establish whole-platform compatibility. Public GitHub file reads were used; the denied Gitea organization API was not retried through another identity or route. The Gitea public page could not be retrieved by this worker.

## Existing implementations and ownership

- [DysonNetwork](https://github.com/Solsynth/DysonNetwork) labels itself the previous-generation C# server. Its current tree includes Passport, Sphere, Messager, Wallet, Develop, and a pinned Shared submodule. A historical name in documentation does not prove a service still exists in the current tree.
- [Stargate](https://github.com/Solsynth/Stargate/blob/master/README.md) already replaces Padlock and Passport's account/profile/relationship domain in Go. Account board items remain Passport's responsibility. Do not duplicate the entire identity service from the remaining username helper.
- [DysonFS](https://github.com/Solsynth/DysonFS/blob/master/README.md) already implements file storage in Go, with master, worker, and storage modes and a legacy migrator. Media processing still needs ffmpeg/libvips. Workspace quota has a WattEngine dependency.
- [MetoerPush](https://github.com/Solsynth/MetoerPush/blob/master/README.md) is the Go Ring replacement, including notification, push, email, scheduler and delivery observability. Its existence makes a fresh Ring rewrite duplicative.
- [Blade](https://github.com/Solsynth/Blade) already implements the Go gateway, service discovery, health gating and WebSocket entrypoint.
- [Solsynth's repository listing](https://github.com/orgs/Solsynth/repositories) distinguishes Dart Solian, Vue FloatLand/Capital, Kotlin CloudySky and other clients from backend services. RoadSign is archived. A whole-backend Go effort need not rewrite these clients.

These main backend repositories identify AGPL-3.0 licenses; preserve original notices and check the actual license file at the pinned revision before redistribution. Client branding/assets have separate restrictions; language labels do not establish license permission.

## Cross-repository contracts requiring a pinned baseline

1. **Wire JSON is not uniform.** Stargate documents snake_case, integer enums, UTC RFC3339, omitted nulls and the `ApiError.traceId` exception. Metoer documents included nulls and snake-cased metadata dictionary keys. Its LastActiveEvent uses PascalCase. A shared global serializer would silently change behavior.
2. **Routing and discovery.** Blade transforms service prefixes to `/api`, preserves special discovery/federation routes and gates readiness on core services. Migrating a controller requires its old route, gateway alias and HTTP/gRPC destination together. Blade supports a Redis-backed leased registry and static fallback targets.
3. **Shared definitions.** [DysonNetwork AGENTS.md](https://github.com/Solsynth/DysonNetwork/blob/master/AGENTS.md) identifies NeTo as the Shared submodule and Spec as the protobuf source. Generated C# code is not the authoritative schema. Pin Spec, NeTo and Go SDK together and verify field numbers, enum values, presence, timestamps and service names before integration.
4. **SDK drift.** Connector-read go.mod files pin `src.solsynth.dev/sosys/go` to `40fa743b4bd0` in Blade/Stargate, `f626a5b8218f` in DysonFS and `e3430ca507d0` in Metoer. These are different contracts until verified. Blade/DysonFS require Go 1.26.2; Stargate/Metoer require 1.26.5. The small ports module's lower language version does not prove those services build.
5. **Cache/event compatibility.** Metoer documents C# HSET-envelope versus Go plain-value cache misses. Push delivery uses an in-process queue, while account activity uses JetStream and websocket fanout core NATS. Do not claim durable push retries or infer identical cache bytes.

## Schema and deployment gates

- Stargate documents an initial schema, data-copy command and restart ordering; its test helper warns about DDL containing DROP TABLE. Only use a disposable database for migration validation. Preserve IDs and authentication state; compare row counts, referential integrity and permissions after copy. No database was accessed here.
- Metoer documents idempotent DDL and temporary sharing of the Ring database. This requires independently verified concurrent-write and rollback behavior, not a generic database replacement.
- DysonFS has migration/cleanup tools with state-changing modes. No migration or cleanup command was run. Verify object references, quotas, derived files and ACLs on synthetic storage before cutover.
- The legacy [Compose file](https://github.com/Solsynth/DysonNetwork/blob/master/docker-compose.yaml) includes Redis, NATS JetStream, Aspire telemetry and .NET service configuration. It is not automatically compatible with Go TOML configuration, listener ports, TLS flags, mounted keys or shared service naming.
- Missing acceptance evidence: complete route manifest, executable legacy differential oracle, PostgreSQL migrations/rollback rehearsal, protobuf compatibility, cache/event integration, authenticated client flows, federation/media behavior, load tests, container builds and deployment health checks.

## Scope of local checks

`scripts/verify.sh` validates formatting, build, vet and ordinary/race tests for this workspace only. Contract tests use synthetic values with source-derived expectations, not captured user data or live endpoints. They do not execute C#, databases, Redis, NATS, gRPC or a client. Source-backed unit parity is a useful starting point, not service parity.

## Expanded checkpoint evidence

The pinned recursive DysonNetwork tree is untruncated and enumerates 98 controller files, five AppDatabase files, six C# projects, 23 RPC-named files, 25 job-named files and 261 migration files. See `inventory/dyson-contract-files.json`; filename counts do not equal endpoints or runtime services, and the Shared submodule is not expanded. `inventory/dyson-service-registrations.json` records literal registrations from the five service startup files; multiline and common registrations still need tracing.

Independent local HTTP contract tests cover GET/PUT response sources, partial/null update retention, fresh-store reopen persistence, account separation and authentication/permission denials. This supports only the development slice. VCS stamping is disabled during verification because this is a source-snapshot workspace, not a standalone Git checkout.

## PostgreSQL adapter checkpoint

The PostWatch PostgreSQL adapter and a source-matched disposable schema fixture now compile. Six real-database test cases are prepared for account separation, cross-instance patch concurrency, rollback, soft deletes/duplicates, identity/timestamps and server restart. They did not run: fresh-cluster initialization passed, but PostgreSQL could not create a private Unix socket (EPERM), including a reviewed retry. A bounded /tmp diagnostic also failed at AF_UNIX socket creation before bind. No TCP workaround was attempted. Ordinary test runs skip these six cases; package-level `ok` is not database validation. See `docs/postgres-integration-result.json`.
