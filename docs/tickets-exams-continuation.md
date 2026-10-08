# Support tickets and Passport examinations

The source's tickets are private support cases. Examinations are a separate account-entry and permission workflow; passing an exam does not create an event/admission/support ticket. This owned slice implements both without remote mail, pushes, event publication, payments or provider contacts.

## Runtime and schema

New packages: `ports/passport/tickets` and `internal/ticketpg`. Install `ticketpg.Schema` explicitly after canonical authority schema; the configured verified-contact policy also requires the existing signin credentials schema. Constructors/readiness/request handlers never migrate or seed permissions. All protected operations use the same-pool `TransactionGuard`, including reads and administrative permission probes.

```go
repo, err := ticketpg.New(db, guard, ticketpg.Config{
    GrantedGroups: map[string]string{"approved-test-group": existingCanonicalGroupID},
    Activation: &ticketpg.ActivationPolicy{
        RequiredTestKeys: []string{"entry"},
        RequireVerifiedContact: true,
    },
    // Rewards: optional existing progression adapter implementing RewardSink.
})
handler := (&tickets.Service{Repository: repo, Authority: verifierOrHostProofIdentity}).Handler()
```

Route the `/api/tickets`, `/api/tests`, `/api/admin/tickets/on-call`, `/api/admin/tests`, `/api/admin/test-question-groups`, `/api/admin/test-questions` and `/api/admin/test-trials` families through this handler and retain the host's verified gateway aliases. The service binds the verified opaque principal through the repository; account IDs, role headers, cookies and URL bearer aliases cannot become proof. Include `repo.Ready` in startup checks.

The schema contains canonical-account-linked case/message/on-call records; reusable exam groups/questions, definitions, links and trials; immutable attempts and answers; effect receipts/pending requests; and an audit table. It creates no accounts, credentials, permission groups/grant definitions, files, wallet or XP balances.

## Implemented controller operations

- Support: create, list, my list, count, detail, metadata update, soft deletion, append message, assign/unassign and status changes; on-call list/add/remove.
- Exam bank: group list/create/update/delete; question list/create/update/delete, bounded JSON import, group-scoped CSV export and confirmed group-scoped pruning; test list/create/update/publish/archive; trial list/create/update and default trial creation.
- Participation: public listed-test catalog, published-test detail, normal/trial/admin-trial start, own attempt detail, submission, administrative attempt list, manual answer review, admin trial preview/grade and activation status/recheck.

Ticket creators and assigned accounts can view a case; only its creator or an authorized ticket admin can append messages. Metadata, status, assignment and deletion require both their operation permission and `tickets.admin`. Non-admin listing/count queries must explicitly scope creator/assignee to self. On-call membership is not an admin grant. Canonical account ID/name/nick projections are intentionally smaller than the unavailable full Shared account model.

Exam administration uses the documented `tests.manage`, review uses `tests.review`, and participation uses `tests.take`. Only the public catalog is anonymous, and an invalid supplied bearer never downgrades to anonymous. The on-call adapter uses an explicitly owned `tickets.on_call.manage` permission; the retained controller references a symbolic Shared permission constant whose definition was not imported.

## Grading, privacy and effects

The immutable snapshot preserves selected questions, shuffled choice order, grading mode, points, passing threshold and configured effects. Editing/pruning the bank does not rewrite attempts. Selection implements authored category balancing, simple/hard targets and preference for unseen questions; it considers question IDs from at most the latest 1,000 completed attempts. Requested participant category selection requires three to five known categories; trial omission selects all categories.

Exact choice-set matches receive positive points; wrong answered auto questions receive negative points; omitted answers receive zero. Nonempty manual answers wait for review. Manual awards are clamped to 0..question points. Final score is awarded/possible points, capped at 100; an all-zero-point snapshot scores 100, matching the source. The deadline has the source's ten-second grace. Active attempts resume, expired attempts consume the rolling limit, and trials suppress real claims/rewards. The source also checks the non-trial attempt cap when starting a trial; this implementation preserves that behavior.

Participant DTOs never serialize snapshot answer keys, grading configuration or choice correctness. Own submitted answer review fields appear only after submission. Correct-answer snapshots are available only in permission-protected admin attempt projections. Cross-account attempt reads/submissions return 404.

Passing can add membership to an already-existing canonical authority group only when its key→ID is in the host allowlist. Tests cannot mint arbitrary groups or grant nodes. Canonical membership and its exam receipt commit atomically with final grading. Removing a configured mapping prevents new grants and leaves a truthful pending receipt. Existing permission evaluation reads the canonical membership; there is no secondary authorization authority or cache bypass.

