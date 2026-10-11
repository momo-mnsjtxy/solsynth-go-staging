# Virtual pets (independently authored feature)

This is a new deterministic virtual-pet backend for the Go rewrite, inspired by
classic care/growth games. It does not import QQ assets or its protocol, restore
old AI-personality code, or change wallet/payment/commerce balances. No cash
purchases, random rewards, trading, transfers, recurring charges or death exist.
A separate client UI and original artwork still need to consume these APIs;
asset keys below are stable identifiers, not claims that artwork files exist.

## Installation and rollback

Apply `internal/petpg/schema.sql` explicitly to the canonical shared PostgreSQL
pool after `internal/authoritypg/schema.sql`. The integration installer includes
it after account schema installation. `petpg.Schema` embeds the same SQL. The
backend does not install schema during startup. No numbered deployment-migration
system exists in this repository; register this additive SQL in the deployment's
own migration system. Reapplying it is safe for this initial schema version.

The backend requires both pet relations/columns for readiness when shared-pool
pet support is active. Deploy schema before application code. Separate authority
pools/non-atomic write mode do not enable pets; the routes return 503. Roll back
application code without deleting pet tables; preserve state and the command
journal for a forward retry. Do not purge the journal while clients can replay
old request IDs. No production migration or deployment is performed by this work.

## API

All routes require the normal authenticated account. The account ID comes only
from shared authority; no account, price, reward, timestamp or balance input is
accepted. Responses are JSON with `Cache-Control: no-store`. `/passport/pets/...`
is the sole gateway alias for `/api/pets/...`. Encoded/traversal paths, query
parameters, forged identity headers and unsupported HTTP methods are rejected.

| Method | Canonical route | Payload/result |
| --- | --- | --- |
| GET | `/api/pets/catalog` | Species, shop items, daily quest definitions |
| GET | `/api/pets/shop` | Fixed catalog items and pet-coin prices |
| GET | `/api/pets/me` | Full state, lazily settled at server time |
| GET | `/api/pets/me/inventory` | Owned item quantities |
| GET | `/api/pets/me/equipment` | Equipped head/neck item IDs |
| GET | `/api/pets/me/tasks` | Current UTC-day progress/claims and definitions |
| POST | `/api/pets/me/adopt` | `request_id`, `expected_version: 0`, `name`, `species` |
| PATCH | `/api/pets/me` | `request_id`, `expected_version`, `name` |
| POST | `/api/pets/me/actions` | `request_id`, `expected_version`, `kind`: feed/wash/play/rest |
| POST | `/api/pets/me/purchases` | `request_id`, `expected_version`, `item_id` (quantity always one) |
| PUT | `/api/pets/me/equipment/{head\|neck}` | `request_id`, `expected_version`, `item_id` |
| DELETE | `/api/pets/me/equipment/{head\|neck}` | JSON `request_id`, `expected_version` |
| POST | `/api/pets/me/tasks/{task_id}/claim` | `request_id`, `expected_version` |
| POST | `/api/pets/me/commands` | Full command; kind adopt/rename/feed/wash/play/rest/buy/equip/claim |

Dedicated routes reject a body `kind` (except `/actions`), and reject
body `slot`/`task_id` when the URL supplies it. An empty `item_id` in the unified
equip command unequips. Dedicated DELETE requires no nonempty item ID. All
mutations return HTTP 200 and the resulting complete state, including replays.
Command JSON must be an object with unique known keys, at most 4 KiB, content type
`application/json`, and no trailing JSON. Null fields are rejected.

`request_id` is 16–64 ASCII letters/digits/underscore/hyphen, unique per account.
Use a new random ID for a new intent; preserve it for a transport retry.
`expected_version` is the state version last observed (zero for adoption).
Identical successful requests return the exact original response, even after
later mutations or UTC rollover. Reusing the ID with a different command returns
409. Replays do not settle, debit, reward or increment a version. An old replay
response can contain an old version; refresh GET before issuing a new intent.
A failed command is rolled back and does not reserve its request ID.

Errors include PET_INVALID (400), authority errors (401/403/423), PET_NOT_FOUND
(404), PET_CONFLICT/PET_REQUIREMENTS (409), PET_COOLDOWN/PET_DAILY_LIMIT (429), and
PET_UNAVAILABLE (503). Internal SQL errors are not exposed. Refresh after a
version conflict; wait until the per-action cooldown timestamp after a 429.

Example adoption command for `/api/pets/me/adopt`:

```json
{"request_id":"9f624821-39a1-4aba-83a4-dc16f318ae58","expected_version":0,"name":"Mochi","species":"cloudling"}
```

## Mechanics and bounds

- One permanent pet per account, with a 1–32 Unicode-code-point name (no controls,
  invisible format characters, invalid UTF-8 or surrounding whitespace).
- Three original species IDs: cloudling, foxlet, mossling. Species has no effect
  on rewards and cannot be changed after adoption.
- Initial stats all 80/100, 20 pet coins, five snacks and five soaps. This starter
  grant is recorded as the adoption journal entry and cannot be claimed again.
- Each complete 30-minute interval reduces satiety by 2, other stats by 1, all
  floored at zero. Residual elapsed time is preserved; reads are pure projections
  and do not consume intervals or increase versions. Mutations persist settlement.
  Offline time is saturated in constant work; no background tick is necessary.
