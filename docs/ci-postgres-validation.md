# Disposable PostgreSQL CI validation

The `PostgreSQL contract validation` workflow performs local build/vet/race checks, then initializes a fresh PostgreSQL 16 cluster under the runner's private temporary directory. Only a private Unix-domain socket is enabled; `listen_addresses` is empty. No existing database, service secret, deployment, or external account data is used. PostgreSQL is stopped on exit and explicitly restarted between write and persistence-read phases.

The original discovery checkpoint required thirty real-database cases and six post-restart cases. The current required set is maintained in `scripts/check-integration-results.py`, with flow coverage and exact-commit status in `docs/runtime-flow-acceptance.json`. These include four watcher-query cases covering recipient union/preferences, explicit soft-delete assumptions, one-hop chains and input errors/default-denial. Watcher reads explicitly opt into a soft-delete-only compatibility-test policy; the normal repository denies those reads until the global-filter contract is verified. Five board cases cover account/JSON/ID/order/null roundtrips, real SQL rollback including retained tombstone order conflicts, both transaction interleavings with observable advisory-lock waits, default policy denial/errors, and board restart persistence. Board catalog/normalizer/cache adapters are explicitly synthetic fixtures. Passing candidate policies is not upstream recipient, board filter/audit, or production authorization parity. `PG_TEST_REQUIRED=1` makes absent configuration fatal. The JSON result guard also rejects skips, failures, missing test cases and empty output. Ordinary `verify.sh` without the disposable runner still clearly reports that database tests skipped.

Official action references were resolved through GitHub's public tag refs on 2026-10-07 and pinned immutably:

- [actions/checkout v4](https://api.github.com/repos/actions/checkout/git/ref/tags/v4): `11d5960a326750d5838078e36cf38b85af677262`
- [actions/setup-go v5](https://api.github.com/repos/actions/setup-go/git/ref/tags/v5): `40f1582b2485089dde7abd97c1529aa768e1baff`

These are official-origin checks, not a claim that every line of each action has undergone a security audit. Checkout does not persist credentials. Permissions are limited to `contents: read`; no user secrets are referenced. Go 1.26.4 is pinned, module downloads are checked against `go.sum`, and test execution disables new module retrieval. PostgreSQL 16 is supplied by the [official Ubuntu 24.04 runner image](https://github.com/actions/runner-images/blob/main/images/ubuntu/Ubuntu2404-Readme.md), which documents the service as disabled by default. The workflow checks the major version and executable before using the binaries; it does not start the default service.

Local preflight passed shell/YAML parsing, Go build/vet/ordinary/race checks, and synthetic guard checks proving that missing/skipped/failed results are rejected. The local runtime still cannot create Unix sockets; only a completed remote workflow run can establish actual database results. This workflow does not establish production authentication, migration rollout, C# differential compatibility or whole-platform parity.

## Historical discovery continuation gate, 2026-10-08

The combined tree adds four app-discovery cases, four package download/counter
cases, three bot-command cases, and four NodeInfo cases to the earlier fifteen-case baseline. Its compiled
process scripts cover app discovery, recovered catalogs/badges, miniapp redirects,
and NodeInfo statistics. The package/NodeInfo smoke runs after all restart
assertions so its download-count writes cannot invalidate the restart oracle.
All connections target the marked disposable cluster; redirect targets are never
followed. Scripts are checked for syntax and unit-level assumptions locally.
That early combined suite was subsequently published to the authorized private
recovery branch and validated. Local socket restrictions persist. Successful CI
results remain valid only for their recorded commits; later expanded candidates
are tracked separately in the runtime flow acceptance map.


## Expanded suite budgets

The composed candidate now requires 560 before-restart cases and 73 restart
cases, with the exact names enforced by the guard. Each Go test command retains
`-race -count=1 -json` and all security derivation work. The before phase has an
explicit 30-minute package timeout; restart has 10 minutes. The Actions job has
50 minutes, allowing both bounded phases plus build/setup and process checks.

Commit `22e2e96d13bb5c90919cf3bfb78f7506dd832beb` reached Go's former default
10-minute package timeout after 378 completed top-level PG cases; all six new
factor policy-history regressions had passed. The timeout stack showed active
password PBKDF2 computation in an ordinary sign-in test, rather than a blocked
SQL wait. No individual assertion failure was reported before termination;
restart was not reached. This focused evidence does not establish a full pass.
The narrow timeout-only correction is `e104bdc31c1202d2e154bf84a677a35c9490afda`.
JSON test events preserve individual durations, and an enlarged timeout must not
be used to mask a blocked test, weaken derivation, remove race checks or skip a
required case.


## Restart fixture discipline

New restart fixtures use `fixtureRestartPhase` and the runner's sole
`PG_TEST_PHASE` variable. Unknown phases fail when a real PG configuration is
present. Saved-state helpers create markers exclusively in before and read only
in after; IDs and pre-restart documents must be verified before any continuation
operation. The source guard rejects invented phase environment names.

The pending recurring-product and stock fixtures initially used an unexported
phase variable and would have recreated state in after. Both now use the shared
helper and exact pre-restart order IDs. No earlier 455-case green test used that
variable, and the pending candidates failed before restart, so no prior full
restart success is attributed to those invalid assertions. Repository
reconstruction on one pool is documented separately from an actual DB restart.

The enlarged 539-case checkpoint uses 30/10-minute phase budgets and a 50-minute job ceiling because observed before phases reached 17–19 minutes. This gives bounded runner-variance headroom without changing per-test deadlines, password/PIN derivation, race instrumentation, or assertions. Per-test JSON durations remain the basis for future isolated-cluster sharding; composed cross-domain and actual restart coverage must remain intact.


## Static SQL bind-argument preflight

`sqlargs` uses the Go AST to resolve string literals and package-local string
constants/concatenations at SQL-looking Exec/Query calls, then the pinned PG16
parser counts genuine ParamRef nodes. Quoted and commented dollar markers do not
count. Highest parameter index must match the explicit argument count. Four
wrong-count controls are rejected. Dynamic builders, unresolved expressions,
variadic calls, SQL type inference and transaction behavior remain outside this
guard. The MLS standalone-policy mismatch was dynamic; it is corrected by
reusing the existing canonical read adapter and still requires actual PG tests.
