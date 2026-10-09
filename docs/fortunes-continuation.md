# Fortune/check-in report continuation

Baseline: continuation `2e6e25f0a9ba31659119b5981338c08edde7bd43`. Delivery owns `ports/passport/fortunes`, `internal/fortunepg`, `tests/integration/fortunes_test.go` and this documentation/manifest. `fortunes-existing.patch` is the approved minimal canonical progression linkage. The sanitized calendar packages are copied as compilation dependencies only; integrate the separately delivered sanitized calendar first.

## Evidenced scope

Pinned authored source establishes two contracts: `/api/fortune` lists sayings, while `/api/fortune/random?language=...` returns a singleton list with case-insensitive language selection. A saying contains `content`, `source` and `language`. Separately, check-in report version 2 is generated asynchronously by a personality provider. Authored callers poll existing GET check-in and monthly calendar until `fortune_report` appears. This is provider-generated entertainment text, not an evidenced astrology calculation. Lunar labels are rendered by the authored client; no lunar/astrology arithmetic is added here.

The typed report preserves the 16 evidenced fields, version 2, source UTF-16 text bounds and structured tip fields. Report fields must be nonempty; malformed/oversized content is rejected rather than silently truncated. Tips are bounded to four with duplicate-title/content rejection; an empty list is supported by the source JSON path. Input duplicate keys, unknown fields and nesting beyond 20 levels are rejected. Provider output is at most 64 KiB. Corpus configuration is at most 2 MiB, with at most 1,000 sayings and 1,000 report templates, bounded text and explicit revision/label/provenance.

`source/manifest.json` records eight included source/license entries and two omitted full sources. Only non-corpus controller/algorithm excerpts are included; complete FortuneSayingController and AccountEventService, their original saying corpus, localized tips, provider prompt and fallback prose are excluded. The official public-doc searches recorded in the manifest did not establish a stronger specific contract. No excluded Shared/Spec/SDK definitions or generated reference pages were inspected. All test content is independently authored and explicitly labeled synthetic; no fixture is compiled as a runtime fallback.

## Runtime and transaction behavior

`fortunepg.Schema` adds `owned_fortune_corpora`, `owned_fortune_config` and `owned_fortune_requests` after canonical authority/account/progression schemas. Corpus installation is an explicitly invoked trusted host configuration method, not a new user/admin HTTP endpoint. Revisions are immutable: replay succeeds, changed bytes conflict. Activating a new revision does not rewrite completed reports or a job already bound to a revision.

The saying endpoints require canonical authentication by default because the retained controller lacks enough global-policy evidence to establish anonymous access. A host may explicitly enable anonymous catalogue reads in both repository and service. Invalid credentials never downgrade to anonymous. Lists are paginated with a truthful X-Total, 1–100 take and bounded offset; random selection uses cryptographic randomness and returns 404 for an unmatched configured language. Responses are no-store. Missing corpus configuration returns 503, not invented sayings.

The optional progression hook enqueues in the same transaction as the canonical check-in and its existing XP receipt. No check-in, level or reward authority is duplicated. Enqueue verifies fresh transaction-bound authority and the canonical stored check-in. It snapshots only account/check-in IDs, canonical UTC day, local report date, profile time zone, language and level. Empty/invalid profile zones use UTC, matching source behavior; there is no implicit language fallback. Later profile edits cannot rewrite the queued request. Account/day isolation comes from the canonical unique daily check-in and a request primary key referencing it.

Provider calls occur after committing the lease transaction, without account/domain locks. Claims and completions lock the canonical account before the report row; they check disablement and active disable-account punishment. Completion checks the current opaque lease, so late/superseded results cannot overwrite newer completion. Successful results are immutable. Failure retries use exponential backoff from 30 seconds, bounded attempts (default five, configurable 1–20), and durable leases. Exhaustion produces failed state; authenticated owner `Retry` is a host method, with no invented public route. It preserves the request/provider binding. Provider-revision disappearance fails the old job explicitly. `ProcessOne` is a bounded, explicitly invoked host worker step; constructors start no jobs or network calls.

Without configured corpus/provider, report generation stays explicitly unavailable. Check-in still succeeds with `fortune_report:null` and an owned optional `fortune_status`; no fake completed report is emitted. Ordinary provider errors preserve the daily check-in and XP. A database enqueue failure rolls back check-in, XP and request atomically. Version query values on the existing check-in route are bounded to 1 or 2; duplicate/unknown query input is rejected. No original version-1 fallback prose is supplied.

