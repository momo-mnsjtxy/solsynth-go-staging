# Account profile, contacts and notification preferences

This delivery extends canonical accounts and sign-in on baseline `8e223ab2b1e88816d910c01fff9feff5900a20e1`. It adds no identity, password, primary-email or notification-delivery authority. The source manifest in `ports/passport/accountsettings/source/manifest.json` verifies complete licensed authored Solian callers, retained Watch DTOs, Dyson controllers and permission documentation. No excluded SDK/schema, unlicensed media or fortune prose was used.

## Supported source and contracts

The authored Solian profile editor submits `PATCH /stargate/accounts/me` for basic account fields, and `PATCH /stargate/accounts/me/profile` for profile fields. These now work alongside the existing `/api/accounts/me` route and the owned `/api/accounts/me/profile` alias. First/middle/last names and gender are bounded strings. Existing biography, pronouns, location, time zone and links remain supported. The caller's unchanged read-only account name is accepted; a different name retains the paid-name-card error. Neutral `username_color: {"type":"plain"}` or null is accepted without inventing paid styling.

Birthday is the caller's timestamp, persisted in UTC; supplied time zone must be an IANA identifier (or empty for UTC), never the machine's `Local` zone. Validation uses that zone's civil date, from 1900 through today. Omission preserves the birthday; explicit JSON null clears it. Only authenticated self GET and profile-update responses include it. All public account and nested publisher projections omit it. No source-backed birthday disclosure policy was established.

The authored contact settings screen establishes these routes and numeric input kinds: email 0, phone 1, address 2.

| Route | Result |
| --- | --- |
| `POST /stargate/contacts` with `{type,content}` | Creates an owner-private, unverified contact; 201 |
| `DELETE /stargate/contacts/{id}` | Deletes a secondary contact; 204 |
| `POST /stargate/contacts/{id}/verify` | Sends through the existing configured delivery adapter; 202 only after successful send |
| `POST /stargate/contacts/{id}/primary` | Promotes a verified email atomically; 200 |
| `POST` / `DELETE /stargate/contacts/{id}/public` | Sets / clears opt-in public visibility; 200 |
| `POST /stargate/accounts/verify` with `{code}` | Existing verification-completion contract, still 204 |

The member actions have no required request body. `GET /stargate/contacts` is an explicitly owned bounded owner-list counterpart; its original SDK route is unresolved. DTO field names follow the retained authored Watch model. Creation accepts only exact `type` and `content`, rejects duplicate/unknown fields, null kinds and malformed UTF-8, and is bounded to 8 KiB. Lists have at most 32 live contacts. Phone/address records remain private and cannot be verified or promoted without a meaningful provider contract. Unconfigured email delivery returns 503 and never reports verification. No live delivery ran during development.

The source exposes normal/silent/reject preference choices and custom topics, but the original SDK route and numeric enum are not available. The explicitly owned string API is `GET /api/accounts/me/notification-preferences` and `PUT` / `DELETE /api/accounts/me/notification-preferences/{topic}` with optional existing `?app=...` tenant. PUT accepts `{preference:"normal"|"silent"|"reject",description?:string}`. Exact topics have no wildcard syntax; at most 200 overrides exist per account/app. This API does not claim original SDK route or enum parity.

## Canonical state and transaction boundaries

Primary email and verification remain solely in `owned_authority_credentials`. A primary contact metadata row references that credential row and stores neither a copied email nor copied verification. Secondary verified emails are uniqueness claims, not login aliases. Promotion updates the existing credential, preserves contact IDs, converts the previous primary into a secondary record, increments canonical account version, and cancels prior challenges in one transaction. Existing account-version checking forces fresh sign-in; factors and password ownership remain unchanged. A primary cannot be deleted.

All ownership writers use the same account-first, sorted email-advisory-lock order: registration, contact creation, verification, promotion and deletion. Canonical actor locks serialize owner mutations. Sorted domain-separated SHA-256-derived lock keys serialize email ownership across actors; collisions can only add serialization. Pending contacts do not reserve an address. Primary credential claims and verified secondary claims are checked together, so verification and registration races have one winner. The per-account unique index and verified-email unique index are additional constraints. No global account lock is introduced.

Owner operations bind the verifier's original opaque proof to the canonical transaction guard. Private reads and mutations require `account.contacts.manage`; creating, deleting, promoting and the owned address-only update additionally use the existing recent first-party-login rule. Management authorization is refreshed after waiting for email locks. The recent-login window is checked again after domain mutations, so a blocked operation rolls back if that window expires. Verification challenges are purpose-, account-version- and contact-bound, hashed at rest, single-use, and checked for expiry and active account status again before commit. The existing verification response is unchanged. Send-rate accounting precedes the actor transaction, avoiding a second pool connection while the transaction is held. Delivery occurs after commit using the existing seam; a failed send returns an error and a later request replaces its pending challenge.

