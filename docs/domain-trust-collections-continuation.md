# Domain trust and collection administration

This is a reconstructed revision after the execution workspace disappeared. The original frozen patch had SHA-256 `18c926632f52c902efbb20f1169ad296763cadb0cdfc5cb1e86d26d17ea35d0f`; its bytes and per-file hashes did not survive. This revision has new hashes and must be validated against restored commit `5e695228fab2e3c4eb956851731e09dcad15d04a`. It does not claim byte-for-byte restoration.

## Source and deliberate policy

Pinned authored DysonNetwork controllers/services and the authored Solian caller are recorded in [the evidence manifest](domain-trust-collections-source-evidence.json). No excluded Shared definitions or generated protocol files were used.

Domain trust levels are the documented HTTP numbers: blocked 0, neutral 1, verified 2. The source service checks default HTTP, IP/private-address and port restrictions before database rules. The docs say database rules override defaults; this implementation follows the actual service ordering. Its independent private-network check remains active even when direct IPs are allowed.

Rules are evaluated by descending priority, then normalized pattern and UUID for deterministic ties. `*.example.test` matches descendants only, including multiple levels; `**.example.test` also matches the apex. A question mark is a wildcard only in a general pattern containing an asterisk, preserving the authored implementation's branch behavior. Matching uses bounded dynamic programming, not arbitrary regular expressions. Rules are ASCII lowercase patterns; IDNA names should use their normalized ASCII form.

URI parsing accepts only absolute HTTP(S), rejects userinfo, malformed authorities/ports, controls, malformed escapes, backslashes, IPv6 zones and noncanonical numeric hosts, and normalizes IDNA, IPv6 and default ports. It performs no DNS lookup or fetch. A domain trust result never grants Reader/network access and does not replace existing SSRF restrictions.

Canonical current Superuser plus the exact named permission is required for every administrative operation. Publisher or realm roles never grant global administration. This strengthens source routes where only authentication is visible and upstream global middleware is unknown.

## Runtime composition

Apply `domaintrustpg.Schema` after canonical authority/account schema. It adds four tables: `owned_domain_trust_state`, `owned_domain_trust_rules`, `owned_domain_trust_metrics`, and `owned_domain_trust_audit`. There is no new collection schema; existing canonical collections, items, publishers and interaction audit are reused.

Construct:

- `domaintrustpg.New(db, guard, domaintrustpg.Config{Policy: policy, Now: now})`, then `Ready(ctx)`.
- `collectionadminpg.New(db, guard, now)`, then `Ready(ctx)`.
- `domaintrust.NewHandler(domainRepo, canonicalPostIdentity)` for the combined API and Passport service.
- `collections.NewHandler(collectionRepo, canonicalPostIdentity)` for the combined API and Sphere service.

Each constructor returns a repository and error. Pass the exact same database pool and canonical guard used by the host's bound original proof. Both ports expose `Handles(path)`; dispatch these before broader collection or administrative handlers. There are no host, CLI, scheduler or external-provider edits in this patch.

Domain routes preserve `/api/domain-blocks` and `/passport/domain-blocks` aliases. GET root/id and GET metrics/domain require `domain.trust.validate`; POST root requires `domain.trust.create`; PATCH id requires `domain.trust.update`; DELETE id requires `domain.trust.delete`. Public POST validate accepts `{"url":"https://example.test/"}`; public GET check accepts one url query parameter. The full public result always suppresses matched-rule metadata; quick check returns only allowed, verified and block reason.

Default configuration blocks HTTP, all direct IP addresses, private/special addresses, and ports 21, 22, 23, 25, 53, 110, 143, 993, 995, 3306, 5432, 6379 and 27017. An explicitly nonnil blocked-port list replaces defaults, including an empty list. Constructor copies that list. Maximum active/nondeleted rule population defaults to 1,000; metric domains default to 10,000 and cannot exceed 100,000. Capacity errors roll back the request. Existing domains continue incrementing when cardinality is full.

The typed `domaintrust.Evaluator.Evaluate(ctx, rawURL)` reads current rules without recording metrics. `Validate` records a successful evaluation and host-only counters atomically. Raw URL paths, queries, fragments, user IDs and session information are never stored in metric rows. Invalid URLs return a fixed blocked result and no metric. SQL or capacity failures return a generic error instead of an unrecorded successful verdict.

All rule/audit/generation changes share a transaction. A durable state row serializes rule writes with validation; administrative page/count reads hold its share lock. Permissions are rechecked after waits and before commit. There is no stale server rule cache. Metrics use exact integer increments and monotonic last-check timestamps. No automatic metric expiration or purge is claimed.

Collection routes preserve `/api/admin/collections` and `/sphere/admin/collections`: GET root/id requires `posts.moderate`, PATCH id requires `post.collections.update`, DELETE id requires `post.collections.delete`. Responses contain collection/publisher metadata and item count. They do not hydrate nested posts or media. Background/icon remain absent in the owned model. Optional query is a literal text probe; publisherId is a canonical UUID. Page size is capped at 200.

PATCH supports only name and description; omitted/null values preserve current values. It cannot change publisher, owner, slug or members. DELETE tombstones the collection and removes item links; posts and files remain. Transactions plan and lock sorted accounts, then realm, publisher and collection, recheck ancestry and current global authority, and append the canonical interaction audit atomically. Ordinary privacy routes receive no global-admin bypass.

## Ingress query privacy

Keep `RequestURI` unchanged so signed routing is consistent. The backend currently adds no general access logger. Operators must redact the query for GET `/api/domain-blocks/check` and `/passport/domain-blocks/check` in ingress/access logs. New clients should prefer POST validate for full URLs where that flow is available. POST does not prevent upstream body logging; configure body logging separately. HTTP error bodies never include the supplied raw URL.

## Verification and limitations

Full Go tests, full vet, focused race tests, URI/wildcard/HTTP regressions and the 452-snapshot source checks pass on the reconstructed revision. All 1,738 baseline files compare unchanged. Ten guarded PostgreSQL cases compile and explicitly skip locally; they cover global denial/current authority, lifecycle, literal matching, concurrent counters, capacity, exact audit/metric rollback constraints, queued rule changes, restart persistence, collection metadata privacy, and delete races. The integration-owned expected-case list must add these ten cases and DomainTrustRestartPersistence to the after phase before its exact guard can pass. Actual PostgreSQL/restart execution remains in the parent's disposable CI harness.

No remote domain ownership verification, reputation provider, DNS check, URL fetching, generic regex engine, owner reassignment, media rehydration or claim of unknown legacy internals is included.

## Composed host candidate

Both families now dispatch through canonical bearer and same-pool transaction authority in the backend. The default domain policy keeps HTTP, literal IP and private-network restrictions enabled; this evaluator never performs network access. Readiness checks the domain rule/metric/audit tables and canonical collection relations. One composed HTTP case covers administrative denial, public blocked verdict with no private rule details, host-only metrics, rule deletion, and canonical collection rename/deletion. The full harness now requires 397 PostgreSQL cases and 53 restart cases, pending exact-commit private CI.
