# Composer restart database isolation

This is a fixture-only response to private CI run 37888914261: `TestPGPostCompositionSavedRestartPersistence` lost its private fund between phases because unrelated wallet worker tests globally processed due funds with a synthetic clock seven days ahead. The saved fund correctly expires after one day. Runtime financial behavior, worker selectors, deadlines and visibility assertions stay unchanged.

## Runner and schema baseline

The source review is at `5ac8adc3a810a565c4f004073e4d20b37ce27660`. The runner explicitly installs 99 SQL inputs containing 315 distinct application tables. The installed source database is **not empty**:

- `public.owned_reader_cache_state`: one row, `singleton=true, generation=0`, from `internal/readerpg/schema.sql`
- `public.owned_automod_state`: one identical singleton, from `internal/adminpg/schema.sql`
- `public.owned_domain_trust_state`: one identical singleton, from `internal/domaintrustpg/schema.sql`
- `public.owned_progression_definitions`: 58 exact source-defined catalog rows, from `internal/progressionpg/schema.sql`

The name reservation, authority contact and realm permission migration INSERTs are backfills from initially empty application tables. INSERT statements inside installed trigger bodies do not execute during installation. No other application rows are allowed: the preparer compares the four reviewed baselines (including every progression definition and timestamp), scans every other application relation for data, and rejects foreign tables. New or changed baseline data requires review. It never deletes or rewrites source rows.

After every schema install, before any Go fixture or process smoke, the runner executes `scripts/fixture_template.py prepare`. A prepare-only SQL validation pass, if installed, must finish and close all its connections before this point. The preparer:

1. Requires private GitHub CI, its exact clean candidate commit, a private marked run/socket directory and the runner's exact synthetic DSN. The ordered schema filenames and bytes are SHA-256 bound to the candidate and marker.
2. Audits the source baseline and checks that `postgres` has no active connections through `template1`. It never terminates sessions.
3. Runs `pg_dump --schema-only --clean --if-exists --no-owner --no-privileges`. The cleanup statements run only while restoring into a uniquely named fresh template0-derived database; they do not touch the source. All application schemas, functions, constraints, indexes, triggers and extensions are retained, with no copied application rows or credentials.
4. Checks matching relation and extension inventories and zero rows in every application relation of the restored template. No singleton or progression baseline is inserted into the template.
5. Closes connections, attaches a candidate/schema/root-specific database comment, sets `ALLOW_CONNECTIONS=false`, and verifies zero remaining sessions. Only then does it exclusively create the private marker containing template name/OID, cluster identity, hashes and inventories. The marker contains no DSN or application rows.

No baseline initialization is needed by this specific composer fixture: its direct post/media/authority/scheduling repositories do not enable automod, reader caching, domain-trust processing or progression. This is not a general claim that every application fixture can use an entirely data-empty clone. A later isolated consumer requiring baseline rows must explicitly review and create its exact baseline in its before-phase clone, never the template or after phase.

## Before and after

Only the saved composer case calls the reusable database helper. Its before phase clones the sealed template through `template1` under a cryptographically unique bounded name and records the clone's OID and comment. Database isolation keeps hardcoded public/wallet schema names and readiness counts unchanged. The helper checks the private marker, exact candidate and schema, root, catalog identity, source connection seal and cluster identity before any clone.

Explicit fixture-pool injection sends every authority seed, policy, media quota, publisher, wallet, reference and post into that database. Original owner/outsider credentials are issued with the existing one-hour bound before composer targets; only their original tokens and public verifier snapshot are saved. The fund's original one-day deadline is unchanged.

The saved JSON contains the clone name/OID/comment and original template/cluster identity, post/reference IDs, original post and all six canonical target rows, ordered link rows, authority state, original proof/public verification material and audit count. After the real whole-cluster stop/start, the case only opens the recorded database. Missing or replaced databases, template/cluster mismatches or changed OIDs fail; there is no recreation, installation, seed, issuance, refresh or financial-row repair path. It compares full original rows before reads and again afterward, authenticates the original proof, and retains all seven owner-visible kinds plus anonymous/outsider private-target redaction and unchanged audit/link assertions.

The private database and markers remain inside the same disposable cluster/run and are not exported as CI artifacts. The existing cluster-identity/postmaster-start evidence continues to prove an actual restart. Only the original 910 before / 107 after required cases are counted; the new controls are offline tests and do not alter group assignments.

## Offline validation and limits

`scripts/test-fixture-isolation.py` simulates the preparer's catalog/tool contract and tests dirty source/template data, baseline drift, schema/extension drift, active sessions, failed seals, duplicate markers, wrong private-CI/root/DSN/hash wiring and after-phase reseeding. It parses generated baseline SQL using the pinned PostgreSQL 16 grammar. Go offline tests exercise filesystem/marker/hash validation and real `database/sql` scans against a connector with no network implementation, rejecting missing/replaced database/OID/comment/cluster/seal values.

Local validation does not start PostgreSQL, open a socket, run the preparer, or execute a real clone. Actual template dump/restore, PostgreSQL catalog/type resolution, trigger behavior, clone isolation and the preserved restart assertion require the authorized private CI run for the final candidate. No passing actual-cluster evidence is claimed by these offline checks.
