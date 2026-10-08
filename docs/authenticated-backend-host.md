# Authenticated modular backend host

This independently authored host composes the owned authority verifier with
account/profile and publisher APIs, Passport board, Sphere taxonomy, Messager
read/sync/unread projections, bounded text-post lifecycle, and the durable Wallet domain. It is a single
in-process Go application. It does not implement the excluded shared protobuf
contracts, emulate Stargate, or establish whole-platform or legacy-token parity.

## Construction and startup

`application.NewPostgres` constructs handlers without connecting, opening a
listener, migrating, creating accounts, granting permissions, or generating
keys. The caller owns the database pool. An optional separate authority pool
supports a SELECT-only verifier connection; the account/profile module still
requires the canonical owned account data in the domain database. This is not
cross-database account replication.

`cmd/backend` validates configuration and exits by default. `--serve` explicitly
enables connection/readiness checks and the loopback listener. The command uses
one database for its combined runtime and takes only a trusted RSA public key,
an explicit key ID, issuer, audience, maximum access-token lifetime, and injected
PostgreSQL URL. It has no enrollment, login-code creation, grant-editing, private
signing-key, PIN-enrollment, funding, migration, or production-seeding command.

Required flags are `--issuer`, `--audience`, `--key-id`,
`--trusted-public-key-file`, `--taxonomy-public-visibility`,
`--messager-none-encryption`, `--messager-notify-all`,
`--messager-notify-mentions`, and `--messager-notify-none`. Enum values are
explicit database policy, not guessed serialized Shared constants. The three
notification values must differ. `--dsn-env` defaults to
`SOLSYNTH_BACKEND_DSN`. Its value must be an explicit PostgreSQL URL with user,
host, and database; supported query options are `sslmode`, `connect_timeout`,
and an absolute Unix-socket `host` directory. Database errors do not echo the
URL or credentials.

TCP database TLS defaults to `verify-full`; unencrypted connections are accepted
only for explicitly local/disposable hosts. Ambient libpq connection variables,
service files, password files, client-key files, arbitrary URL options, and
non-loopback HTTP listeners are rejected or disabled. The default HTTP address is
`127.0.0.1:8090`; production ingress/TLS, deployment policy and credentials remain
separately owned operational decisions. No production connection was attempted.

`--pin-pepper-env` and `--pin-pepper-id` are optional and must be supplied together
for an already provisioned payment policy. A pepper does not provision a PIN or
allow a transfer when policy is missing. The command runs a bounded durable wallet
expiry/settlement batch every `--settlement-interval` (default 15 minutes). It
stops serving on interrupt/SIGTERM. Server header/read/write/idle timeouts are
bounded. `/healthz` is liveness; `/readyz` checks required relation presence and
returns only a sanitized status. Readiness does not certify schema versions,
column types, data migrations, external-writer safety or legacy compatibility.

## Routing and authorization

- `/passport/accounts/.../board` and `/api/accounts/.../board` share the Board
  handler. Public board reads use persisted account resolution; self/admin
  operations require the exact declared domain grants.
- `/sphere/posts/tags`, categories, related administration/subscription routes,
  and their `/api` equivalents use the Taxonomy handler.
- `/sphere/posts` and `/api/posts` use the bounded text-post handler after
  taxonomy-specific routes. Public/unlisted/private text Moment/Article posts,
  drafts, edits, publication and deletion retain canonical publisher ownership.
  Unsupported media, tags/categories, social graph visibility and provider
  workflows fail explicitly. The separate `owned_taxonomy.posts` projection is
  not automatically populated by `owned_text_posts`; no cross-domain tag-link
  integration is claimed.
- `/messager/chat/...` and `/api/chat/...` use persisted room membership and the
  explicitly selected public-room/encryption policy. No sending, realtime
  delivery, encryption-key exchange or notification provider is added.
- `/wallet/wallets/...`, `/api/wallets/...` and the evidenced
  `/stargate/accounts/me/pin-status` alias use the Wallet handler.
- `/stargate/accounts/...`, `/passport/accounts/...`, `/api/accounts/...`, and
  `/sphere/publishers/...` / `/api/publishers/...` route to the supported owned
  account/publisher operations. These aliases do not imply complete Stargate or
  publisher endpoint coverage.

