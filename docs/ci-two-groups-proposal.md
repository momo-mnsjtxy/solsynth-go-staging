# Two isolated PostgreSQL contract groups

This proposal was independently reviewed at frozen `15716955258a67d805f986e6a434b5ad7b5708a6` (560/73), then rebased and dependency-reviewed against `c8f2fe79c6f8822534c24ad3926fcfaad4ded917`: 580 before cases and 76 after-restart cases. The 20 added cases and three saved restart fixtures are individually covered in the dependency inventory. A bounded private pilot was approved after this review. Actual two-cluster validation is pending; correctness runs remain independent.

## Expected benefit and limits

The supplied timing sample is from commit `dd964c5bc7f307bd2c248d55c079b64627724d0f`, run `37833319218`, job `113503952738`. Its 516 before cases total 972.57 seconds (16.2 minutes). Six cases account for 348.96 seconds. Fourteen sampled cases failed and 64 newer cases have no measurement, so this is planning data, not acceptance evidence or a runtime guarantee.

The explicit named assignments are recorded in `scripts/integration-groups.json`:

| Group | Before cases | Restart cases | Measured before load | With 5s per unmeasured case |
| --- | ---: | ---: | ---: | ---: |
| group-a | 292 | 36 | 486.29s | 646.29s |
| group-b | 288 | 40 | 486.28s | 646.28s |

The projected parallel before-phase critical path is approximately 10.8 minutes plus setup/build/process-smoke overhead and unmeasured variation. After timings are not available. Full ordinary build/vet/normal/race checks still run once before both jobs. The new static SQL bind-count guard and an exact compiled Go case-list comparison remain mandatory in that job. Both groups install every existing schema and build every existing real fixture host. This consumes two simultaneous GitHub runners; without two slots, elapsed time may increase.

Names are frozen, sorted and explicit. Assignment uses longest estimated case first, with deterministic name and group tie-breaks, after pinning four process-smoke seeds to group-a. Offline checks reproduce the assignment. Every new or renamed TestPG case fails validation until assigned; no fallback runtime hashing, wildcard domain exclusion, or implicit unassigned bucket exists.

## Dependency audit

`docs/ci-two-groups-dependencies.json` inventories all 76 restart cases, source functions, filesystem markers/object directories, named database markers, and assigned before/after group. The offline guard pins each reviewed function, all integration fixture/helper source files and the external smoke scripts by SHA-256 so fixture edits require a refreshed dependency review. The audit also records 44 composed host cases as intact named assignments. The three newly included restart fixtures preserve actual artwork bytes/owner/quota, realm permission generations/overrides/membership, and paid subscription state/events/charge counts using the common exclusive save/load helpers.

Four restart seeds must stay with their external consumers in group-a:

- Develop discovery: `TestPGDevelopDiscoveryRestartPersistence` creates `develop-restart-sentinel`, which `verify-develop-http.py` reads before and after PostgreSQL restart while restarting the application twice.
- Package redirect/counter: `TestPGDevelopPackageRestartPersistence` creates `package-restart`. Its own before read changes the download counter from 0 to 1; after checks 1 then changes it to 2. Only after that does `verify-candidate-http.py` start two real application processes and require two additional increments. Moving that smoke before the after phase would invalidate this contract.
- Bot commands: `TestPGDevelopBotCommandsRestartPersistence` creates the fixed bot and `persist` command consumed by the same candidate smoke.
- NodeInfo: `TestPGNodeInfoRestartPersistence` creates the retained publisher/post in `nodeinfo_candidate`, which the candidate smoke requires to produce exactly one user and one post. Other NodeInfo cases clean up their distinct a700 namespace.

Catalog/badge smoke is self-contained with temporary file-backed configuration, but remains in group-a in both phases. Backend, backend sign-in, Board, Messager, and Taxonomy HTTP process cases each seed their own fixtures and remain indivisible named cases. `TestPGAuthorityLifecycle` launches `TestAuthorityProcessHelper` within that same top-level case using its own temporary synthetic key/token files and database. These process bodies and all composed cross-domain flows are unchanged.

Filesystem restart markers, object stores and synthetic keys remain under each job's original `SOLSYNTH_DISPOSABLE_PG` directory across stop/start. Database-only sentinels remain in that same cluster. The after selector is mechanically derived as the group's before names intersected with the authoritative after registry. A marker cannot move groups between phases. Existing fixture behavior, including cases that reconstruct independent authentication services after restart, is preserved; this proposal does not claim to rewrite or newly certify those inherited semantics.

## Fail-closed aggregate

The existing `contracts` job ID and default check name become the final aggregate. Ordinary checks run in `checks`; `postgres` runs an explicit two-element matrix with fail-fast disabled, retaining the 50-minute job ceiling and exact 30-minute before / 10-minute after Go invocations with `-race -count=1`. No Go source, test body, KDF/crypto parameter, per-case deadline or production behavior changes.

Each group creates one fresh private-socket PostgreSQL cluster. Before and after SQL record `pg_control_system().system_identifier` and `pg_postmaster_start_time()`. A receipt requires the same cluster identity, a strictly later postmaster start, exact passing JSONL cases in both phases, and the expected process-smoke completions. Every raw Go event must have an aware timestamp: before events fall between the initial and restarted postmasters, and after events cannot precede the restart. Any skip is rejected, including a skipped helper with a name that does not start with TestPG. Receipts bind the commit, workflow run, attempt, plan hash and evidence-file hashes. Artifact names include the workflow attempt (`postgres-group-a-attempt-N` and `postgres-group-b-attempt-N`), preventing immutable-name collisions and keeping reruns separate. The aggregate downloads only the current attempt and checks that attempt again inside each receipt. Only the five evidence files plus receipt are uploaded; fixture databases, object stores, synthetic credentials and restart marker contents are not uploaded.