Configured experience is once per account/test on the first non-trial pass/fail, preserving the snapshot amount. `owned_exam_rewards` is a receipt, not a balance. A configured `RewardSink.GrantExamRewardInTransaction(context.Context, *sql.Tx, accountID, testID, attemptID string, amount int64) error` joins the transaction and must target the existing progression provider idempotently. A failed sink rolls back to a savepoint and leaves the exam completed with a pending receipt; it cannot leave a partial reward mutation. No network callback or retry scheduler is started.

Activation checks configured required non-trial passes and the existing canonical verified-email timestamp. Satisfaction creates one durable pending request. `is_activated` remains false: no disabled account is enabled, no session changes, no email-verification bypass and no false completed activation. A later configured authority policy must consume/revalidate the request. A missing policy is reported as unconfigured rather than successful activation. Registration invitation bypasses are unsupported.

## Bounds, transactions and owned differences

- JSON bodies ≤2 MiB, duplicate/unknown fields rejected, nesting ≤24. Pages default to 20 and cap at 100. Keys/text/configs have explicit finite bounds.
- At most 16 groups and 200 questions per test, 32 choices per question, 200 questions per group, 2 MiB snapshot, 200 messages per case and 100 on-call entries. Import/prune/export require one bounded group.
- Case/message creation and attempt start optionally bind `Idempotency-Key`; changed payload replays conflict. Identical submissions and completed manual reviews return the committed result without duplicate answers/effects. Other operations reject that header rather than ignoring it.
- Canonical actor/proof locks precede domain rows. Test definitions precede group locks sorted by UUID. Manual review takes a target account lock with NOWAIT after the reviewer lock, so inverse account ordering returns a retryable conflict instead of waiting in a cycle. Claims, answers, final status and audits share one commit.
- Duplicate answer/question/choice IDs and unknown choices fail rather than source last-wins/ignored-input behavior. Group reassignment on question update is explicitly unsupported. Group-key renames remain reflected in linked test projections.
- Ticket types/priorities and the older client status mapping are bounded to 0..3, with priority default 1. Newer waiting-status enum values require verified encoding before support. Case deletion uses owned tombstones. Resources are bounded strings, never fetched.
- Ticket file IDs remain explicitly unsupported, including source `usage: 'ticket'`; no raw references confer file access. Push/email delivery, full Shared model parity, contact spell integration, global bank prune/export, dedicated docs-only listing route and production provider wiring are not claimed.
- Errors use stable owned Passport codes; cross-account ticket detail uses 404 rather than the source's existence-revealing 403. Count/page metadata follows the guarded ReadCommitted transaction; it is not advertised as a cross-query repeatable snapshot.

## Evidence and verification

`ports/passport/tickets/source/manifest.json` records all retained controllers plus exact-blob-verified authored TestService, SnTest, TicketService, TicketOnCallService, activation documentation and Solian support consumers at the already pinned revisions. Root AGPL licenses are retained. No excluded Shared definitions, generated SDK code, appsettings or secrets were fetched.

Passed locally: `go test -race ./ports/passport/tickets ./internal/ticketpg`; affected-package `go vet`; complete integration package compiled with `go test -c -race`. No local PostgreSQL listener/socket was attempted and no PostgreSQL execution result is claimed.

Add these GuardedPG tests to required-before:

- TestPGTicketsPrivateSupportLifecycle
- TestPGExamsSnapshotsClaimsAndPrivateAnswers
- TestPGExamsManualReviewTrialsAndLimits
- TestPGExamsIndependentPoolIdempotencyAndAtomicClaims
- TestPGExamsFreshAuthorityAndDisabledIsolation
- TestPGExamsRewardSinkAtomicAndIdempotent
- TestPGTicketsExamsRestartPersistence

Add TestPGTicketsExamsRestartPersistence to required-after. The suite covers privacy/roles, real HTTP routes, immutable answer snapshots, canonical group grants, manual grading/trial suppression, deadline/caps, cross-pool replay, policy waiters, disabled accounts, injected claim rollback, reward savepoint recovery, pending activation and process/database restart. All identities and provider failures are synthetic inside the marked disposable database.

Final SQL audit: repeated text/UUID ticket filters have explicit text→UUID casts; optional status/trial parameters are explicitly typed; nullable fingerprints and JSON-derived titles scan through `sql.NullString`, while JSON documents scan through `[]byte`. Time-window arithmetic happens in Go and binds `time.Time` values rather than ambiguous interval parameters. Rollback fault injection uses an actor-scoped synthetic trigger, so it introduces no validating CHECK constraint over preexisting rows.

The unified host defaults the typed exam reward sink to the canonical progression XP ledger and verifies the grading receipt in that same transaction. A composed HTTP test proves one11-XP credit and one canonical group claim after submission/replay. The executable exposes explicit exam group mappings and activation eligibility flags; no group is created and no account is enabled by configuration.