All protected domain handlers receive one private proof-bearing principal from
the owned authority. The middleware strips bearer/cookie bytes before forwarding
to domains, rejects caller-asserted identity headers and query credentials, and
never stores a bearer token in a long-lived map. Invalid supplied credentials
fail instead of falling back to an anonymous route. Adapters revalidate current
session/account state and exact grants on protected operations; authentication-
only operations use the separate freshness method without inventing a grant.

Account, publisher, room-member and wallet IDs retain distinct meaning.
Taxonomy resolves publisher ownership and manager access through `accountpg`;
`owned_taxonomy.post_tags.owner_publisher_id` references canonical
`owned_publishers`. It does not create a second publisher directory.

App-secret/consent identity routes return `501 APP_IDENTITY_UNSUPPORTED`. The
command is verifier-only, so session/token endpoints return
`501 AUTHORITY_ISSUER_ROLE_REQUIRED`. An embedding application may explicitly
provide a separately configured issuer service; the command never creates one.
Unknown routes stay unavailable. Ambiguous/encoded/traversal paths are rejected;
relative `Location` responses retain the service prefix used by the caller.

## Schema and disposable integration wiring

No schema is installed at construction or startup. The disposable integration
runner needs this order in addition to its existing domain fixtures:

1. `internal/authoritypg/schema.sql`
2. `internal/accountpg/schema.sql`
3. Existing Board and Messager projection fixtures
4. `internal/walletpg/schema.sql`
5. `internal/application/schema.sql` (canonical publisher FK requires step 2)
6. `internal/postpg/schema.sql` (requires authority and canonical publishers)

The taxonomy candidate tests keep their separate `taxonomy_candidate` schema.
The owned runtime uses `owned_taxonomy`; fixed SQL identifiers are rewritten by
the authored namespace adapter and request values remain positional arguments.
The supplied SQL and projection fixtures need deployment/migration review before
any real-data use. This work did not perform real-data migration or production
DDL.

Build `./cmd/backend` in the disposable runner and export its absolute path as
`BACKEND_BINARY`. Require these non-skipped cases in the before phase:

- `TestPGBackendDomainComposition`
- `TestPGBackendFreshAuthorityAndIndependentPools`
- `TestPGBackendHTTPProcessRestart`
- `TestPGBackendRestartPersistence`

Require `TestPGBackendRestartPersistence` again after the actual PostgreSQL
restart. All setup begins with the existing marked disposable-cluster guard.
Synthetic account/session fixtures and ephemeral signing keys are used only
inside that cluster. The process test supplies the command only the public key,
uses the actual compiled host on a loopback port selected by the OS, and verifies
persisted data after stopping/restarting it. Restart-phase files stay inside the
marked disposable run directory and must never be treated as deployment secrets.

## Validation and remaining limits

Command configuration/key/DSN validation, private-proof freshness, authority
revalidation, routing and handler tests pass with the race detector. Integration
tests compile and are guarded/skipped without the disposable DSN. Actual
PostgreSQL, socket/process HTTP and database-restart execution were not performed
in this workspace; no test-package `ok` is presented as a database pass.

Fresh authorization checks and domain mutations are separate transactions.
Revocation that commits before an operation's authority check is observed, but
concurrent revocation between that check and the domain commit is not serialized
into a single distributed/transactional boundary. Existing cooperating-domain
locks and transaction semantics remain in place. Strict revocation linearization
across every domain requires additional design and tests.

The host intentionally retains each port's bounded projection and unsupported
features. It does not deliver mail, notifications, external payments, media,
federation or realtime events. An outbox record is durable internal state, not
proof of provider delivery. Client differential tests, migration/rollback plans,
production credentials and ingress, and full API coverage remain separate gates.

The integrated host additionally mounts canonical social/realm and post-interaction routes, including relationship-based visibility, and public badge/exchange/product catalogs. Explicit `--catalog-config` supplies catalog values and `--badge-icons` supplies icon assets. Product catalog reads return a configuration error until currency is explicit. Canonical taxonomy public visibility is 0. Post/wallet writes now use same-transaction authority checks; other domains retain their documented boundaries. Realm-wallet writes use canonical locked realm membership.
