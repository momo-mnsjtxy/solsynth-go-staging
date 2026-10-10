# Identity, realm and progression completion

This lane implements owned current-version business operations. It does not
import legacy records, restore unknown historical XP, add old packet formats,
or configure an external payment channel. SourcePoints use an explicitly funded
internal treasury and the shared wallet transfer implementation.

## Runtime operations

| Capability | Entry point or consumer | Authoritative source |
| --- | --- | --- |
| Account administration | `/api/admin/accounts`, account detail, sessions, credentials, contacts, factors, credits | Root session, explicit global `accounts.manage`; credit grants also require `credits.records.manage` |
| Verified provider link | `/api/accounts/me/connections/{provider}/start` and `/complete`; UUID unlink/privacy mutation | Server provider response and single-use, original-session-bound challenge |
| Sensors | `POST /api/accounts/me/presence/sensors` | Original `presences.activities.manage` proof and the canonical activity writer |
| Provider presence | `/api/accounts/me/presence/providers/{steam,lastfm}` and automatic worker | Verified link, bounded per-account opt-in, original session/version/scope, current provider observation |
| Push registration | `/api/notifications/devices`, `/confirm`, UUID revoke | FCM-delivered random possession challenge, original root session, `notifications.devices.manage` |
| Push delivery | Existing durable notification worker | Current registered device consent and a fixed configured FCM transport |
| Member query | Existing realm member route with name, label, order and status filters | Current joined member incarnation, realm permission and privacy-aware status projection |
| Member XP | Backend maintenance consumer | Committed authored chat/post records and current paid perk policy |
| Member tenure | Backend maintenance consumer | Explicit tenure policy, active joined member, unique realm/account/UTC-day receipt |
| Social credit | Account admin credit mutation | Explicit trusted admin policy and immutable source record |
| SourcePoints | Backend earned-grant consumer | Immutable earned grant, explicit funded treasury, canonical decimal transfer |
| Birthday and backdated check-in | Existing check-in route | Private canonical birthday or current paid configured entitlement |
| Seed synchronization | `POST /api/admin/progression/sync` | Explicit `progression.admin` and `progression.sync` grants; embedded licensed definitions |
| Identity achievements | Backend canonical progression projector | Ready owned avatar, complete canonical profile, verified provider, enabled TOTP, verified push device, real friend acceptance |
| Night-owl reminder | Backend maintenance and account admin notification route | Current account-local hour, recent eligible activity, durable hourly receipt and notification preferences |
| Admin Steam scan | `/api/admin/accounts/presences/steam/scan` and scoped scan routes | Existing owner-authored Steam consent; an admin cannot fabricate owner consent |
| Request history context | Backend HTTP middleware and canonical audit transactions | Actual `RemoteAddr`, original authenticated session and sanitized display-only user agent |

Account admin contact operations use the authority-owned contact/credential
records. Factor and credential changes retain assurance requirements for
self-service; the administrative route rejects self-targeted credential,
factor and session changes. Password hashes, factor secrets, sealed refresh
credentials and provider tokens are not returned by aggregate reads.
Security mutations also reject an UPDATE-locked superuser target; one root
administrator cannot replace another root's login or recovery identity.

Every provider operation checks the original opaque proof before external I/O
and again after I/O and local waits. Poll consent records the original root
session, account version and authorization epoch; a worker reconstructs no
impersonated owner principal. Revocation, expiry, policy loss, disabling and
scheduled punishment stop renewal. Activity leases are bounded by consent.
Claim leases and provider rate limits are durable across processes.

Push device tokens are AES-GCM encrypted at rest. Device reads expose only a
token hash and public registration metadata. The challenge secret is delivered
only through the configured provider. A device identifier or token already
belonging to another account cannot be taken over. Registration proof is
single-use and expires; only a verified registration creates
`account.push_enable`. Normal provider delivery carries public wake-up IDs and
a stable delivery key, rather than private notification contents.
Delivery fixes its device audience once and commits a per-device attempt fence
before provider I/O. No account/device transaction is held during FCM calls.
Accepted devices remain deduplicated across partial failures and restarts.
FCM has no server idempotency operation for acceptance-before-ack crashes:
an expired sending fence or ambiguous provider outcome becomes durable
`uncertain`, is never blindly resent, and never marks the queue delivered.
`DELIVERY_UNCERTAIN` terminates the outer delivery with `suppressed_at`,
clears its lease and leaves `delivered_at` null. Replacement workers do not
lease it again. In-progress fences remain retryable. No provider errors or
tokens are retained. The canonical inbox remains available independently.