The aggregate runs even when dependencies fail or skip, first requires ordinary and matrix success, then independently recomputes both result logs and receipts. It rejects missing/extra artifacts, overlapping/omitted cases, unexpected tests, wrong packages, skips, failures, stale commits/runs/attempts/plans, absent smokes, no restart, a replaced cluster, or the same cluster identity reused for both groups. No one-group result is a complete contract pass.

Official artifact actions are pinned to verified release commits: [upload-artifact v4.6.2](https://github.com/actions/upload-artifact/commit/ea165f8d65b6e75b540449e92b4886f43607fa02) and [download-artifact v4.3.0](https://github.com/actions/download-artifact/commit/d3f86a106a0bac45b974a628896c90dbdf5c8093). Existing checkout/setup-go pins are unchanged.

## Verification and activation boundary

Offline validation covers exact compiled Go case names, deterministic balancing, all reviewed restart ownership, 89 group/aggregate/runner/workflow/all-job negative mutations, the existing 30 result and two phase-name negative controls, 26 schema/binary/runner negative controls, workflow YAML validation, shell syntax, pinned-source checks, and all ordinary verification configured by `scripts/verify.sh`. Synthetic JSONL inputs exercise guards only and are explicitly not PostgreSQL passes.

Before adopting the proposal, rebase onto the final correctness-green parent and explicitly review every new case and changed dependency. The deterministic draft refresh command below performs assignment and highlights required reviews; it does not activate or overwrite the plan. Rerun the offline guards after that review. Then require an actual complete two-group 580/76 run (or the new exact total) with all receipts/smokes on one commit before treating the parallel workflow as accepted. Runtime speedup and unforeseen cross-test ordering dependencies remain unverified until that real run. No existing required case may be removed to make the split green.

## Inspect every CI job and both result artifacts

Never decide from `jobs[0]`, the first green matrix job, or a package-level `ok`. Fetch the exact attempt's complete paginated job list using the official [workflow jobs API](https://docs.github.com/en/rest/actions/workflow-jobs#list-jobs-for-a-workflow-run-attempt). The read-only helper `scripts/check-integration-ci-jobs.py` prints every job and requires checks, both named PostgreSQL jobs and the aggregate, with all required steps successful on the expected commit/run/attempt. It rejects truncated pagination and a mismatched run attempt; job-level attempt fields are checked when the API supplies them. Fetching the exact attempt endpoint is required. Definitive per-case attempt binding comes from the artifact receipts, so this helper alone never establishes acceptance.

After a separately authorized run, save the run object and all job pages with the existing authenticated read-only GitHub client. The equivalent CLI procedure uses `gh api repos/OWNER/REPO/actions/runs/RUN_ID` and `gh api --paginate --slurp repos/OWNER/REPO/actions/runs/RUN_ID/attempts/ATTEMPT/jobs?per_page=100`, writing each response to a local JSON file. Run `python3 scripts/check-integration-ci-jobs.py run.json jobs.json --commit COMMIT --run-id RUN_ID --attempt ATTEMPT`. Read logs for every returned job ID, or use `gh run view RUN_ID --attempt ATTEMPT --log` without selecting only one job. A pending result remains pending.

Download both named artifacts into separate `postgres-group-a-attempt-ATTEMPT` and `postgres-group-b-attempt-ATTEMPT` directories and run `python3 scripts/check-integration-groups.py aggregate --directory EVIDENCE_ROOT --commit COMMIT --run-id RUN_ID --attempt ATTEMPT`. That second check independently verifies the exact before and after sets from all raw JSONL evidence. The job-status helper alone is not case-level acceptance. This proposal does not execute these remote commands.

## Deterministic refresh for a later exact suite

The original independent artifact was pinned to 560/73; this rebased plan is580/76. A later parent checkpoint may
have more cases; these tools derive exact sets from that checkout rather than
hardcoding either count. First compile its case inventory without executing the
database cases, then write a fresh draft directory:

```sh
"$GO_BIN" test ./tests/integration -list '^TestPG' > /tmp/compiled-pg-cases.txt
python3 scripts/refresh-integration-groups.py \
  --compiled-list /tmp/compiled-pg-cases.txt \
  --output-directory /tmp/review-next-pg-groups
```

The command requires registry, source and compiled declarations to agree. It
keeps known duration estimates, marks new cases unmeasured, deterministically
balances the complete names, derives each after group from its before group,
and refuses silent case removal/rename. It emits a plan, dependency inventory
and review report. New cases, changed restart/composed bodies, changed fixture
helpers and changed process smoke scripts populate `review_required`; such a
plan cannot run until those reviews are completed. New restart entries require
an actual marker/state inventory, not an assumed one. Review the source and
complete those records before clearing the draft's pending-review list.

The tool writes only a new output directory and refuses an existing target. It
never copies its draft over the working plan or changes a workflow. After an
independent review, manually stage the reviewed JSON files in the proposal and
run `scripts/verify.sh`, including compiled-name and static SQL argument checks.
Adoption still requires parent approval and an actual complete run on the final
commit. Offline synthetic event/receipt tests prove guard behavior only; neither
the supplied historical timings nor this review establishes real two-cluster
success, runtime speedup or new database/process-restart evidence.

The full-suite comparison path remains `PG_TEST_GROUP=all bash scripts/integration-postgres.sh`; it executes every before/restart case and every process smoke in one disposable cluster. Partial group success never replaces full aggregate acceptance. The latest bounded fixture changes preserve encrypted-message rejection of plaintext fields and realm authority/lock assertions.