Two concrete providers are available:

- `CorpusProvider`: selects a configured complete report template deterministically by account, canonical day and immutable revision, with exact language/level filtering. This is an explicitly owned selection policy, not original corpus or personality-model parity.
- `HTTPProvider`: an operator-selected HTTPS endpoint using an independently owned JSON contract. POST body is typed `fortunes.Request`, header `Idempotency-Key` is `fortune:{checkinUUID}:v2`, and response is `{report,tips}`. Redirects, URL credentials/query strings and invalid responses are rejected. Endpoint/client/revision are host configuration; users cannot choose a destination. This does not claim compatibility with the excluded original personality RPC. No live request or credential was used; tests use an in-memory RoundTripper.

The provider should honor the stable idempotency key because a process crash after a provider response can require a repeat call. Local publication remains exactly once under the lease even when a provider repeats work. No provider request includes birthday, biography, health data, public-event text or earlier reports. Enrichment with source birthdays/public events/notable days/recent fortunes remains an explicit gap; canonical profiles currently have no birthday field. Existing check-in weights, UTC day and XP semantics remain canonical, including the current absence of birthday level 5 and paid backdating.

## Host wiring

Use the exact same pool and TransactionGuard instance for progression, fortunes and calendar so opaque authority proof remains valid across hooks:

```go
reports, err := fortunepg.New(db, guard, fortunepg.Config{Provider: configuredProvider})
progressionStore := progressionpg.NewWithCheckInReports(db, guard, reports)
fortuneHandler := (&fortunes.Service{Repository: reports, Authority: verifier}).Handler()
calendarStore, err := notabledaypg.New(db, guard, notabledaypg.Config{
    Activity: fortunepg.CalendarOverlay{
        Base: notabledaypg.CanonicalActivityOverlay{}, Reports: reports,
    },
})
```

Keep the existing progression service's Bind set to `guard.Bind`. With `Provider:nil`, the worker uses the explicitly installed corpus; no corpus is installed automatically. Register the exact two fortune routes. Call `Ready` after explicit schema installation. Schedule `ProcessOne` only under configured host policy. The calendar decorator performs no enqueue or mutation on reads, retains canonical owner-only check-in privacy and exposes the same completed report through monthly polling. Existing merged activity type-code limitations remain in the calendar module.

## Verification

Passed locally: race tests for fortune/progression packages, vet for changed packages and integration, and race-enabled integration binary compilation. Synthetic transport tests cover typed input, response limits, version/field validation, UTF-16 limits, duplicate tips and redirect rejection. Source hashes are verified and full corpus-bearing files are asserted absent. No PostgreSQL/local socket or live external provider was contacted.

Parent GuardedPG must install `fortunepg.Schema`, canonical progression/presence, and sanitized calendar schema. Before-phase tests:

- `TestPGFortuneConfiguredCatalogueAndDailyIsolation`
- `TestPGFortuneEnqueueAtomicityAndFreshAuthority`
- `TestPGFortuneRetryLeaseRecoveryAndImmutableCompletion`
- `TestPGFortuneWorkerRevocationAndStaleLease`
- `TestPGFortuneConcurrentCheckInAndProfileSnapshot`
- `TestPGFortuneRestartPersistence`

Repeat `TestPGFortuneRestartPersistence` after process/database restart. It verifies a ready report and resumes a pending persisted request; `fortune-record.json` stores only synthetic IDs. Actual PostgreSQL execution remains pending parent CI. New SQL placeholders use explicit UUID/text/timestamp casts, nullable JSON scans use byte slices, and rollback injection uses a scoped trigger. See `fortunes-delivery.json` for exact hashes and patch boundaries.

## Integrated optional host configuration

The composed backend accepts `--enable-fortunes` with an explicit `--fortune-corpus-file` and/or `--fortune-provider-url` plus immutable `--fortune-provider-revision`. It validates bounded corpus JSON and provenance before connecting. Only the `--serve` path installs that explicitly supplied immutable corpus revision after readiness; no corpus is bundled or discovered. Anonymous catalogue reads require `--fortune-anonymous-catalogue`. The separate bounded worker uses `--fortune-interval` (default five seconds), preserving wallet/call worker scheduling. Provider traffic only begins when an operator enables and runs the configured host; validation/tests use no live provider.
