# Backend migration checkpoint

This is a source checkpoint, not a completed platform replacement.

## Working code

- Go post-watch preference service and `GET/PUT /api/posts/watch` development host
- Durable single-process local persistence, account isolation, corruption rejection, atomic replacement and concurrent preference-update serialization
- Source-backed username generation, timeline ranking and room sequence-recovery policies
- Account-board transport-independent service with ordered reads, transactional repository replacement, app-owned payload preservation and app/admin payload updates; required repository/validation/cache adapters remain explicit
- Synthetic contract tests; see `verification-result.json` for the exact validated state

An actual loopback process smoke test verified fail-closed default authentication, explicit synthetic fixture behavior, read/update, persistence after restart and graceful shutdown. No real account, production backend or external message delivery was used.

## Inventory coverage

- GitHub Solsynth: 26 public repository results, empty second page; 3 archived repositories retained in inventory
- SoSYS: all 19 publicly visible repositories across two organization pages
- Passport: 36 controller files and 255 static HTTP verb attributes
- Sphere: 37 controller files and 331 static HTTP verb attributes
- Messager: 74 static HTTP action declarations
- Develop: 11 controllers and 63 static HTTP action attributes
- Wallet: 10 controllers and 95 static HTTP action attributes
- Total across these five groups: 818 static action attributes, not 818 verified distinct runtime APIs

These are source inventory counts, not verified distinct runtime endpoints, unique features, or migrated routes. Gateway aliases, inherited behavior, jobs, RPC and other services make the full feature count unknown.

## License boundaries

Implemented translated policies use DysonNetwork's verified AGPL license. Source snapshots and notices are retained. Board interface work cites licensed Solian and DysonNetwork declarations; it does not claim strict clean-room development.

Metadata-null backend repositories ElecPostal, PersonalityCore, DistributionCenter and MaidCafe have no conventional root license file in the limited audit. This does not prove absence of every license, but establishes no reuse permission. Their implementation was not incorporated. The audit records previous source exposure where applicable.

## Exact blockers and remaining work

### Source/access or contract gaps

- The SoSYS organization API returned 403; no bypass was attempted
- A later public repository-page batch was cancelled; those reads were not retried through another route
- A Stargate clone was cancelled and not retried; this checkpoint preserves selected pinned source snapshots, not complete Git history
- WattEngine has no accessible same-named GitHub repository in the current lookup; its source/license remains unverified
- NeTo, Spec and Socommon's complete shared model/protobuf/permission contracts are not available in the evidence collected so far
- Room-sync exact message DTO, global deletion filters, null/error codecs and some hydration contracts remain incomplete; an apparently compatible endpoint must not be fabricated from partial DTOs
- Board universal payload-validation and protobuf field-number contracts are not fully established; mandatory adapters remain explicit

### Independent authorized work still possible

- Use the completed licensed Wallet/Develop inventories to port self-contained service behavior
- Derive additional interfaces from lawfully licensed public clients and documented protocols
- Implement/test production adapters once their contracts are specified, using disposable data and infrastructure
- Expand legacy/Go differential, database migration/rollback, concurrency, event and existing-client tests

### Not achieved

No complete production service parity, production authentication, PostgreSQL migration, gateway integration, protobuf compatibility, NATS/Redis integration, container deployment, real-data migration, or whole-platform cutover is claimed. Frontend clients are unchanged. No upstream repository writes were made.

## PostgreSQL follow-on checkpoint

The PostgreSQL adapter is implemented against the pinned licensed migration, and the development host can select it. It uses explicit per-account transaction locking and applies nullable patches inside the transaction, preserving unspecified fields across cooperating Go instances. It performs no startup schema changes. The original schema fixture and six actual-database test cases are included.

Database verification is **blocked, not passed**: initdb succeeded, but PostgreSQL could not create its private Unix socket. One supported escalation attempt failed the same way; an ordinary `/tmp` diagnostic then showed AF_UNIX socket creation itself raises EPERM before bind. No TCP fallback, OS/security change or production database was attempted. The actual PostgreSQL cases were skipped by the ordinary unit suite and must run in an environment that supports the required private sockets.

Production authentication also remains unimplemented. The lawful contracts establish a shared authoritative authenticator, but its exact RPC descriptors and PostSubscriptionsManage permission value are unavailable. The command continues to deny authentication by default. See `internal/auth/CONTRACT.md` for the smallest missing contract inputs and `docs/postwatch-review.md` for remaining transport/schema differences.
