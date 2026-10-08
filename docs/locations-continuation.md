# Location, Meet and plain NFC continuation

This is an owned PostgreSQL implementation of licensed, authored user flows. It never reads device location, performs physical NFC operations, generates NFC keys or fabricates external-provider results. All tests use synthetic accounts, coordinates and tag references.

## Source contracts

`ports/sphere/locations/source/manifest.json` records retained SHA-256 hashes and pinned URLs. DysonNetwork sources/docs are pinned at `4ee2d0cebb9da8284a92483fee6dce795bf6947c`; authored Solian meet/pin parsers and NFC caller are pinned at `ffd0838e5f04cb40bea311a39118ebeedad210c1`. Licenses are retained beside them. No excluded repository, generated protocol or SDK model was fetched.

Authored Solian parsers directly establish visibility public/private/unlisted = 0/1/2; meet active/completed/expired/cancelled = 0/1/2/3; pin active/offline/removed = 0/1/2. `PASSPORT_MEET.md`, `PASSPORT_PINS.md`, controllers and services establish host ownership, participants, deadlines, per-session pin changes, public/friend/private filtering and plain NFC profile association.

## Schema and composition

Install `internal/locationpg/schema.sql` explicitly after canonical authority, account and social schemas. Tables: `owned_meets`, `owned_meet_participants`, `owned_location_pins`, `owned_nfc_tags`, `owned_location_audit`. Account/session/participant references point to canonical tables. No second account, realm, relationship or device-authentication store is introduced.

Construct `locationpg.NewWithAuthority(db, guard)` and mount `locations.NewHandler(repo, auth)` with the existing post-style verified `Authenticate`/`Authorize` adapter. `BindAuthority` is implemented. `NewVerifiedHandler` supplies a standalone verification boundary for integration recorders. Unguarded `New(db)` follows existing trusted fixture patterns and cannot create pins because it has no verified session proof.

Each authenticated operation checks its original proof and current policy inside a read-committed transaction under the canonical actor-account lock. Mutations retain domain locks through their audit insert and commit. Meet locks precede pin locks, avoiding a read/update inversion. Unique participant and active-pin constraints enforce duplicate invariants across pools. Pin session identity comes from the verified authority session, never `X-Device-Id` or a caller-supplied account identifier.

## Implemented routes

- `/api/meets`: create, list, nearby, detail; join; complete; visibility update; delete; create/update/delete own meet pin; list visible meet pins. The host owns state changes. Joining is idempotent. Duplicate create requests from the same host within five minutes return the same equivalent active meet.
- `/api/pins`: create, detail, location update, disconnect, delete, nearby, `me`/`my-pins`, stream. Defaults are private and keep-on-disconnect. Five-second update throttling is durable. A nearby replacement can reuse its active record; a moved replacement retires the previous pin. Only the owning authenticated session can mutate a specific pin.
- `/api/nfc`: resolve plain UID or record ID; admin-permission registration; owner list, label/activation update and delete. UID is normalized uppercase and bounded hexadecimal. Inactive/deleted tags and disabled/blocked accounts do not resolve. Inactive rows remain visible to their owner for reactivation. No NFC keys, SUN verification, encrypted claim, physical write/lock or fabricated scan timestamp is produced; those routes fail explicitly with 501.

Meet join with `Accept: text/event-stream` and pin stream emit an initial snapshot and poll committed state every second. Every poll rechecks signed authority and current domain visibility. Streams terminate on revocation, hidden/deleted resources, cancellation or terminal meet state. These are live current-state streams, not a claimed durable event-history replay. Without the SSE Accept header, join returns the same state as JSON.

## Privacy and state rules

Meets and pins default to private. Private meets are host/participant-only; unlisted meets allow mutual canonical friends of an eligible host/participant. Public outsider discovery requires explicitly supplied coordinates within 5 km; a supplied point is search input, never physical-presence attestation. Public location remains deliberately public. Private access is never granted by coordinates, a tag UID or realm membership. There is no authored realm link in these models, so the implementation does not invent a realm access grant.

All visibility paths apply canonical bilateral blocking and disabled-owner filtering before projecting results/counts. Pin visibility is independently checked inside a meet: a host does not automatically receive a participant's private pin. Foreign pin projections omit the owning session ID. NFC resolution uses a bounded canonical public account projection, omitting contacts, credentials, grants and private authority metadata. Anonymous NFC profile resolution does not mutate `last_seen_at` or claim that a physical scan occurred.

Only WKT `POINT(longitude latitude)` is accepted, with finite longitude ±180 and latitude ±90. Points, labels and addresses come only from explicit request input. Nearby calculations use great-circle distance, including correct date-line handling. Polygons, provider geocoding and device-location acquisition are unsupported. Metadata is bounded to 8 KiB; page size is 1–100 and radius is at most 100 km.

Meets default to 30 minutes, with supported TTL 60 seconds–24 hours. Pin updates persist immediately. Five minutes without a heartbeat derives offline/removal; retained offline pins expire after 24 hours. Reads enforce deadlines even after a restart, without requiring a process timer. `Expire(ctx, limit)` optionally materializes only deadline-derived states with bounded `SKIP LOCKED` batches. It does not initiate notifications or hardware actions.

## Deliberate differences and gaps

- The pinned meet service grants friendships access to private meets and returns public meets before its distance check; the authored API document describes stricter rules. This implementation follows the documented private host/participant rule and 5 km public-discovery limit. This is a disclosed correction, not exact parity with that service bug.
- Immediate durable pin writes replace the source's in-memory 30-second flush. Heartbeat expiry derives from the last heartbeat, so restarting late cannot extend retention. Soft deletion reserves identifiers and audit history. Plain tag registration reserves deleted UIDs.
- Metadata/account model hydration is a bounded owned projection. Image attachments currently fail with 501; no guessed media permissions. PostGIS, rich geometries, notifications, durable event replay, precise source trail rendering and production providers are not claimed.
- Discovery currently enumerates candidate IDs before applying shared-domain privacy and paging. Result sizes are bounded, but large deployments should replace enumeration with a shared visibility SQL projection.
- No physical NFC credential or hardware lock can be inferred from the plain tag association. Encrypted claims require an independently configured secure provider and are intentionally absent.

## Verification

Focused `go test -race` passes for point/enum/plain-tag validation and the package build. `tests/integration/locations_test.go` compiles; actual PostgreSQL runs are the parent integration runner's responsibility and are not implied by skips.

The synthetic integration cases cover private/friend/nearby/participant separation, terminal states, duplicate creation across independent pools, failed-audit rollback, durable deadlines, owning-session isolation, update rate limits, private pin projection, block changes, NFC owner isolation, inactive/deleted resolution, signed HTTP and real SSE completion through recorders, and queued authority revocation preventing location persistence.

Before phase: `-run 'TestPGLocations'`. After a real PostgreSQL restart: only `TestPGLocationsRestartPersistence` with `PG_TEST_PHASE=after`; it reads prior meet, offline pin and NFC association records without schema/seed/token writes. Both phases use the existing disposable-cluster marker guard. No worker-started network listener is used.
