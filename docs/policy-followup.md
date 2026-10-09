# Licensed policy continuation

This increment adds five executable policy modules using only retained, pinned
AGPL source snapshots at DysonNetwork revision
`4ee2d0cebb9da8284a92483fee6dce795bf6947c`. No HTTP operation or production service
is counted as completed. Existing behavior was retained.

| Module | Implemented behavior | Remaining integration |
|---|---|---|
| Messager formatting | Nullable heading escaping, fences, encrypted-content bypass | Full message hydration and transport |
| Messager unread | Single-room count and user room summaries, notification modes and strict time boundary | Verified global filters, SQL adapters, numeric enum mapping |
| Messager placeholders | Creation/replacement, stream/progress updates, TTL, finalization/expiry, copy isolation | Helper parsing, IDs/audit hooks, transactions, sync persistence and events |
| Messager notification text | Source branch ordering, localized argument construction, UTF-16 truncation and mentions | Real localization, metadata conversion, packet mapping and delivery |
| Sphere discovery | Publisher/realm scores, best-three aggregate, ordered filtering and reasons | Candidate queries, visibility, account discovery, hydration and HTTP |

Each module has tests and a provenance document beside its implementation.
Inputs represent explicit, already-resolved domain snapshots, not authenticated
request data. No permissive adapters, fake successful persistence or manufactured
notification delivery are included. Missing localization returns an error where
required. Unpaired UTF-16 output cannot silently become a Go UTF-8 string.

## Verification

Local Go 1.26.8: complete formatting, build, vet, ordinary and race tests passed.
Database cases in this local run were skipped because PG_TEST_DSN was unset;
that is not a database pass. The existing mandatory private GitHub workflow runs
fifteen actual PostgreSQL cases and two post-restart assertions separately.

The source-integrity gate checks 30 retained snapshots against their existing Git
blob and SHA-256/length evidence before verification. A disposable mutated copy
was rejected. Expected source hashes were not regenerated. This proves byte
integrity against retained evidence, not licensing of any other repository.

Cross-review found and fixed nullable locale handling and the placeholder type
predicates. No .NET differential oracle or existing-client end-to-end run occurred.

## Recovery and baseline

The remote main observed when this checkout was created was
`0735ebb3f1553d3658d944705c6d32eecf2dd541`, matching the supplied recovery baseline.
Its run `37632714563` and job `112830858757` were independently checked: all fifteen
required PostgreSQL tests and both restart assertions passed without skips.
Historical local socket failures do not supersede that CI evidence.

The supplied Library recovery archive could not be downloaded in this consumer
environment. The supported materialization flow, with an explicit local target,
and one refreshed preparation/retry both returned `download failed`. No bytes
were available for hash verification or comparison; no alternate URL or access
route was attempted. Therefore the unpublished anonymous exchange catalog and
badge discovery increment is **not integrated here** and was not reimplemented.
That was the historical result for the main baseline. On 2026-10-08 the separately preserved, hash-verified local recovery snapshot was integrated in this continuation, retaining the newer main policy and Develop work. See the README evidence chronology.

The later retained owned-source documentation in `internal/auth/source-docs` independently confirms `post.subscriptions.manage`; the earlier delegated-only limitation has been resolved. Current identity validation, Redis failure/version/
epoch rules, credential precedence, authoritative permission evaluation and exact
errors remain unresolved. Protected development routes continue to deny by
default. There was no production access, original-organization write, deployment,
frontend change, excluded protocol import or cancelled-source retry.
