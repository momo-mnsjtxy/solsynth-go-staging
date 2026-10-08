# Develop app discovery compatibility slice

This is source-derived AGPL-3.0-only work, not a clean-room implementation or a
production-compatible Develop replacement. The source baseline is
`Solsynth/DysonNetwork@4ee2d0cebb9da8284a92483fee6dce795bf6947c`.
The retained repository license is [`../../LICENSE.txt`](../../LICENSE.txt).
The repository-level evidence and its independent-submodule limitation are
recorded in [`../../docs/remaining-services-inventory.json`](../../docs/remaining-services-inventory.json).

The implementation is grounded in separately licensed DysonNetwork-owned source
files retained under `source/`. An authored Solian model is retained only to
document the unresolved numeric status mapping, with its separate license. References to shared types inside those callers and the
Develop-owned EF snapshot establish only their observed usage and SQL storage
shape. They do not authorize copying the excluded NeTo/Spec protocol repository,
its schemas, descriptor exports, generated bindings or shared implementations.
No such definitions were read or reproduced for this slice.

## Established local contract

`Identity/CustomAppPublicController.cs` declares `GET /api/apps` with defaults
`take=20`, `offset=0`, and nullable `search`. Its discovery method calls
`CustomAppService.GetActiveAppsForDiscoveryAsync`, returns an array, and appends
`X-Total`. The record has exactly six fields:

| JSON field | Observed source type / meaning |
| --- | --- |
| `id` | UUID app ID |
| `slug` | Required string |
| `title` | Required string, projected from app `Name` |
| `description` | Nullable string |
| `products_count` | Integer count of related app products |
| `widgets_count` | Integer count of related board widgets |

`Startup/ServiceCollectionExtensions.cs` selects snake_case JSON property names.
The source record does not expose OAuth configuration, secrets, developer data,
images, links or other app entity fields. The discovery method has no direct
remote publisher/account hydration: its results are projected from the local
database. This narrow fact does not establish the behavior of shared middleware.

`CustomAppService.cs` lines 202–239 filter by the symbolic
`CustomAppStatus.Production`; optional non-whitespace search is trimmed, wrapped
in `%`, and passed to PostgreSQL `ILIKE` for slug, name or non-null description.
User `%` and `_` retain SQL pattern semantics; the source does not turn search
into a literal substring. Count is obtained before pagination. Results sort by
name, then slug, skip offset and take the requested count. Related product and
widget counts have no explicit enabled/available predicate in this method.
Shared global filters can still affect all three entity queries.

The Develop-owned `Migrations/AppDatabaseModelSnapshot.cs` establishes
`custom_apps`, `app_products`, `board_widgets`, their relevant columns and
`deleted_at` fields. It stores app status as an integer but gives no lawful
numeric mapping for the `Production` symbol. No status value is guessed. The
retained Solian-authored `lib/developers/models/custom_app.dart` at revision
`ffd0838e5f04cb40bea311a39118ebeedad210c1` carries integer `status` with default
zero, but does not establish that mapping: field storage/defaults are not enum
semantics. Its license is retained in `source/SOLIAN-LICENSE.txt`; neither its
generated parts nor imported SDK definitions were inspected or copied.

## Candidate policy and deliberate differences

The synthetic compatibility slice executes real read-only
PostgreSQL count and page queries and serve the six-field projection through a
loopback development HTTP command. Both the ordinary handler and repository
constructors deny access pending independently resolved production policy.
Testing requires explicit compatibility constructors, a caller-supplied
synthetic production-status integer and explicit loopback command opt-in. The
synthetic integer is a fixture policy, never a claim about upstream enum values.

Its selected global-filter candidate is strictly `deleted_at IS NULL` on apps,
products and widgets. `AppDatabase.cs` calls `ApplySoftDeleteFilters`, but the
implementation and all resulting behavior remain unverified. A matching column
name does not prove complete global-filter parity.

The candidate accepts `take` from 0 through 100 and nonnegative 32-bit offsets.
These are documented local bounds. `AddPaginationValidationFilter` is registered
upstream, but its allowed ranges, binding and error envelopes are not established
from the eligible sources. The miniapp controller's separate clamp to 1–100 is
not evidence for the custom-app controller's pagination behavior.

