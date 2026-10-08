# Affiliation tracking and configured account admission

This is an owned implementation grounded in the complete retained AGPL
AffiliationSpellService, AffiliationSpellController, AffiliationPurchaseOptions,
TestService, activation documentation and both Solian affiliation screens.
The exact source hashes and licenses remain in the existing manifests. The
first-party registration caller in
`inventory/identity-client-evidence/solian/lib/auth/create_account_content.dart`
sends `affiliation_spell` alongside its existing CAPTCHA proof and credentials.
No excluded Shared enum/model, generated SDK, protocol definition or external
provider implementation was read or imported.

The original controller supplies no update operation and no invite type in its
create request. The Solian create sheet explicitly calls that operation tracking
creation; its separate purchase flow creates and pays a Wallet order. This slice
preserves those original bodies and implements create, list, detail, results and
delete. The additional non-purchase admin invite endpoint is an owned extension,
not a claim about an upstream route or enum.

## Routes and JSON

Both `/api/affiliations` and the Passport gateway prefix are supported.

- `POST /api/affiliations`: `spell`, `max_usages`, `skip_tests`. Omitted/null spell
  generates an unbiased 144-bit uppercase hexadecimal code. Omitted/null maximum
  leaves tracking unlimited. Omitted/null skip-tests defaults true, but tracking
  can never bypass tests regardless of this stored metadata. Creation requires
  current `affiliations.manage`.
- `GET /api/affiliations`: own live spells only, `order=date|usage`, `desc`, `take`
  (default 10, maximum 100), `offset`, and `X-Total`.
- `GET /api/affiliations/{id}` and `GET .../{id}/results`: owner-scoped records;
  results use date ordering and pagination. Cross-account access returns 404.
- `DELETE /api/affiliations/{id}`: own spell plus current `affiliations.manage`.
  It revokes the spell with a tombstone and retains audit/results. Revoked words
  cannot be recreated, preventing deletion/recreation from reviving old links.
- `POST /api/affiliations/{word}/results`: anonymous tracking with
  `resource_identifier`. A supplied invalid bearer fails authentication. Invite
  spells are rejected even if the resource text looks like `account:{uuid}`.
- Owned `POST /api/affiliations/registration-invites`: same create fields plus
  mandatory `expires_at`, strictly in the future and no more than 365 days away.
  `max_usages` is mandatory and positive. This requires current
  the explicit owned `affiliations.invites.issue` permission and a current canonical first-party root session. Ordinary `affiliations.manage` authorizes tracking codes only; activation never grants invite-issuance authority.
- `POST /api/affiliations/registration-invites/consume`: the original `spell`
  body. It trims the code, requires a current first-party root identity, and
  returns the existing activation requirement state. It rejects an already
  activated account without a matching prior receipt. Successful replay of the
  same receipt consumes no additional quota; a different invite for the same
  account conflicts. Revocation prevents replay. Expiry prevents new admission,
  not replay of an already earned, unrevoked receipt.
- `POST /api/affiliations/purchase`: authenticated 503
  `AFFILIATION_PURCHASE_UNCONFIGURED`, with no order or payment side effects.

The JSON type mapping is explicitly owned: 0 tracking, 1 registration invite.
It is not an observed value from the excluded Shared enum. Returned spell fields
are id, account_id, spell, type, meta (max_usages/skip_tests), created_at,
updated_at, expires_at, affected_at and usage_count. Results contain id,
spell_id, resource_identifier and created_at. The full original Shared serializer
and excluded SDK decoding are not claimed. No frontend files are changed.

Bodies are at most 128 KiB and reject duplicate/unknown fields, malformed UTF-8,
null roots and trailing documents. Codes are case-sensitive, at most 1,024
Unicode characters, without surrounding whitespace, slash, backslash, NUL or
newlines; resources are nonempty text of at most 8,192 characters and are never
fetched. Safer generated entropy, finite tracking caps, path restrictions,
mandatory finite invite admission and soft revocation are owned differences.

## Atomic admission and authority

The canonical actor and issuer account rows are locked in UUID order before
spell rows. Preflight is not authorization: the opaque principal is checked
again after those account locks and before commit. Expiry and scheduled account
restrictions use a fresh clock after waits. The issuer must remain enabled at
commit. The spell row serializes usage accounting; all writers also hold its
issuer account lock, so the usage query begins after preceding consumers commit.
Independent SQL pools therefore share one limit; there is no process mutex.
Enrollment locks the existing issuer immediately after inserting its new account,
before taking email advisory locks, preserving account-before-contact lock order.

A unique recipient admission row and the spell's usage count, affected timestamp,
audit, eligibility evaluation, activation receipt and grants share the same SQL
transaction. A failure rolls all of them back. Ordinary tracking results never
have recipient_id and cannot satisfy the activation adapter. Account ownership
is the isolation boundary; this backend has no separately modeled tenant ID and
this slice does not claim an additional tenant boundary.

Registration joins the existing account/credential/factor/challenge transaction.
The original CAPTCHA and password policies run first; a failed invitation leaves
no newly registered account, factor or challenge. A tracking code only records
tracking. An invite can bypass configured tests only when its explicit skip_tests
metadata allows it. A primary contact must still be verified through the existing
one-use first-party challenge before actual activation. Existing contact delivery
failure semantics remain: delivery happens after committed registration, so a
failed delivery is recoverable by the existing resend flow and does not refund
an already consumed admission.