- Feed consumes one snack, +30 satiety. Wash consumes one soap, +40 cleanliness.
  Play needs 10 energy, costs 10 energy/5 satiety, and grants +25 mood. Rest grants
  +30 energy. No care is accepted when its target stat is already full.
- Each care kind has an independent five-minute server-time cooldown. The first
  20 successful care actions per UTC day award 5 XP and 2 coins each. Later care
  is allowed without those rewards, subject to a total 100 successful commands
  per UTC day (including shop, equip, rename, adoption and claims).
- Level = min(100, 1 + floor(XP / 100)). Neglect never removes XP, inventory or pet.
  XP/coin balances saturate at one billion; versions stop before one trillion.
- Snack and soap cost 2 pet coins each, quantity cap 99. Blue scarf (neck) costs
  30; leaf hat (head) costs 50; cosmetics have quantity cap 1. Only owned matching
  slot items can be equipped. Cosmetics have no gameplay advantage.
- Daily UTC quests: feed once (5 coins/10 XP), play once (5/10), four successful
  care actions (10/20). Claims are once per day; reset does not refill stats or
  erase active cooldowns. Maximum care/quest currency minting is 60 coins/day,
  plus the one-time 20-coin starter grant. Saturation may reduce actual awards.

## Persistence and concurrency

`owned_pets` contains one account-keyed state document with scalar version/balance
constraints. Inventory, equipment, cooldowns and daily progress are in that bounded
atomic aggregate. `owned_pet_commands` is the append-only application journal and
idempotency store, retaining command hash, exact response, version, coin delta and
balance. No runtime code updates or deletes journal entries. The runtime database
role should not grant direct table writes to clients.

Every self read/write begins a read-committed transaction and uses the canonical
TransactionGuard with its original opaque principal proof. The guard locks the
canonical account even before adoption. Mutations additionally lock the pet row,
read DB time after locking, enforce expected version with a conditional update,
and insert the response/coin delta in the same transaction. Shared authority is
checked again before commit, including replay. Any error rolls back every effect.
Unrelated accounts do not share a global pet lock. Repository access is fail-closed
without a guard; synthetic identity or account IDs alone never authorize access.

## Validation status

Locally verified on 2026-10-11 with checksum-verified Go 1.26.9, PostgreSQL 17.11,
pglast 6.7 and PyYAML 6.0.3:

- `scripts/verify.sh` passed: whole-repository formatting, static gates, build,
  vet, unit tests and race tests. The ordinary run had no `PG_TEST_DSN`, so the
  historical platform PostgreSQL integration suite was explicitly skipped.
- 19 pet domain/HTTP tests and two dedicated application-routing tests passed.
- Seven pet-only real PostgreSQL tests passed under `-race`, covering HTTP/shared
  authority composition, replay/CAS/concurrent debits and claims, negative-balance
  constraints, journal/final-guard rollback, disabled-account lock waits, lazy
  decay and fresh-pool recovery.
- The separate before/after pet test processes passed around an actual
  `pg_ctl` server restart. Persisted pet state/balance and original command replay
  survived; this pet test issues fresh authentication in the after phase and does
  not claim preservation of the old credential proof. Broader platform credential
  continuity/restart acceptance remains a separate suite.
- All test pet schemas were removed and the private loopback-only cluster stopped.

See `virtual-pets-validation.json` for scope and exact commands. This is backend
code only: no client UI/artwork, remote push, production migration/deployment,
external-provider acceptance or full-platform database pass is claimed.

### Focused commands

After installing the approved Go 1.26.x compiler and resolving the existing pinned
module dependencies, run domain/HTTP, repository and routing tests:

```sh
go test -count=1 ./ports/passport/pets ./internal/petpg ./internal/application
go test -race -count=1 ./ports/passport/pets ./internal/petpg
```

Without `PET_TEST_DSN`, the repository tests explicitly skip real PostgreSQL.
To run them, use only a separately created disposable local PostgreSQL cluster,
with loopback TCP, database `pet_test`, user `synthetic`, and
`PET_TEST_DISPOSABLE=1`. Never point this fixture at an existing/shared service.
Each ordinary test uses a fresh random schema and removes it on completion.

```sh
PET_TEST_DISPOSABLE=1 PET_TEST_DSN='host=127.0.0.1 port=YOUR_DISPOSABLE_PORT user=synthetic dbname=pet_test sslmode=disable' \
  go test -count=1 ./internal/petpg
```

`TestPostgresPetServerRestartPersistence` deliberately skips in ordinary runs.
Run that exact test once with `PET_TEST_RESTART_PHASE=before`, stop/start the same
disposable PostgreSQL cluster, then run it in a new process with
`PET_TEST_RESTART_PHASE=after`. Both phases require the above DSN/disposable flag.
The before phase leaves only its explicitly named synthetic fixture; the after
phase verifies state, balance and command replay and removes that schema. A
fresh-pool test is also included, but does not replace a real server restart.

These focused commands are not a substitute for `scripts/verify.sh` and the
existing full-platform PostgreSQL/restart acceptance gates. This feature does not
change historical source-evidence manifests or claim legacy client compatibility.
