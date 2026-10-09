# Canonical account picture and background references

This continuation builds on `b25849e17badaf9c2f5fb22c88f78b7df1635622`. It completes the retained authored profile editor's picture/background selection and replacement through `PATCH /stargate/accounts/me/profile`, composed with the existing canonical media upload and open-file routes. `/api/accounts/me/profile` remains the owned alias. No new identity, file ownership, quota, storage, or external delivery authority is introduced.

## Evidence and contract boundaries

`ports/passport/accountsettings/source/Solian_profile_update.dart`, pinned by its existing source manifest, selects an edited cloud file and sends `picture_id` or `background_id` to the profile route (lines 45–95). Its authored view reads `profile.picture` and `profile.background` (lines 242–264). The retained official `ports/media/source/DRIVE_API.md` documents the descriptive `profile_picture` usage label at line 1014. `docs/profile-media-source-evidence.json` records these sources and the limits of what they establish. Complete licensed snapshots remain in their existing locations; no additional source retrieval was performed.

The selected ID must identify a ready canonical file owned by the acting account and use a passive image MIME type: PNG, JPEG, GIF, WebP, AVIF, or BMP. A different owner's public file, a file visible through a post, or a file readable through realm membership cannot be redistributed as the actor's profile image. Remote URLs, empty IDs, unavailable files, SVG/HTML, and unknown image MIME types fail closed. The owned canonical UUID identity remains in use; this does not claim original nanoid or excluded SDK model compatibility. The implementation restricts the declared MIME type and uses the existing byte-serving behavior; it does not decode/re-encode images or claim malware/content inspection.

Omitting a slot retains its current reference. A non-null ID selects or replaces it. Explicit JSON null removes that slot, an explicitly owned removal extension not evidenced by the retained editor. Both slots may reference the same file. Removing one slot preserves access through the other. Duplicate, case-ambiguous, and non-lowercase profile keys are rejected. Mutation still requires the existing `accounts.manage` policy and the verifier's original proof.

## Canonical storage and current visibility

`owned_account_profile_media` stores only account/slot/file references. `owned_media_files` remains the source for ownership, readiness, MIME, file metadata, quota, and bytes. The `profile_picture` usage label is descriptive; setting it alone never makes a file public or attaches it to a profile. Attaching an owned private file deliberately grants the public profile audience read access without changing its canonical `is_public` flag.

Reference updates, text/name/birthday changes, and the existing profile audit share one transaction. The actor account lock comes first, followed by distinct selected file IDs in sorted order. File readiness and ownership are checked after any file wait. The original authority proof is checked again before the profile audit/commit, preventing a media wait from extending access-token validity. SQL or audit failure rolls back the profile and every reference together.

Self GET and mutation responses include current media plus the existing private birthday. Public account GET includes current media and only current opted-in verified contacts; birthday remains absent. Supplying authorization now binds the authenticated viewer to the public lookup, so an invalid credential is rejected and a block in either direction hides the profile. Anonymous public profiles remain public. There is no newly invented account-private/security-mode setting.

Canonical file reads recheck active owner status, including currently effective disable-account punishments, after acquiring the file lock. A profile grant additionally requires a current reference, current ready file, matching canonical owner, existing profile, passive MIME type, and no block for a supplied viewer. Removing/replacing the last reference withdraws only that profile grant; independent canonical public visibility, file ownership, and existing post grants retain their own rules. Source deletion immediately removes the projected image and byte access. Profile visibility is chained in front of existing post visibility, preserving post/realm reads and current membership checks. Reads begun before a later mutation can complete; this is current request authorization, not recall of already delivered bytes. Responses retain the existing private/no-store byte policy and nosniff header.

## Explicit installation and host composition

Constructors never migrate. Install `internal/profilemediapg/schema.sql` after canonical authority, account, media, and social schemas. Fresh `mediapg.Schema` recognizes `profile_picture`; apply `internal/mediapg/usage_schema.sql` as one transaction on existing databases to update the usage constraint without an unguarded interval. The private integration runner requires this atomic installation and checks dependency order.

The backend creates `profilemediapg.New(db, postMedia)`, installs it on the canonical account repository with `WithProfileMedia`, and uses the same adapter for media `WithReadVisibility`. Media readiness also validates its composed visibility adapter and the profile-reference/social tables. Standalone account hosts without this composition reject supplied media fields with the existing unsupported response. No grants, quota, provider access, production configuration, or live account changes are provisioned here.

## Verification and remaining scope

The six required PostgreSQL tests in `tests/integration/profile_media_test.go` cover:

- Composed upload, picture/background assignment, replacement, omission, explicit-null removal, shared-slot reference retention, public/private projections, contact opt-in, source deletion, canonical public-flag preservation, and actual bytes/headers.
- Ownership denial, public foreign-file denial, invalid MIME/IDs, duplicate payloads, atomic rollback after reference mutation, block enforcement, disabled accounts, and active disable punishments.
- Authority revocation while waiting, changed file state after a file lock wait, and access-token expiry while waiting for media.
- Anonymous byte reads waiting on a file lock observe newly removed references, disabled owners, and disable punishments.
- Existing realm/post visibility remains effective, membership does not become file ownership, and revoked membership loses bytes.
- Profile references, privacy projection, canonical file bytes, and reopened media service survive the required before/after database restart.

Local build, vet, ordinary tests, race tests, retained-source checks, integration wiring checks, and pinned PostgreSQL-16 syntax parsing are run by `scripts/verify.sh`. The integration package's local success compiles these tests but skips actual database execution when `PG_TEST_DSN` is absent. Real lock/rollback/restart validation remains for the parent's authorized private PostgreSQL CI. No socket retry or alternate database was attempted.

Security-mode UI remains unresolved: the retained account-settings screen invokes excluded SDK methods `getSecurityPreferences` and `updateSecurityPreferences`, and UI strings do not establish routes, request schema, authority rules, or mode effects. No security-mode endpoints, enum mapping, or inferred lockdown behavior are implemented. Other previously recorded account/SDK/provider gaps remain outside this delivery. This continuation does not claim whole-platform or upstream wire parity.
