# Combined backend validation

The combined runner checks the independently implemented owned Authority,
Accounts, Wallet, Posts and authenticated Backend together with the retained
Board, Messager, Taxonomy, watcher, Develop, catalog and NodeInfo fixtures. Every
database and process check uses synthetic data in a new marked disposable
PostgreSQL cluster. It does not connect to an existing database.

## Required evidence

`scripts/check-integration-results.py` is the explicit test inventory. The
before phase requires **122 top-level PostgreSQL cases**; the after phase requires
**20 database-restart persistence cases**. The source-declaration check rejects an
added or removed `TestPG...` test unless the required inventory is also reviewed.
Both Go test selectors are generated from that inventory.

| Domain | Before PostgreSQL restart | After PostgreSQL restart |
| --- | ---: | ---: |
| Retained watcher, Board, Develop, NodeInfo, Messager and Taxonomy fixtures | 53 | 9 |
| Owned Authority | 2 | 1 |
| Accounts and publisher identity | 5 | 1 |
| Wallet | 9 | 1 |
| Posts | 7 | 1 |
| Authenticated Backend composition | 4 | 1 |
| **Total** | **80** | **14** |

The JSON guard requires a run event and one successful top-level result for
every required case, followed by successful completion of the integration
package. It rejects failures, PostgreSQL skips (including nested subtests),
missing results, unexpected or wrong-phase cases, duplicate passes and results
from another package. Synthetic guard tests exercise those rejection paths;
these are checks of the guard, not evidence that PostgreSQL cases passed.

The runner stops PostgreSQL with `pg_ctl -m fast -w stop`, starts the same data
directory again, and then runs only the 14 required persistence cases. It does
not reapply schemas or reconstruct their sentinels between phases. Restart
tests must retain and inspect their existing state in the after branch.

## Schema and executable wiring

The runner explicitly installs these owned schemas first, in this order:

1. `internal/authoritypg/schema.sql`
2. `internal/accountpg/schema.sql`
3. `internal/walletpg/schema.sql`
4. `internal/application/schema.sql`
5. `internal/postpg/schema.sql`

It then installs the nine existing watcher, Board, Develop, NodeInfo, Messager
and Taxonomy fixtures. The owned taxonomy namespace is separate from the
candidate taxonomy namespace. Canonical publisher references depend on the
Accounts schema. The static wiring check verifies the complete 14-file schema
inventory, dependency order, and all **58** distinct qualified table names, including
`CREATE TABLE IF NOT EXISTS` declarations.

Before testing, the runner builds and exports the Backend, Board, Messager and
Taxonomy command paths. It also builds Develop discovery, catalog and NodeInfo
commands for the existing external HTTP probes. Required subprocess tests use
those exact binaries, real loopback listeners on port zero and readiness output.
A missing binary is a failure in required mode.

Authority tests authenticate retained synthetic sessions in a fresh test
process; Accounts tests read a retained profile in a fresh process. The unified
Backend process test writes a profile, wallet, publisher and public text post
through HTTP, then reads the retained profile, wallet and post through the
restarted host. It also checks cross-account denial and a fresh authority
revocation. Its Board state is retained across the same host restart. The database suite additionally covers profile/publisher state,
exact-decimal wallet transactions and policy, post publication/privacy, fresh
authority decisions, concurrency, rollback and independently opened pools. The
PostgreSQL phase checks are separate from host-process restarts.

## Execution and boundaries

The CI workflow first runs `scripts/verify.sh`: source and coverage checks,
static integration guard/wiring checks, formatting, build, vet, ordinary tests
and race tests. An ordinary package result without the guarded database DSN is
explicitly reported as skipped database coverage.

The next CI step requires PostgreSQL 16, creates the disposable cluster, and
runs `scripts/integration-postgres.sh`. PostgreSQL listens only on the private
Unix socket in its marked run directory, with socket permissions `0700`; process
HTTP tests bind only loopback. `PG_TEST_REQUIRED=1`, shell pipe failure checking
and both JSON result checks prevent skips or partial phase output from being
reported as a successful database run. The run directory retains logs for CI
diagnosis; all identities, keys, credentials, balances and messages in it are
synthetic fixtures.

This continuation was prepared without opening local sockets, starting a local
database or contacting a production service. Shell/Python checks and Go compile
checks can validate the harness locally. Actual PostgreSQL, HTTP process and
database-restart evidence must come from the authorized CI run for the exact
published combined commit. An earlier commit's successful run does not validate
this combined change.

These checks validate the implemented owned protocol and explicitly configured
domain policies. They do not establish compatibility with hidden upstream
identity protocols, production deployment or migration readiness, C#
differential behavior, upstream watcher/global-filter equivalence, external
delivery, or whole-platform parity.

## Local verification of this checkpoint

- `bash -n scripts/integration-postgres.sh scripts/verify.sh`: passed.
- Python syntax compilation for the result guard and both harness test scripts:
  passed.
- `python3 scripts/test-integration-guard.py`: passed; 80 before / 14 after,
  exact source declarations and selectors, and 30 negative event streams.
- `python3 scripts/test-integration-wiring.py`: passed; 14 schema files,
  58 qualified tables, and 18 rejected wiring/collision regressions.
- `go test -list '^TestPG' ./tests/integration`: compiled successfully and
  listed exactly the 80 required cases; no tests ran.
- Harness `git diff --check`: passed.

These are local harness checks only. The required actual PostgreSQL and process
checks remain pending the exact combined commit's authorized CI run.

## Harness provenance

The retained 53/9 Board/Messager/Taxonomy harness extension was taken from the
review patch with SHA-256
`7d7054dfa65f9da28ac748d5aaba9c8978d30297c4ed7211c638a41185af0723`.
The `verify.sh` insertion was applied manually because this continuation already
had additional coverage checks; those checks were preserved. Owned-domain
schemas, binaries and explicit required test names were then added, and guard
and static-wiring regression checks were strengthened.

Chat lifecycle adds six required cases and one restart assertion; transaction-bound post/wallet authority adds one before case with deterministic concurrency subtests. The backend subprocess test additionally verifies chat create/send/read across process restart. Previous 80/14 core checkpoint passed CI37726985433; changed tree requires fresh CI.

Social/realm providers add five cases and one restart assertion; reactions/bookmarks/collections/associations add ten cases and one restart assertion. Canonical taxonomy now reads a view of owned posts; post schema precedes taxonomy. Unified-host tests include friendship visibility, canonical realm wallets, catalogs and persisted bookmarks.

Filesystem media adds eight guarded PostgreSQL cases and one restart assertion. Backend process checks stream a private upload into an explicit temporary root and read unchanged bytes after process restart, including anonymous denial.

Developer apps add four cases and one restart assertion; notifications/realtime add eight cases and one restart assertion. The backend process test redeems a one-use browser stream ticket, completes a real WebSocket ping/pong and rejects ticket reuse. Canonical app identity, consent and board-cache updates are checked through composed HTTP.