SourcePoints do not mint funds. The source point amount is converted with
`ParseAmount(strconv.FormatInt(points, 10))`, never floating point. The adapter
validates both active owners, treasury wallet ownership and canonical currency,
enforces grant/day limits, and calls `walletpg.FundedTransfer` in the earned
grant transaction. Transfer, `source_points_granted_at`, payout outbox and any first
primary-wallet creation commit together or roll back together. The transfer
key is the domain-prefixed immutable grant UUID. Wallet locking is delegated
once to the shared UUID-sorted transfer helper; there is no wallet SHARE-to-
UPDATE upgrade. Treasury account locks use NOWAIT to avoid reverse account
lock cycles, and contention is retried by the worker.

Request facts annotate an already-created canonical audit record in the same
transaction. They confer no permissions. Forwarded headers are ignored; no
request body, bearer, cookie, query string or full header set is retained. User
agent is shortened and explicitly marked untrusted in the action projection.
Historical rows without captured context remain unknown.

## Explicit activation

The new optional integrations fail closed when their configuration is absent.
Configuration does not claim successful external authentication or delivery.

| Configuration | Required values |
| --- | --- |
| Account providers | `--account-provider-config` absolute regular JSON file, version 1; `--account-provider-key-env` names a server environment variable containing a base64 32-byte encryption key |
| Steam linking | Provider kind/name `steam`, explicit HTTPS callback URL; fixed Steam OpenID endpoint |
| Steam presence | `--steam-presence-key-env` with a real Steam Web API key and configured Steam linking |
| Last.fm linking and presence | Provider kind/name `lastfm`, callback URL, `api_key_env`, `api_secret_env`; fixed Last.fm API endpoint |
| OAuth userinfo linking | `oauth-userinfo` with explicit authorization/token/userinfo HTTPS URLs, client ID, `client_secret_env`, callback URL and scopes; PKCE required |
| FCM devices | `--push-fcm-project`, `--push-fcm-service-account-env`, `--push-device-key-env`; actual service-account credentials and a base64 32-byte device encryption key |
| SourcePoints | `--enable-progression-source-points`, treasury account/wallet UUIDs, canonical currency and explicit per-grant/day budgets |
| Check-in perks | `--check-in-backdate-days`, `--check-in-perk-products` naming canonical paid subscription products |
| Birthday | `--birthday-experience`; zero disables the policy |
| Check-in CAPTCHA | `--require-check-in-captcha` plus the existing configured verification provider; missing configuration rejects startup |
| Night-owl maintenance | `--enable-night-owl-reminders`; existing notification preferences and sender configuration remain effective |
| Realm tenure | `--enable-realm-members`, `--realm-member-config` with version, level thresholds and `enable_daily_tenure` |

Example provider configuration contains only server environment variable names,
never secret values:

```json
{
  "version": 1,
  "providers": [
    {"kind": "steam", "provider": "steam", "callback_url": "https://client.example.invalid/connections/steam"},
    {"kind": "lastfm", "provider": "lastfm", "callback_url": "https://client.example.invalid/connections/lastfm", "api_key_env": "SOLSYNTH_LASTFM_API_KEY", "api_secret_env": "SOLSYNTH_LASTFM_API_SECRET"}
  ]
}
```

The current client can call `web/notification-device-client.js` with its actual
Firebase `getToken`/`onMessage` functions and authenticated access-token
provider. It subscribes before registration, receives the device challenge,
confirms it and exposes revoke. A client still needs its own Firebase project
SDK configuration and provider callback routing. No complete client app or
real external credentials were supplied for live activation.
The same bridge exposes `observeNotificationWakeups`, with durable IndexedDB
delivery receipts shared across tabs and restarts. It reads the current owner
from the client authentication state and refreshes the authenticated inbox;
provider payloads never select an account or supply display contents.

Provider adapters follow the primary protocol documentation:
Steam OpenID (`https://steamcommunity.com/dev/`), OAuth PKCE (RFC 7636),
Last.fm web authentication and `user.getRecentTracks`
(`https://www.last.fm/api/webauth`,
`https://www.last.fm/api/show/user.getRecentTracks`), and FCM HTTP v1
(`https://firebase.google.com/docs/cloud-messaging/send/v1-api`). Tests use
controlled transports and verify fixed endpoint, signatures, identity and
provider-error handling; they do not claim a public-provider live connection.

## Database installation and verification

Schema installation remains an explicit CI/operator operation, not a startup
migration. In addition to the existing modified owned schemas, the disposable
CI database installation must include these new files:

