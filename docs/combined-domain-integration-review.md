# Combined Board, Messager and Taxonomy integration review

Reviewed the three completed domain deliveries together on archive baseline
`ac50657a6fa0fc16921ae44ca0ddedc46689f496`. Only the assigned Board, Messager and
Taxonomy deliverable paths were overlaid. The publication snapshot was not
modified, and the concurrent authority and wallet work was not overlaid or edited.

## Correctness changes

- Board HEAD requests receive an explicit bodyless 405 with `Allow: GET`
  (`GET, PUT` on the two shared layout paths), consistent with the retained
  `HttpGet`-only method declarations. This prevents Go's implicit GET-pattern/HEAD
  matching from entering handlers that branch on a literal GET. Recorder tests
  cover self, public, admin and app manifest paths and prove no board/cache state
  changes or mutation of the original request. No new HEAD-read contract is added.

- Required PostgreSQL wiring now installs both new schemas and builds/exports
  all three domain binaries. The result guard requires all **53** declared
  `TestPG...` cases before restart and **9** genuine persistence assertions after
  PostgreSQL restart. The post-restart selector comes from the same required set.
- Board HTTP database-restart coverage now writes its own app-payload sentinel
  and reads/asserts it in both phases. It no longer skips the required before
  phase or depends on an earlier test's setup.
- Real process tests cover default-denied behavior, synthetic opt-in, actual
  database-backed responses and application restarts for Board, Messager and
  Taxonomy. Messager additionally checks membership denial, opaque ciphertext,
  exact integers beyond float64 precision, projection metadata, credential refusal
  and an independently disabled unread policy. Taxonomy checks persisted ownership,
  protection, subscriptions, local audit count, anonymous read availability and
  denied anonymous mutation. None claims real identity or external delivery.
- Commands report the actual loopback listener address. Tests let each process
  bind port zero and consume verified readiness, eliminating a release/rebind
  race. Messager's default port is **8085**, distinct from Board's **8084** and
  Taxonomy's **8083**; this prevents the two completed hosts from colliding by
  default. Test HTTP clients do not use environment proxies or follow redirects.
- Taxonomy JSON is fully encoded before committing a success status. A stored
  timestamp outside JSON's representable year range returns a sanitized 503 and
  removes success-only `X-Total`/`Location` headers. Unit regression coverage
  checks both detail and list responses and failed 201 serialization.
- Board's 2 MiB fixture and Taxonomy's 64 KiB authority-file limits include all
  trailing bytes. An `io.LimitReader` boundary can no longer be mistaken for real
  EOF after a valid JSON prefix. Exact-limit and extra-whitespace/second-value
  cases have regression coverage.
- The four new pinned Taxonomy source files are checked against both manifest
  SHA-256 values and Git blob IDs by an ordinary Go test. Expected hashes and
  retained source snapshots were not changed.

## Combined review findings

The nine installed disposable SQL fixtures define **28 distinct qualified table
names**. Taxonomy uses `taxonomy_candidate`; NodeInfo uses its own namespace;
Messager's chat tables do not collide with Board, watchers or Develop. A new
static wiring test checks collisions, missing schema installs, missing command
builds/exports, required-test mode and the restart selector, including seven
negative mutations. It runs in the ordinary verification script without sockets.

Board cooperating mutations use account-scoped transaction locks and rollback;
its explicit cache invalidation is preserved. The fixture cache holds account
identity only; Board deletion retains its documented no-purge behavior. Messager
reads remain read-only repeatable-read transactions with separate synthetic
notification-enum opt-in; encryption JSON remains opaque. Taxonomy count/list
snapshots, serialized claim/quota/duplicate decisions, rollback of audit failures
and subscription/delete ordering remain intact. Its audit ledger is local and
transactional; no external action-log delivery is claimed.

Default constructors and command modes remain denied for unsupported production
identity/grants. The implemented independent storage/cache/audit policies are
usable for explicit disposable fixtures; they are not proof of hidden upstream
hooks or a complete shared DTO contract.

## Verification

Verified with Go 1.26.4 and the supplied local module/build caches:

- `gofmt` check over Go sources: clean
- `bash -n scripts/integration-postgres.sh scripts/verify.sh`: passed
- `python3 scripts/test-integration-guard.py`: passed, including missing/skip/fail
  rejection and exact phase-pattern correspondence
- `python3 scripts/test-integration-wiring.py`: passed; 9 schemas, 28 tables and
  7 negative wiring checks
- `go build ./...`: passed
- `go vet ./...`: passed
- `go test -count=1 ./...`: passed
- `go test -race -count=1 ./...`: passed

No PostgreSQL server, HTTP listener, .NET process, production endpoint or database
was started/accessed during this review. Ordinary Go verification skips the 53
real PostgreSQL cases without the guarded DSN. Those cases, all process tests and
actual database restart remain **pending authorized CI**, not validated locally.
The source-evidence script's repository-wide copy/self-test was not invoked here;
publication-reference sanitization remains the integration owner's separate work.

## Applying the review

Apply the review patch only **after** overlaying the completed workers' exact
assigned deliverables on the baseline. Its diff is relative to that combined
pre-review tree, not directly to the archive commit. The integration owner should
merge additional authority/wallet test declarations into the required guard and
update counts only when those separate modules are included. Do not copy an
entire review directory over a separately sanitized publication tree.
