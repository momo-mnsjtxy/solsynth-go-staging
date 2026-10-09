# Bounded subscription maintenance quarantine

A due subscription can have an indexed `ended_at` in the past while its JSON
projection still contains a future end. The previous worker selected that oldest
row, read the future projection and returned “no work”, stopping before unrelated
valid renewals. This was observed through an inconsistent synthetic translation
fixture; that fixture was corrected separately. This independently authored
runtime follow-up addresses the general failure mode without treating corrupt
financial state as a valid renewal.

## Behavior and financial boundaries

`ProcessDue` locks the canonical account, current payment policy and subscription
in its existing order. Before renewal or expiry effects, it validates the saved
subscription's document against its indexed UUID, account, identifier, status,
payer, beginning, end and optional renewal timestamp. JSON decode failures or
projection mismatches cause a durable quarantine decision. No subscription,
order, wallet balance, hold, ledger event or payment authorization is changed by
that decision. Normal PostgreSQL microsecond encoding is accepted by truncating
both timestamp values to the same precision.

Active quarantines are excluded from later due selection. The same bounded call
can therefore continue to a valid due subscription. If a candidate changed
coherently while waiting for its account lock, the worker continues instead of
quarantining it or ending the batch. Such retries consume the call's work budget.
The result count includes committed domain transitions and quarantine decisions;
the supplied limit bounds those decisions plus stale-candidate retries, with at
most one empty-candidate query per existing order/gift/subscription lane.

Existing standing mandate, auto-renewal, original payer, amount/currency/payee,
current definition, disabled-account/payment-policy and canonical wallet
settlement checks remain in place. This change does not make a provider request,
create a mandate, grant an entitlement, bypass PIN/payment policy or settle a
quarantined row. Order and gift maintenance retain their existing behavior; this
quarantine contract is deliberately scoped to subscription projection integrity.

## Durable evidence and operator recovery

Apply `internal/commercepg/maintenance_schema.sql` after the commerce schema. It
is embedded as `commercepg.MaintenanceSchema`, included in the guarded runner and
required by backend readiness. Constructors perform no initialization or repair.
The new `commerce_v1.subscription_maintenance_quarantine` table stores one row
per subscription: a stable reason code, document SHA-256, first/last observation,
observation count and optional resolution timestamp. It stores no copied payment
body, full document, credential or profile projection. Concurrent workers record
one decision under the canonical subscription lock. Failed quarantine writes
roll back and return an error; they do not silently skip a damaged row.

The repository exposes two trusted host/operator seams, with no new HTTP route:

- `SubscriptionMaintenanceFailures(ctx, afterUUID, limit)` lists active diagnostic
  records in UUID order, with a strict 1–1000 limit and optional UUID cursor.
- `RetrySubscriptionMaintenance(ctx, subscriptionID)` takes account then
  subscription locks and validates coherence again. It refuses an unrepaired
  document. After a separate authorized repair it only marks the quarantine
  resolved, retaining the diagnostic record. It neither repairs nor charges
  anything. The next ordinary maintenance pass rechecks the standing mandate and
  all current financial conditions. Clearing an already resolved coherent row is
  idempotent.

Repair is an operator decision outside this API. No script automatically chooses
which conflicting financial representation is authoritative. A later recurrence
reactivates the same record and increments its observation count.

## Verification

The unit test covers timestamp precision and every compared identity/payer/status
or date field. Five new GuardedPG cases cover a corrupt earliest row followed by
an actual valid renewal, malformed and cross-identity documents, one-item work
budgets, concurrent quarantine, injected quarantine-write rollback, legitimate
concurrent cancellation, explicit repair/retry and restart persistence. The
fixtures use isolated synthetic wallets and the real canonical atomic payment
adapter, and preserve the global worker clock.

Local build, vet, unit/race and integration declaration/schema checks are separate
from actual PostgreSQL execution. Database assertions are compiled locally and
must pass the parent's private disposable GuardedPG CI. No local database/socket,
external provider, live transfer or production repair was performed.
