# Calendar and notable-day continuation

Isolated delivery from continuation `5149232` in the sanitized `solsynth-go-next-notabledays-sanitized` copy. The original frozen copy is unchanged and must not be published as-is: its full AccountEventService.cs snapshot is excluded here because of unrelated fortune fallback prose. Its source URL/hash remain omission metadata only. New files only: `ports/passport/notabledays/`, `internal/notabledaypg/`, `tests/integration/notabledays_test.go` and the three `docs/notabledays-*` documents/manifests. Earlier post-media, usage and tickets deliveries remain frozen.

## Routes and storage

- `/api/notable-days`: anonymous GET catalogue with region/year/tag/layer/count/paging; permissioned POST. `/{id}` supports GET/PUT/DELETE.
- `/api/accounts/me/calendar`: monthly daily entries; `/merged`, `/search`, `/countdown`, `/tags` and `/notable-days/{occurrenceKey}` provide source-shaped projections.
- `/api/accounts/me/calendar/events`: owned POST and accessible GET feed. `/{id}` supports visible GET and owner-only PUT/DELETE. `/api/accounts/{name-or-id}/calendar`, `/merged`, `/countdown`, `/events` and `/events/{id}` provide current-audience reads; `/api/accounts/unknown/calendar/events/{id}` resolves a visible event directly.
- `/api/accounts/me/calendar/subscriptions`: GET target IDs, `/{accountId}` POST/DELETE, `/subscribers` GET subscriber IDs. Subscriptions are idempotent and cannot target self.

`notabledaypg.Schema` creates `owned_notable_days`, `owned_calendar_events`, `owned_calendar_subscriptions` and `owned_calendar_audit`. Catalogue/event deletes are soft deletes; subscription removal is durable deletion. Existing canonical authority accounts and social relationships are dependencies. This module does not enroll identities, seed permissions, grant rewards or mutate social state.

Every authenticated operation uses the same-pool `authoritypg.TransactionGuard` with bound fresh proof. Mutations require `notable.days.create/update/delete`, `calendar.events.create/update/delete`, or `calendar.subscriptions.manage` from the existing permission catalogue. Ownership is required independently. The actor account is locked before event/catalogue rows; subscriptions first lock actor and target in canonical UUID order, matching social writers, then recheck proof and lock the subscription. Creation limits and replay checks are protected by those locks. Catalogue creation uses a catalogue table lock for its global limit. Audit failure rolls back the whole mutation.

`Idempotency-Key` on event creation binds the normalized request body. Identical retries return the original current event; a changed request conflicts, and a deleted original remains deleted. Other operations reject that header rather than promise unsupported replay semantics. Event edits increment a durable version. All responses are `Cache-Control: no-store`; reads check current deletion, owner enablement, relationship and audience state.

Anonymous callers see public events only. Authenticated callers see own private events, mutually established friends' friends/public events and subscribed public events. Bilateral blocks suppress cross-account reads regardless of subscription, and disabled owners disappear. A known UUID does not bypass privacy. Public event/calendar projections use read-only ReadCommitted transactions and revalidate assembled event versions, media and activity privacy before returning; catalogue-only reads retain repeatable-read snapshots. Concurrent changes can return a retryable conflict or not-found response rather than a stale partial projection. Unsubscribe remains available after blocking/target disablement.

## Dates and projections

Event timestamps are instants. Optional `time_zone` is an IANA zone, defaults to UTC and is persisted; host-local `Local` is rejected. `timeZone` controls monthly boundaries without changing each event's recurrence zone. Named-zone data is embedded with Go's `time/tzdata`. Supported ordinary event/catalogue years are 1900–2199. Derived monthly windows allow adjacent UTC boundary days so the first/last local months work in positive and negative offsets.

Recurrence preserves the original local clock. Daily/weekly intervals use civil dates; explicit weekly weekdays honor the interval. Monthly/yearly recurrences clamp to the original requested day in short months, then return to that day. Ambiguous DST wall times select the earlier instant; nonexistent wall times skip that nominal slot. The supplied first instant is preserved. Timed events retain elapsed duration; all-day events spanning civil dates retain that span, including 23/25-hour DST days. `occurrences` counts nominal slots, including skipped gaps; `end_date` is exclusive. JSON `recurrence:null` clears recurrence. Expanded events have stable `eventUUID@UTCinstant` keys and no nested recurrence.

Solar notable recurrence uses `MM-DD`; invalid dates in a particular year have no occurrence. Lunar recurrence supports Gregorian 1901 through partial 2101. Official calendar bounds are 1901-02-19 through 2101-01-28; unsupported lunar years return an explicit unsupported response. Regular months use the source's previous-year and month-12/day-30 fallback. Period offsets are measured from the recurrence date in the configured start year, with an exclusive end. Adjacent-year spillover is included in the actual requested year. Nonrecurring periods retain the source's single start-date occurrence.