- `internal/connectionpg/schema.sql` after account history and authority.
- `internal/accounthistorypg/request_context_schema.sql` after account history.
- `internal/notificationpg/devices_schema.sql` after notification base.
- `internal/notificationpg/wellbeing_schema.sql` after notification base.

The exported Go schema bundles include these files for the existing Go
fixtures. Shell installation must also install them explicitly. Readiness
probes deliberately fail when required owned tables or columns are missing.

The lane adds these PostgreSQL cases in
`tests/integration/identity_completion_test.go`:

1. `TestPGVerifiedProviderLinkProofReplayAndCurrentAuthority`
2. `TestPGAccountAdminAggregateAndSecurityMutations`
3. `TestPGProviderPresenceConsentWorkerExpiryRevocationAndClaims`
4. `TestPGProgressionFundedSourcePointsConservationReplayAndRollback`
5. `TestPGProgressionSourcePointsWaitRechecksScheduledEligibility`
6. `TestPGRealmCanonicalSourceExperienceTenureAndMemberFilters`
7. `TestPGNotificationDevicePossessionEncryptionReplayAndRestart`
8. `TestPGNotificationDeviceProofExpiryDisableAndCrossOwner`
9. `TestPGProgressionBirthdayBackdateEntitlementAndSeedSync`
10. `TestPGProgressionFirstPrimaryWalletFundingAtomicity`
11. `TestPGCanonicalFriendAcceptanceTimingAndDailyAchievements`
12. `TestPGProviderPollOriginalPermissionCheckedBeforeExternalRead`
13. `TestPGAccountActionRequestContextCanonicalAtomicAndPrivate`
14. `TestPGCheckInCaptchaOriginalAuthorityAfterProvider`
15. `TestPGNightOwlReminderDurableHourDedupAndAdminProducer`
16. `TestPGAdminSteamScanSchedulesOnlyOriginalOwnerConsent`
17. `TestPGCanonicalIdentityMediaProviderAndTOTPRewardSources`

Independent-review follow-ups in
`tests/integration/identity_delivery_review_test.go` add:

18. `TestPGProviderChallengeCapsShortSessionExpiry`
19. `TestPGAccountAdminCannotReplaceOtherRootSecurityIdentity`
20. `TestPGNotificationDeviceDeliveryReceiptsPartialRestartAndUncertain`
21. `TestPGNotificationDeviceFencedDispatchAndAuthorityDuringIO`
22. `TestPGProgressionSourcePointsActualWorkerAtomicRestart`

The actual payout worker case includes insufficient funds, an injected outbox
failure after transfer, two instances, same-process repository replay,
disabled treasury and a disabled-recipient queue fairness check.
Marker, points packet, transfer and balances are checked together. Final payout
and backdate checks sample fresh PostgreSQL `clock_timestamp()` after local
waits; they do not substitute the application machine clock.

`tests/integration/identity_terminal_guards_test.go` adds:

23. `TestPGBackdatedCheckInTerminalReportWaitRechecksPaidExpiry`
24. `TestPGSourcePointsActualWorkerTerminalOutboxWaitRechecksPunishment`

Both observe real PostgreSQL relation waits. New and duplicate backdates wait
across a paid-cycle expiry before report hydration and terminal validation.
The actual payout worker waits after transfer/marker writes while publishing
its outbox, across a pre-authored scheduled punishment. The entire transfer,
marker, outbox and budget roll back; retry after expiry uses the full budget.
Removing the corresponding final guard invalidates these expectations.

`tests/integration/identity_saved_restart_test.go` adds:

25. `TestPGIdentityDeliverySavedRestartPersistence`

The runner's before phase clones its sealed empty template into an independent
database, records its OID, original public verifier/configuration and exact
source, authority, device, manifest, receipt, ledger, budget and outbox rows.
After the real database and Go-process restart, the test only opens that same
database and compares all original rows before consuming anything. No schema
installation, seed, key generation, session issue, fixture reconstruction or
repair occurs after restart. Two fresh actual workers pay the original pending
grants once and conserve treasury funds; another replay has no effects. The
actual DeviceStore and outer worker acknowledge saved accepted receipts without
a provider call, retain ambiguous receipts as terminal non-delivered, and a
second worker finds no delivery due. Original proof and device receipts stay
byte-for-byte unchanged. Same-process repository reconstruction in earlier
cases is supplementary concurrency/replay evidence, not a complete restart.

All-package ordinary tests, race tests, vet and build pass in this worktree.
The SQL parser and SQL argument checker also pass. PostgreSQL cases compile
but were not executed here: their original private disposable-CI guard is
preserved. Group registration, schema installation, immutable fixture hashes,
both restart groups and contracts must pass on the final combined SHA before
calling database validation complete.