Current root proof means a freshly revalidated canonical type-0 session with no
parent or client binding. It does not mean credentials were entered in the last
five minutes and adds no new request header or token format. A delegated session
may complete a permitted examination but leaves activation pending; a root
recheck is required. First-party contact proof and the participant’s own root exam submission
may complete activation in their existing transactions. An examiner’s root
session proves only the examiner’s identity: manual grading leaves a different
participant’s activation pending until that participant supplies root proof.
The completion proof binds the recipient and exact session ID and is freshly
revalidated around grant writes. Contact proof is separate: its hash, subject,
account generation, purpose and primary contact are checked against the canonical
one-use challenge before and after completion; expiry or consumption rolls back.
Both primary-contact challenge routes join the same activation transaction;
verifying a secondary contact does not satisfy the primary-contact requirement.

## Explicit canonical activation policy

The default remains no activation policy. The existing policy without a
completion policy records pending eligibility as before. Actual completion
requires explicit configuration, for example:

```
--initial-member-permissions=tests.take
--exam-activation-policy
--exam-require-verified-contact=true
--exam-required-tests=entry
--activation-policy-id=member-entry-v1
--activation-member-permissions=posts.create,posts.react,tests.take
```

`initial-member-permissions=tests.take` is an explicit onboarding prerequisite
when new accounts must read activation progress, take exams or request a root
recheck. Initial grants otherwise default to none. Completion grants are applied
only after eligibility; listing `tests.take` there alone cannot bootstrap access.

`activation-member-permissions` defaults to an empty list. The policy identifier
is required for completion. Actual completion always requires verified contact,
including when the configured tests are bypassed. Required tests still use real
canonical non-trial passed attempts; no passed attempt or group membership is
fabricated by an invitation.

The owned allowlist contains only keys already enforced by member operations:
`tests.take`, `posts.create`, `posts.update`, `posts.delete`, `posts.react`,
`posts.bookmark`, `chat.create`, `chat.update`, `chat.delete`, `tickets.create`,
`tickets.update`, `tickets.delete`, `tickets.messages.create`.
Unknown, duplicate, differently cased/whitespace-padded and wildcard entries are
rejected. Administrative keys, `accounts.manage`, affiliation administration,
permission management, wallets and payments are excluded. This list is an
explicit small member-promotion policy, not the original default/verified group
or an automatic grant of every known permission.

`owned_authority_activations` is canonical once-only state keyed by account ID.
It records activation time, the original policy identifier, the complete policy
and whether tests were bypassed, plus SHA-256 of its serialized policy input.
Configured direct grants and this receipt commit atomically under the same
account lock used by canonical policy/revocation writers. Existing deny rules,
punishments, groups, account/session generations and session scope limits remain
authoritative. This code never enables a disabled account or issues a session.
Changing the policy ID, clearing grants or replaying activation never re-adds
revoked grants or replaces the original receipt. Completed activation is a
historical enrollment transition; revoking an invite stops future admission and
pending bypass, but does not remove already activated member permissions.
Ordinary canonical permission administration remains responsible for those.

Existing `GET /api/tests/activation` and the source-documented
`GET /api/accounts/me/activation/progress` return the canonical activation state;
`POST /api/tests/activation/recheck` reevaluates eligibility. The Passport aliases
are wired. This does not retrofit an unverified full Shared account serializer
or silently restrict accounts that already have independently provisioned grants.

## Purchase boundary

The authored purchase service specifies points, product
`affiliations.registration-invite`, a purchase-period cap and one-use fulfillment.
Its options provide example/default price 100, cap 2, period 30 days. Neither that
source nor the caller supplies the canonical receiving wallet/account. The current
commerce integrations have purpose-specific sponsorship and award treasury
bindings; no general affiliate beneficiary or transactional settlement hook
exists for this product. Reusing an unrelated treasury is not implicit authority.
This slice therefore has no purchase configuration or fake fulfillment fallback.
A future explicit sales policy must bind price/currency, period/cap and beneficiary,
then use real canonical order/payment/PIN settlement and one atomic fulfillment
receipt. No balance, paid order, external payment or invite purchase is fabricated.

## Migration, validation and limits

Install `internal/authoritypg/activation_schema.sql` after canonical authority
base schema and `internal/affiliationpg/schema.sql` after authority. The runner
installs both explicitly; constructors and readiness never perform migrations.
`authoritypg.Schema` also includes its activation schema. Preserve activation and
admission receipts on rollback; downgrading the binary or disabling completion
must not delete them, because that could re-enable spent invite quota or revoked
grants. No legacy data import or mixed legacy/Go writer protocol is claimed.

Twelve required actual-PG cases cover owner isolation, anonymous tracking separation,
concurrent exact-limit consumption across pools, replay, tombstone reuse denial,
expiry, delegated-origin denial, account/spell waits, issuer revocation after a
wait, atomic rollback on grant failure, deny preservation and changed-policy replay,
real signup/CAPTCHA/contact proof and account-before-email locking, a real non-bypass exam, combined host routes and
strict before/after database-restart reads. Restart saves random identifiers and
prestate with the shared exclusive fixture marker; the after phase only reads.
Two review regressions additionally cover examiner/recipient assurance isolation,
canonical contact proof substitution, primary versus secondary contact completion,
and expiry after a contact activation wait with full rollback. The exact CI
registry includes all twelve before cases and the restart case after.

Offline unit, build, vet, race, SQL grammar/argument and source/wiring checks are
separate from actual database validation. No local PostgreSQL sockets, real
accounts, real credentials, provider deliveries or payments were used. Private
CI must execute every required case without skips before acceptance claims.