Notable dates are civil date labels represented at UTC midnight. Monthly projections assign these to the same local calendar date rather than shifting a holiday into the preceding day. Keys are `lower-region|YYYY-MM-DD|normalized-localizable-key-or-name`; current catalogue data is resolved on detail requests, so edits/deletes invalidate old keys. Duplicate catalogue keys conflict rather than select an arbitrary record. Only numeric JSON integer priorities affect `layer`; numeric strings use the source default 3.

Bounds: pages 1–100; offset at most 100,000; 1,000 live events/account; 1,000 live catalogue rows; 100 subscriptions/account; 1,000 list candidates; 4,096 expanded results; 732 days per event window. Overflow returns an explicit limit error, never a silently truncated count. Tags are normalized/deduplicated and limited to 16. JSON bodies are at most 128 KiB and metadata 16 KiB; duplicate/unknown fields are rejected. Event/notable durations are at most 366 days. Recurrence interval is 1–366 and explicit occurrence count is 1–10,000; omitted count supports long-lived recurrences using direct range seeking. Countdowns select up to three nearby occurrences per recurring event, ordered ongoing, future, then recent past.

Intentional source corrections: anonymous private-event lookup is denied; recurring dates retain wall-clock time and original month-end anchor; weekly interval is honored with explicit weekdays; long-lived daily events remain visible; periods crossing New Year resolve on both sides; merged user-event lists do not repeat a multi-day occurrence once per day. Activity overlays respect canonical provider privacy.

## Host wiring and explicit gaps

Apply canonical authority/account and social schemas before `notabledaypg.Schema`, using the explicit owned migration runner. Configure the canonical pool/verifier:

```go
guard := authoritypg.NewTransactionGuard(authoritypg.New(db), verifier)
repo, err := notabledaypg.New(db, guard, notabledaypg.Config{
    Activity: notabledaypg.CanonicalActivityOverlay{},
})
handler := (&notabledays.Service{Repository: repo, Authority: verifier}).Handler()
```

The concrete activity adapter requires existing `presencepg.Schema` and `progressionpg.Schema` first. It reads only `owned_presence_preferences`, `owned_presence_statuses` and `owned_progression_checkins` within the caller's transaction. No progression imports, duplicate XP store or writes are needed. Status audience follows the canonical public/friends/private preference, mutual friendship and blocks; invisible status becomes default for other viewers. Check-ins remain owner-only, matching current canonical progression policy. Raw status metadata/device fields are not exposed. Unavailable progression fields remain truthfully null/empty.

Daily status/check-in fields work with `CanonicalActivityOverlay{}`. `CheckInType` and `StatusType` pointers may be supplied only after verifying the merged wire enum contract. Otherwise `merged_check_ins`/`merged_statuses` appear in `unavailable_components`; daily fields and the independently authored string-kind `activity_events` array remain available. Without an activity adapter, monthly output lists `statuses`/`check_in_result` as unavailable. `Ready` checks configured schema dependencies. Mount only the exact calendar/notable routes; do not replace existing account/status routes.

No self-reminder lifecycle or notification producer is included because it is not evidenced in this source family. Calendar event icon/background associations are now connected when canonical media is configured; see `calendar-media-continuation.md` for transactional ownership, current audience checks, removal and download authorization. Without media configuration, media mutations still return 501. No third-party regional holiday assets are seeded.

## Verification and parent integration

Passed locally without PostgreSQL/network/listeners: race-enabled Go unit tests; `go vet` for both new packages and the integration package; integration binary compilation with race instrumentation. The independent offline oracle rerun passed all 74,772 cases on .NET 10.0.12 using installed SDK 10.0.401. Resolution digest: `c6956b73e3c604bf04dce1073995c25656bc0425838e691efb37715e0d6f77eb`.

`bash ports/passport/notabledays/oracle/verify.sh` reproduces the oracle. It clears package sources, disables diagnostic endpoints and ASP.NET certificate generation, and keeps CLI homes/build artifacts in a fresh `/tmp` directory. It never inspects/reuses certificate stores. Go needs no .NET runtime.

Parent GuardedPG must install `notabledaypg.Schema` after canonical schemas, plus presence/progression schemas for the overlay test. Include these tests in the **before** phase:

- `TestPGNotableDayCatalogLunarAndStableLookup`
- `TestPGCalendarPrivacySubscriptionsAndSearch`
- `TestPGCalendarRecurrenceMutationAndHTTPBounds`
- `TestPGCalendarCanonicalActivityOverlayPrivacy`
- `TestPGCalendarAtomicityAndFreshAuthority`
- `TestPGCalendarRestartPersistence`

Run `TestPGCalendarRestartPersistence` again in the **after** phase. It uses `calendar-record.json` under the guarded disposable state directory. Atomicity covers same-key two-pool creation, reciprocal subscriptions, actor-scoped audit failure rollback, and permission revocation while waiting for the account lock. Nullable JSON uses byte slices; nullable request hashes use `sql.NullString`; new SQL placeholder text/UUID/timestamp types are explicit. Failure injection uses a scoped trigger rather than an invalid CHECK over older rows.

Actual PostgreSQL execution is pending the parent's authorized private CI runner. No local socket attempt was made. `notabledays-delivery.json` records exact file hashes, schema/source manifest hashes and validation commands.
