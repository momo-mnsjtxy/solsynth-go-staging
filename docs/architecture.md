# Backend architecture: staged compatibility migration

## Evidence and decision

The source estate is already hybrid. Stargate owns identity, DysonFS/FileSystem owns storage, Metoer owns Ring/push responsibilities, and Blade is a Go gateway. DysonNetwork still contains substantial C# Passport, Sphere, Messager, Wallet and Develop behavior; WattEngine is another C# backend. Client applications remain separate.

Reimplementing the existing Go services or converting the clients would increase migration risk without satisfying missing backend functionality. A single all-purpose serializer would also break documented differences in null handling and field names. Therefore the initial code uses a single Go module with explicit domain packages and infrastructure adapters, while preserving upstream service ownership and wire contracts. This is a development organization decision, not a decision to merge production databases or collapse all services into one process.

## Implemented boundaries

- Passport username policy accepts injected availability, randomness and time; no account-authority duplication
- Sphere ranking accepts resolved policy signals; it cannot bypass visibility or moderation by pretending to resolve them
- Messager sequence selection handles the deterministic recovery predicate; caller must still authorize room/thread access and perform hydration
- Sphere post-watch service separates defaults, patch semantics and watcher resolution from repository and permission providers
- A local post-watch HTTP host connects the actual service to a durable JSON store, with fail-closed authentication unless explicitly placed in synthetic fixture mode
- Passport board work uses independently specified DTO/contracts from licensed client and server declarations, with mandatory validation/transaction/cache adapters and no invented protobuf binding

## Operational constraints

The local file store is single-process only. It atomically replaces and syncs its file, rejects corrupted formats and cross-account rows, and never resets a damaged store to empty. Per-service update serialization prevents lost field updates inside that process. It is not a substitute for PostgreSQL isolation, migration, indexes, backups or multi-replica concurrency.

Production authentication, permission mapping, RPC and database adapters must be implemented against pinned lawful contracts. Missing adapters fail closed. The development host requires an actual loopback IP, has timeouts and graceful shutdown, and refuses to treat synthetic fixture identity as production login.

## Cutover criteria

1. Freeze the intended backend versions, deployments and client versions; same-named forge repositories are not proven byte-identical
2. Enumerate routes, DTOs, permissions, error/null/time behavior, persistence transitions, events and jobs per domain
3. Implement each production adapter with disposable infrastructure and synthetic data
4. Differential-test legacy and Go behavior, including failure and concurrency cases
5. Rehearse migration, reconciliation and rollback on copies, retaining originals
6. Prove existing-client flows and gateway aliases before replacing any service

No production cutover, endpoint replacement, external account action or real-data migration occurred in this checkpoint.
