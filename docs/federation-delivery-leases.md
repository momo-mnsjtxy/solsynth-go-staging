# Federation outbound delivery observation and leases

This independently authored continuation starts from reviewed commit
`89b9ad8c187c57b12f27e8d41f67b136257cd344`. It changes only the existing explicit
ActivityPub POST queue. It does not activate sending, provision keys, discover
new peers, or claim whole-platform or production readiness.

## Observation, commit and send

1. A short SQL claim commits an unpredictable owner token, incremented attempt
   number and database-clock lease. Only the oldest unfinished publisher/peer
   job is eligible. Claims use `FOR UPDATE SKIP LOCKED`. Reclamation retains the
   same payload and Activity ID, creates a new token and consumes another attempt.
2. A read-committed observation locks the original requester/owner accounts in
   order, then the current publisher/domain rows and delivery. It evaluates
   canonical session ancestry and policy, membership, owner, enabled identity,
   blocks, post revision/audit/publication, relationship or quote state, original
   key fingerprints and admitted remote incarnation/reservations. It checks
   current authority again after delivery/signing/remote-key waits. It commits
   `dispatch_started_at` only for the same token, attempt and live lease.
   When the optional reservation ledger exists, its table lock is acquired
   before any admitted-key row lock, including quote validation. This avoids
   a lock-upgrade cycle with concurrent key revocation; both revocation-first
   and observation-first quote interleavings have required PostgreSQL coverage.
3. That transaction is closed before the single bounded `Transport.Send` call.
   There are no SQL account, publisher, post, quote, reservation or delivery locks
   held by the dispatch worker across network I/O. No lease is renewed mid-send.
4. A separate transaction repeats the current checks and conditionally records
   the result for the same unexpired owner token and attempt. Superseded,
   revoked, cancelled, expired or terminal workers cannot acknowledge another
   worker's attempt. If the network context expires, its result is uncertain;
   the committed lease remains recoverable rather than being marked sent.

The observation commit is the authorization observation boundary. A revocation
that wins before it is observed prevents dispatch. A revocation after that
boundary can commit immediately, including while transport is blocked. It may
prevent ledger success without preventing bytes from reaching the peer. There
is an unavoidable interval between commit and the external send; this is not a
transactional external effect, recall, or exactly-once guarantee. An expired
worker can also have transmitted before a replacement worker retries. Per-peer
SQL ordering cannot impose external ordering on such delayed, uncertain sends.
The transport must honor its context; fencing still protects the ledger if a
misbehaving or delayed adapter returns after it loses ownership.

The ActivityPub Activity ID and exact JSON payload remain stable across retries
and process restart, allowing peers which deduplicate Activity IDs to do so.
No unsupported idempotency header or peer guarantee is invented. Existing
RSA-SHA256 request-target, host, date, digest and content-type signing remains.
HTTPS allowlists, all-address SSRF validation, numeric-address dialing, TLS,
response bounds, no ambient proxies and zero redirects remain unchanged.

## Current authority and immutable intent

An authenticated settings-enable operation records an origin intent using its
original verified proof. Explicit relationship/quote-revoke operations capture
their own original requester/session. Automatic inbox responses and canonical
post fanout refer to the settings-enable intent. It is not a credential, a
permission grant, or sufficient dispatch authority. Each enqueue and dispatch
must re-evaluate the recorded ordinary session, ancestry, scopes, current
canonical policy, original owner and active manager membership. A missing
same-pool recorded-session guard fails closed. Delegated sessions cannot enter
this ordinary-session lane.

Independent monotonic history fences record account/policy/session/membership
and publisher changes, and per-peer relationship/block changes. Revocation and
restoration cannot silently revive an earlier queued intent. The original
session/policy boundary, capped at 24 hours, is retained; session refresh or
configuration changes cannot extend it. A new authenticated settings operation
can establish new automatic-work intent, but cannot rewrite old jobs. This is a
conservative, bounded continuation: unattended federation stops when the
original enabling intent expires or its authority history changes.

Original local key fingerprint, remote actor/inbox/key fingerprint/incarnation,
peer revision, payload, post revision/audit, budget and deadline are immutable.
Admitted trust reservations remain authoritative even if admission is disabled
or a static pin is configured. Static-pin providers expose no historical
rotation generation; changing a key away and back entirely between observations
is outside the observed-current-binding guarantee. Source-owned immutable
configured providers do not perform network discovery.

