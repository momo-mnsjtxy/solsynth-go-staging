# App credential publisher-owner eligibility

This bounded review starts from `820323e86861b3762391fe72417f1e5c1c87ee83`.
It changes canonical app credential checks, app-secret issuance/rotation, and
the documented organization-app exclusion in bot/miniapp ancestry.
It does not change commerce charge/refund handling or intentionally public app
metadata.

## Current owner and ancestry

The canonical app adapter already restricts developer enrollment, scoped
management, app credentials and OAuth registration to individual publishers
(`type=0`). The publisher schema requires a nonnull direct account for that type.
Consequently its nullable-owner branch was not a supported organization bypass.
Organization developer/app ownership remains unsupported. Synthetic imported
organization ancestry must still fail closed, regardless of realm-owner health.
Bot/miniapp management and public projections previously omitted that explicit
restriction and treated a NULL direct owner as exempt. Their shared ancestry
predicates now also require `type=0`, and management rejects a missing owner.
This preserves the documented unsupported integration; it does not implement
realm-owned developer enrollment or globally change ordinary public metadata.

App credential authentication now observes the complete app/project/publisher
chain and the same direct owner after its existing ancestry locks. A changed
project, publisher or owner fails instead of following a different chain or
acquiring a late lock. Missing/deleted ancestry and active owner disable
punishments deny. The owner is checked again after the secret lock: a scheduled
punishment may activate while that lock waits, without any policy writer.
Previously the secret wait refreshed only secret expiry/revocation. The bare
AppWidgets path had no later owner check; commerce RequireClient and board
PushPayload had additional downstream authorization checks.

Secrets created or rotated by a distinct publisher editor now require an active
current owner. This is an explicit owned secure policy aligned with credential
use, not a newly recovered upstream behavior. The locked app/project/developer/
publisher/member identity is checked before issuance and after secret and audit
writes. The original actor proof and its permission are also checked again.
An error, including a failed/uncertain commit, returns no secret projection.
Authorized credential revocation remains available for cleanup while the owner
is disabled. Secret lists still return metadata only. Ordinary public app
metadata keeps its existing publication/moderation policy.

## Ordering and limits

No account, realm or other row lock was added. Authentication retains its
existing owner-before-ancestry-before-secret order. Secret management retains
actor, publisher/member, project, app, secret order. In particular, this review
does not expand a sorted account set that could invert the existing board payload
writer's recipient-then-app-owner order.

For secret issuance/rotation, the final READ COMMITTED owner observation after
all relevant waits is the eligibility boundary. A distinct owner's disable may
commit after this observation and before the secret transaction commits. This
review does not claim strict disable-before-commit serialization; any resulting
credential is unusable under the subsequent current-owner authentication check.
Clock-driven eligibility is evaluated at the final observation, not continuously.

Root/delegated boundaries remain unchanged: original private proof and actor
binding protect management writes, permissions remain session-scope bounded,
and OAuth delegated sessions only admit the four declared identity scopes.
Opaque app secrets only authenticate their explicit app and purpose scopes.

## Verification

New disposable-PostgreSQL cases:

- `TestPGAppPublisherOwnerEligibility`: healthy and changed direct owners;
  disabled/active/future/expired punishments; block-login distinction; deleted
  app/project/publisher; healthy and deleted realm imported organization denial;
  private-secret, scoped-client and OAuth-registration boundaries.
- `TestPGAppPublisherOwnerRecheckedAfterWait`: observed secret-row wait crossing
  a scheduled owner disable; publisher wait crossing owner replacement; app wait
  crossing project replacement; no returned widget projection on denial.
- `TestPGAppSecretIssuerOwnerPolicy`: a distinct editor can create/rotate for a
  healthy owner, cannot issue for disabled/punished owners, can revoke for
  cleanup, cannot substitute its actor, and does not change public metadata.
- `TestPGBotImportedOrganizationAppBoundary`: healthy direct bot and miniapp
  behavior remains usable; imported organization bot/developer/chat/miniapp
  projections and scoped mutations deny for healthy, disabled and deleted
  realm ancestry without audit effects.
- `TestPGAppSecretIssuerOwnerWaitRollback`: committed owner disable while an
  editor waits on the secret; no plaintext, secret change or audit survives;
  late audit failure also rolls back new credential issuance.

All fixtures are synthetic. No live credentials, provider calls or local sockets
were used. Focused Go/race/vet and static PostgreSQL grammar/SQL-argument checks
are recorded in the separate immutable delivery. These PostgreSQL tests are
compiled only locally; actual execution belongs to the coordinator's disposable
private CI. The delivery supplies test names separately and does not edit CI
layout/group inventory files. No local skipped result is claimed as a DB pass.