Profile mutations still require `accounts.manage`. Preferences require `notifications.preferences.manage`; reads require current owner proof. The preferences adapter rejects app/pool mismatches. Public contacts are queried from the canonical store on every lookup, require current verified and opt-in state, exclude disabled/punished accounts, and reject a different database pool. HTTP responses are `no-store`. Contact response timestamps are read back from SQL, including primary verification timestamps; profile fields are also rehydrated after persistence. Existing verifier/sign-in clocks remain shared through their configured clock; preference audit timestamps use database time and have no authorization role.

Preference projection runs inside the notification worker's existing recipient transaction. Normal preserves prior behavior. Silent keeps inbox/replay but creates no external delivery. Reject consumes the notification outbox receipt without creating inbox/replay/external output. Dispatch rechecks the current preference before leasing and durably suppresses queued silent/rejected deliveries. Existing already-visible inbox entries are retained; an external send already begun cannot be recalled. Wallet domain events are unaffected. No external sender is called by tests.

## Schema and host wiring

Apply `authoritypg.Schema` first, then the updated `signinpg.Schema` and `accountpg.Schema`, followed by `accountsettingspg.Schema`. Sign-in schema adds contact metadata, audits and the optional contact challenge reference, backfills canonical primary metadata, and expands the known challenge purpose constraint. Account schema adds profile columns. Settings schema adds preferences and their audit. Constructors never migrate. Applying only the new settings schema is insufficient.

1. Reuse the host's one database, canonical authority repository, verifier and transaction guard for `signinpg.New` and existing `signin.New`; reuse the same configured sign-in clock and delivery.
2. Configure `accountpg.NewWithAuthority(db, guard).WithPublicContacts(signinRepository)` before serving.
3. Existing `signin.Service.Handler()` now registers contact routes. The host dispatcher must route `signin.ContactRouteMatches(path)` to it. Preserve the existing `/stargate/accounts/verify` path and response.
4. Build `accountsettingspg.New(db, guard, notificationApp)` and mount `accountsettings.Service{Repository: settings, Authority: verifier}` where `accountsettings.RouteMatches(path)` is true, before the generic account handler. Include `settings.Ready` in readiness.
5. Configure the existing notification repository with `.WithPreferencePolicy(settings)` before starting request or worker goroutines. The seam receives the existing transaction and identical pool; it performs no new transaction or external call.

The delivery does not change the host's router, installer or policy provisioning. The parent integration must supply the named grants through its current policy configuration. It must retain independent existing organization/realm/OAuth gates while applying shared-file changes.

## Verification and limits

Focused unit/race tests cover strict preference/contact inputs, authored profile aliases, birthday clearing and provenance. Six guarded PostgreSQL tests are delivered in `tests/integration/account_settings_test.go`: `TestPGProfileAuthoredAliasesAndPrivateBirthday`, `TestPGContactVerificationPrimaryAndPublicLifecycle`, `TestPGContactEmailOwnershipAndRegistrationRace`, `TestPGContactAtomicityExpiryAndAuthority`, `TestPGNotificationPreferenceProjectionAndQueuedDelivery`, and `TestPGAccountSettingsRestartPersistence`. They cover privacy, no-mail configuration, purpose isolation/replay, promotion and stale proofs, owner isolation, pool mismatch, ownership races, rollback, expiry while blocked, account disablement, queued suppression and before/after restart persistence. HTTP fixtures reuse the parent's explicit JSON/no-body helpers. These tests compile here; real PostgreSQL execution is reserved for the parent's guarded private CI.

Remaining gaps are explicit: original SDK preference routes/enums and owner contact-list route; source admin contact-management route; phone/address verification; profile picture/background attachment policy; paid name cards and paid username styling; security-mode effects, PassKit and rewind. Existing source references document those boundaries rather than claiming implementation parity.

The composed host shares its exact sign-in repository with public-contact hydration, routes authored contact operations to the configured sign-in service, mounts the owned notification preference API before generic account handling, and wires preferences into the existing notification repository without replacing room visibility. The typed host fixture covers private profile fields, synthetic secondary-email verification, public projection, preference routing and stale authority after primary promotion. Actual PostgreSQL evidence remains separate from local checks.

Address-only detail/update and canonical order destination capture are documented in [shipping addresses and fulfillment](shipping-addresses-and-fulfillment.md). Saved contacts remain the single address source; payment snapshots are immutable order history, not a second editable address book.