Repository-owned remote-key reads inside a SQL transaction use that same
transaction for the optional-ledger probe, durable key/policy/incarnation lookup
and permanent collision check. This includes delivery observation, quote checks
and withdrawal, inbox freshness checks, post fanout and discovery rechecks.
Ordinary KeyProvider callers still use the pool. No stale prefetch replaces a
fresh READ COMMITTED check, no extra pool connection is needed for these reads,
and the reservation table-before-key-row fence remains unchanged. This removes
pool self-starvation even at one connection; it does not promise that unrelated
locks, external provider implementations or overloaded callers cannot wait.

Successful 2xx responses become `sent` only under current final authority.
408, 429, server failures and transport errors use bounded exponential backoff.
Other 3xx/4xx responses and exhausted attempts become `dead`. Maximum attempts
are captured per job (1–12); increasing configuration cannot expand an old job,
and decreasing it tightens the cap. Each claim cleans at most 100 expired or
legacy jobs. Exhausted final leases become terminal. There is no automatic
terminal reset or resend. Local quote revocation still succeeds when remote
trust or the local signer is unavailable; such a notification is suppressed
instead of rebinding old key authority or rolling back local withdrawal.

## Migration and validation

Apply `internal/federationpg/delivery_schema.sql` atomically after the base,
identity and quote federation migrations and canonical authority/account/post
schemas. It is included in `federationpg.Schema` for disposable fixtures. It has
no dependency on the optional discovery/admission migrations. Existing
publishers/jobs receive no invented authority provenance. Legacy queued jobs
without origin intent are cancelled by bounded maintenance. Readiness requires
the new columns and all thirteen history/immutability triggers. Constructors
and readiness do not install schemas, generate credentials or send anything.
Mixed legacy writers are unsupported. Rolling back to a worker that holds locks
through POST is not safe while the new worker runs; disable sending before an
operator-controlled rollback. Retain the additive ledger/history for review.

The required actual PostgreSQL cases cover concurrent claims, SQL lock release
while a synthetic send is blocked, concurrent session/settings/block and public
reservation revocation, stale/expired owner acknowledgements, immutable budgets,
policy/membership/identity/relationship ABA, authority after account/lease waits,
original deadlines, uncertain network contexts and composed backend execution.
The pool regressions additionally require one-connection static/admitted Follow
and quote dispatch, before/during key and policy revocation, transaction-local
inbox/quote/fanout/discovery lookups, and two workers with the shared pool fully
occupied by SQL-observed transactions. Pool limits are fixture-local and restored
before cleanup. Every transport/fetcher in these cases is an in-memory adapter.
The saved before/after restart fixture preserves the original one-hour synthetic
bearer, public authority verifier, session, publisher, job, signed request and
public federation key. No private signing material is saved. After restart it
verifies the same rows, authenticates the original bearer using elapsed wall
time, checks signing-time signature math separately from current-clock admission
and explicit six-minute expiration, then reclaims the original job. The private
operator signer is unavailable after restart, and the test requires terminal
failure with zero sends. Successful resumed sends with already-provisioned keys
are covered separately by same-process repository/lease-recovery tests; this is
not evidence that a fresh process can load operator keys. No credential or target
resource is reissued after restart. Markers are exclusive private 0600 disposable
files, never CI artifacts.

All new transports are local synthetic adapters. No provider, network, production
or outreach action was performed for this work. Local Go tests that skip
PostgreSQL are not database validation. Real PostgreSQL 16 and process-restart
execution remain required in coordinator CI. Inherited integration grouping is
still draft/fail-closed and must be reconciled with these new required cases and
the coordinator-owned public-only verifier helper. Exact checks and patch/file
hashes are delivered separately.

The lawful source contracts remain those recorded in
`ports/federation/PROVENANCE.md`, including retained licensed delivery/renderer
and quote evidence. This lease/authority design is owned hardening, not a claim
that the upstream code had these semantics. No excluded protocol definitions,
Socommon, Suki, generated bindings or unlicensed implementation were used.

Quote consent captures a separate monotonic block/security revision. Follow lifecycle changes still fence Follow/post jobs, but do not revoke independent valid quote acceptance. Quote revocation, private/deleted target, current actor/key/session/owner checks and block/unblock history remain mandatory. Legacy quote jobs without a captured security revision fail closed; no historical authorization is inferred during migration.
