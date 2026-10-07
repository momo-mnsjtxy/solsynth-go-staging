# Deterministic discovery target selection

This source-derived AGPL-3.0-only addition uses only the retained, licensed
`Solsynth/DysonNetwork` revision `4ee2d0cebb9da8284a92483fee6dce795bf6947c`.
It is not clean-room work or a complete discovery API.

Source: [`TimelineService.cs`](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Timeline/TimelineService.cs),
retained as `upstream/TimelineService.cs`, SHA-256
`b59656ca21fa645779a8012bce68fa7006b9b0132b2a56248e6dc27d82768e8b`.
The repository AGPL notice is retained in `upstream/LICENSE.txt`, SHA-256
`0d96a4ff68ad6d4b6f1f30f713b18d5184912ba8dd389f86aa7710db079abcb0`.
No excluded protocol repository, generated binding, descriptor or schema was
consulted or copied. No remote service was accessed for this addition.

`CalculateDiscoveryPostScore` ports lines 1681–1703: weighted tag, category and
collection interests; nonnegative engagement; article/blog bonus; publication
or creation age and freshness. Discovery uses `RepliesCount`, not the timeline
ranking field `ThreadRepliesCount`. Future dates clamp to zero age. Negative
interest values and non-finite floating values are not silently sanitized.

`SelectDiscoveryTargets` ports the in-memory portions of publisher selection
(lines 1402–1481) and realm selection (1538–1588). Scores sum only the best three
post scores per target, then add publisher interest or realm boost. A strict
score greater than 0.2 is required. Own/subscribed/hidden publishers and
joined/hidden realms are omitted. Stable ties preserve first group occurrence.
Publisher top-eight truncation occurs before missing records and blocked/muted
accounts are removed; no backfill is invented. Realm eligibility requires the
pre-resolved public-realm map. Reasons follow lines 1724–1733: all tags before
all categories, null-name fallback to slug, nonblank distinct labels, first
three only. Returned slices do not alias caller-owned label or post slices.

Inputs are explicit domain projections. Trusted callers must resolve visible
candidate posts, database global filters, public realm membership, interests,
subscriptions, own publishers, and blocked/muted accounts. Collection IDs must
already be distinct as in `GetPostCollectionMapAsync`. IDs are canonical UUID
strings as in the existing ranking port; short IDs appear only in synthetic
tests. These functions do not validate transport data or authorize a request.
Missing publisher records and missing public realms are omitted. Decimal score
conversion remains the caller's responsibility, consistent with ranking.go.

The returned lists correspond to the private builders (up to eight results),
not the enclosing profile's separate final Take(3). This is a typed selection
result, not a fabricated upstream response. Account suggestions, public data
hydration/redaction, suppressed suggestions, persistence, caches, randomized
discovery-event choice, HTTP, identity and permission adapters remain unported.
The public candidate query's lookback/limit/moderation conditions are also
outside this pre-resolved policy boundary. No .NET differential runtime oracle
or client end-to-end verification has been run.

Validation: synthetic Go tests cover the source formula and age boundaries,
separate reply counters, top-three aggregation, reason ordering and null/empty
names, stable ties, publisher and realm exclusion sets, strict threshold,
publisher lookup ordering with no backfill, empty inputs, NaN/Infinity, realm
boost and list limits. The full Sphere package race tests and vet passed with Go 1.26.8; the three new discovery functions each reached 100% statement coverage (Sphere overall: 95.5%). Passing these
tests establishes the checked deterministic policy, not production parity.