The adapter uses a read-only repeatable-read transaction for count and page.
This produces one coherent database snapshot. Upstream executes two awaited
queries without an explicit encompassing transaction in the retained method;
the stronger consistency is intentional and is not exact concurrent parity.
Ordering and ILIKE behavior remain dependent on the actual PostgreSQL collation.
No additional ID tie-breaker is inferred where source ordering keys tie.

The HTTP candidate is GET-only (no implicit ASP.NET HEAD), accepts only one value
for each recognized query key, and parses parameter names case-sensitively.
Unknown parameters are ignored. Empty numeric values and malformed query escapes
are rejected. Local 400/403/404/405/503 error envelopes are not source ApiError
parity. Authorization, Cookie and X-Test-Account header presence is rejected;
no header or decoded JWT grants identity or access. The enabled candidate exposes
only the local synthetic discovery policy, not verified anonymous production
access. The default handler returns 503 before querying storage.

## Unresolved boundaries

`ApplicationConfiguration.cs` registers authentication, authorization and
`RemotePermissionMiddleware` before controllers; `Program.cs` adds shared auth.
A public controller name and absence of method-level authorization attributes do
not establish anonymous access or permission semantics. This slice supplies no
production identity, claims, token decoding, permission or remote-auth adapter.

Other public operations remain unported. App details load the developer's
publisher; board discovery hydrates publisher names and uses unresolved shared
scope/manifest behavior; miniapp discovery/details hydrate developer publishers;
bot information hydrates accounts and developer publishers. Their remote
contracts cannot be fabricated from excluded protocol bindings. Some other
public methods, such as custom-app products or bot chat/commands, do not show
direct RPC hydration in their controller bodies; they still require their own
service, model, filter and transport evidence rather than being covered by this
six-field endpoint.

This is not a database migration, full schema reconstruction, deployment or
proof of compatibility with existing clients. Actual PostgreSQL tests must run
in a disposable database and be reported distinctly from skipped test cases.
Synthetic HTTP/repository tests are not a .NET differential oracle. The final
validation record, including non-skipped database execution, belongs in the
repository verification notes; source inspection alone proves no runtime result.

## Retained source integrity

Hashes below identify the exact DysonNetwork implementation evidence reviewed for this slice.
`source/manifest.json` additionally records blob IDs, the Solian model and both
licenses; all 13 manifest entries were independently verified against SHA-256. All filenames
are relative to `source/`; their upstream paths replace `__` with `/`.

| File | SHA-256 |
| --- | --- |
| `DysonNetwork.Develop__AppDatabase.cs` | `33763d2ceffb40401d5e467611f20282bc985ebe19641c1c181c3752ffae4ee7` |
| `DysonNetwork.Develop__Identity__BoardPublicController.cs` | `bac6ba46e90db498c76b3d38ea3f655f896f33bd575bdf21c07be011a696d695` |
| `DysonNetwork.Develop__Identity__BotAccountPublicController.cs` | `1176c4758f09c92abbbf7233df835d5089f4667e2388dd2a4441afa1027dec06` |
| `DysonNetwork.Develop__Identity__CustomAppPublicController.cs` | `a19a0143e3d7f854178da56b913c82f9f5135525ccdcc9bcaed4af6ff676a96c` |
| `DysonNetwork.Develop__Identity__CustomAppService.cs` | `025883b599df33148927b5bc740802179d92584727aabf65a750f81d526f8bb0` |
| `DysonNetwork.Develop__Migrations__AppDatabaseModelSnapshot.cs` | `dab8948a3da3026491fa4fdc9c9204fdd850ee174051c9c56f61d604d99c2f2a` |
| `DysonNetwork.Develop__MiniApp__MiniAppPublicController.cs` | `a1c12172fa27bf9040b4ffcf9a8140724c732d9175e9f3e5fc5f259aea231abe` |
| `DysonNetwork.Develop__Program.cs` | `e7d5349148aa0bd028ca85b9cfb898633d4525857a55f2abcd17c46632861268` |
| `DysonNetwork.Develop__Startup__ApplicationConfiguration.cs` | `aa683eb1b6ae067ef6ef5d286b0f8850f690172e7ded9bbb27ffad874675048e` |
| `DysonNetwork.Develop__Startup__ServiceCollectionExtensions.cs` | `11366bab721723dad14dd18a86aade6fcda8b584ec56198107952b78725589a2` |
