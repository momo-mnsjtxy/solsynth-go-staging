# NodeInfo development host

This loopback-only host is a source-backed synthetic compatibility candidate.
It is disabled by default and never connects to a database in that mode.

Build with Go 1.25+:

```sh
go build ./cmd/nodeinfo
```

An isolated synthetic PostgreSQL fixture may be exposed with:

```sh
export NODEINFO_TEST_DSN='host=/path/to/disposable/socket port=55432 user=synthetic dbname=postgres sslmode=disable search_path=nodeinfo_candidate'
./nodeinfo --listen=127.0.0.1:8082 \
  --postgres-dsn-env=NODEINFO_TEST_DSN \
  --synthetic-fediverse-type=73 \
  --synthetic-soft-delete-policy=deleted-at-is-null \
  --activitypub-domain=fixture.example
```

`73` is only a fixture value, not a known upstream enum value. The soft-delete
policy is also unverified. Omit `--activitypub-domain` for the source's
`localhost` fallback. An explicitly empty domain remains empty. Domain strings
are configuration, never derived from request Host/Forwarded headers.

Routes:

- `GET /.well-known/host-meta`
- `GET /.well-known/nodeinfo`
- `GET /.well-known/nodeinfo/2.0`
- `GET /.well-known/nodeinfo/2.1`

Discovery intentionally advertises `/nodeinfo/2.x` without `/.well-known`, as
the pinned controller does. Those advertised paths are not candidate aliases.
HEAD is rejected with status 405 and no body. Errors, middleware, routing edge
cases and soft-delete behavior are explicitly candidate choices. Do not expose
this as a production replacement. See `ports/sphere/nodeinfo/PROVENANCE.md`.

The host opens PostgreSQL only after validating every required opt-in, pings it
before listening, redacts connection errors, and executes no schema creation or
migration. It owns a bounded connection pool and shuts down on SIGINT/SIGTERM.

Disposable actual-PG integration requires loading
`internal/nodeinfopg/testdata/schema.sql`. Required cases are
`TestPGNodeInfoCountsAndHTTP`, `TestPGNodeInfoPolicyAndReadOnly`,
`TestPGNodeInfoFailurePropagation`, and `TestPGNodeInfoRestartPersistence`.
The last case must execute in both `PG_TEST_PHASE=before` and `after`, with an
actual PostgreSQL server restart between them. Ordinary unit tests skip those
cases without a verified disposable DSN. Never point this fixture runner at
production or personal data.
