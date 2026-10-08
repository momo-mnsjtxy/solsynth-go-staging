# Wallet product catalog contract review

Date: 2026-10-08. Baseline: owned AGPL DysonNetwork revision
`4ee2d0cebb9da8284a92483fee6dce795bf6947c`. Scope is the read-only catalog
configuration/projection; no checkout, payment, authentication or provider
operation was implemented or exercised by this review.

## What the owned evidence resolves

The retained `docs/WALLET_PROVIDER_PURCHASE_APIS.md`, Git blob
`c553258f1c5d4d6aec8e20abfb0d5ca6077773dd`, explicitly documents
`GET /api/wallet-products/catalog` as public, with no authentication required.
It shows the same five top-level fields as the owned controller. This resolves
an important contract question; missing shared implementation does not mean the
whole catalog DTO or its documented access intent is unknown.

Owned `WalletProductOptions`, `WalletProductService` and `WalletProductController`
define the configuration path, known Identifier/DisplayName/Key literals, nested
decimal dictionaries and projection. Wallet startup explicitly selects
SnakeCaseLower for both properties and dictionary keys. The catalog operation
itself reads only configuration, despite broader services having other injected
dependencies. Those other service methods are not part of this bounded reference.

Exact sources, docs, license, SHA-256 values and Git blobs are retained under
`ports/wallet/productcatalog/source`. All eight entries were independently
rehashed successfully before creating this reference. No excluded definition,
Shared assembly, protocol schema or generated binding was inspected.

## Currency-default conclusion

The provider API document supplies `Currency: "golds"` in configuration, shows
`currency: "golds"` in a response, and describes depositing that currency. This
establishes an independently documented currency spelling. It does not explicitly
state the missing-configuration default of the owned class's symbolic initializer
`WalletCurrency.GoldenPoint`, whose declaration remains excluded.

The retained MULTI_WALLET document supplies no stronger mapping. WALLET_FUNDS
uses `golds` as an example, not as proof of that initializer's value. Therefore:

- Explicitly configured Currency is a fully observable bounded path.
- A `golds` fixture is lawfully grounded in owned documentation and useful for
  differential testing.
- Calling that fixture the verified canonical no-configuration default would
  overstate the evidence. An implementation must disclose its explicit default
  policy or keep this remaining default unresolved.

`tests/contract/wallet-product-oracle/OwnedProjection.cs` labels its sole
initializer substitution `FixtureDefaults.Currency = "golds"`. No excluded type
or constant definition is invented or imported.

## Executed binder and projection observations

The independent reference uses .NET SDK 10.0.401, runtime/ASP.NET Core 10.0.12 and
the actual framework configuration JSON provider, reflection binder, decimal
converter, ordinal comparers and JSON serializer. The exact source service
expression and owned nested ToDictionary projection are retained. These are
runtime-specific observations, not a claim that all framework versions agree.

### Sections and string fields

- Binding begins at `Payment:Product`; field names are case-insensitive.
  Snake-case input aliases such as `display_name` are not equivalent to
  `DisplayName` and are ignored.
- Missing/null/empty/irrelevant scalar GoldCurrency sections retain the declared
  default definition under this reference fixture.
- A string property set to JSON null or an empty object becomes null and is
  serialized as null. An empty array becomes an empty string. Empty and whitespace
  strings remain distinct. Nonempty object/array values leave its initializer.
- Numeric string-property inputs retain their JSON numeric spelling; boolean
  values become framework `True`/`False` text.
- Unknown properties are ignored by the default binder.

### Nested mappings and errors

- Null/empty/scalar ProviderMappings produces an empty dictionary under the
  initialized class. Arrays expose numeric configuration children.
- Null/empty inner provider sections produce empty inner dictionaries; nonempty
  scalar provider values are omitted. An empty array yields an empty dictionary.
- Null or complex decimal leaves yield zero. Empty, malformed, boolean or
  overflowing decimal scalars are omitted, while other mappings survive.
  These are not complete-request configuration errors.
- Valid zero and negative decimals remain in the catalog. No purchase positivity
  validation is applied by this read projection.
- Duplicate case-insensitive JSON configuration keys fail during configuration
  loading. Layered providers merge descendants; assigning a null parent does not
  delete lower-provider children. A later null numeric leaf binds as zero; an
  invalid later leaf is omitted rather than reverting to its earlier value.
- The source projection uses OrdinalIgnoreCase dictionaries. Five separately
  labelled direct-definition cases demonstrate null/case-collision exceptions
  and serialization collisions after bypassing configuration binding. They are
  projection diagnostics, not claims that JSON configuration creates those states.
- The harness records exception type and phase only. No unverified HTTP status
  or production-host error envelope is inferred from a thrown exception.

### Decimal conversion

The binder's decimal conversion differs materially from the exchange catalog's
positive numeric-prefix grammar. It accepts leading signs, decimal points,
exponents and surrounding whitespace; it does not accept thousands separators,
trailing signs, parentheses, NaN or Infinity in these mappings. Trailing decimal
scale is retained where representable. Signed zero serializes without a minus
sign while retaining scale. Rounding is midpoint-to-even at the decimal
representation boundary. Underflow can become scale-28 zero; overflow drops the
mapping. Huge negative exponents and zero with huge positive exponents were
checked without allocating correspondingly huge numbers in the reference.

The corpus includes exact number lexemes beyond binary64 range and 192
deterministic generated coefficient/scale/exponent combinations. This is bounded
coverage, not exhaustive proof over every possible numeric string.

### Ordering and snake-case collisions

Dictionary insertion follows framework configuration-child enumeration. Numeric
configuration keys are ordered using framework numeric-key comparison; other
keys use ordinal-ignore-case behavior. Layer precedence affects surviving key
spelling. Numeric extremes and sufficiently large layered dictionaries are
included in the corpus; a generic lexical map sort is not equivalent evidence.

Both dictionary levels undergo SnakeCaseLower at serialization. Distinct keys
can become the same output name: `PriceID` and `price_id` serialize as two
`price_id` members; `AppleAppStore` and `apple_app_store` serialize as two outer
`apple_app_store` members. The serializer does not merge or reject them.

The owned API document's mixed-case provider example consequently differs from
the pinned startup's executable serializer policy. The reference follows the
owned source/framework policy and records that discrepancy.

The naming policy works on UTF-16 chars: supplementary letters are not lowercased
as Unicode scalars. BMP upper/lower category boundaries affect underscore
placement. Space-separator characters act as separators; tabs/newlines are
preserved. `naming-bmp.json` records 2,967 relevant BMP classifications and
invariant-lowercase facts directly from the framework, avoiding an unverified
substitution of Go's Unicode version.

## Evidence and remaining gates

The 388-case reference and its 2,967-entry naming table were independently
executed and reproduced from a fresh build. Reproduction instructions, source
adaptation boundaries, exact hashes and versions live in
`tests/contract/wallet-product-oracle`. Response bodies remain raw strings so
nested duplicate names, order and decimal lexemes survive. A Go comparison must
use raw or ordered-token evidence, not ordinary maps or binary64 conversion.

This review owns reference evidence only; implementation/differential completion
belongs to the parent continuation. The complete production middleware pipeline,
configuration lifecycle/hot reload, actual deployment settings and unspecified
Currency initializer remain separate limits. No sockets, PostgreSQL retries,
production endpoints, purchases, excluded material, publication or canceled
retrieval were used by this review.


## Independent Go/host review, 2026-10-08

The subsequent implementation review covered product binding, decimal conversion,
ordered JSON output, the reusable Configuration snapshot/child traversal, naming,
HTTP handler, command flags/router and `verify-catalog-http.py`.

**Found and fixed:** Configuration.Children originally selected subtree prefixes
by UTF-8 byte length. Ordinal-equivalent names with different encoded widths
(`Ⱥ` and `ⱥ`) lost merged provider descendants. The implementation now compares
hierarchy segments ordinally. Independent .NET/Go checks confirm both entries
survive same-provider, reversed and layered variants. Six framework-generated
review cases were appended without changing the original 382 inputs; a focused
copy is retained in the oracle directory. The full reference therefore has
388 cases, including 383 configuration-reachable cases and five direct-definition
projection diagnostics.

No further correctness defect was found in the reviewed runtime. In particular:

- Decimal signs, scale, rounding, overflow omission and underflow match the
  bounded ordered-token corpus; duplicate output keys remain intact.
- Configuration construction copies input values, Lookup returns detached values,
  Children returns a new slice, and catalog JSON returns fresh bytes.
- New rejects absent Currency and a nonempty object/array-only Currency when no
  explicit synthetic default is selected. Explicit null/empty object represents
  a known null; an empty array represents a known empty string. These latter
  cases remain accepted consistently with the binder, without inferring a default.
- Product configuration/default flags cannot enable the route without its separate
  synthetic opt-in. The normal handler denies. Existing watch protection and
  unrelated routes remain separate; no checkout route is implemented.
- The current smoke uses explicit Currency and genuine startup snapshots, checks
  retained decimal text, and preserves the file-backed watch denial assertion.
  Its fixture does not contain collisions, while duplicate-key correctness is
  covered by ordered-token unit/reference checks. Live execution remains pending.

After the fix, all 388 .NET cases, the focused six-case subset and 2,967 naming
facts reproduced. Race-enabled tests for productcatalog, exchangecatalog and the
solsynth-go host passed, as did vet and smoke-script syntax checks. No socket,
PostgreSQL or publication action was attempted. Every .NET process invoked for
this implementation-review pass explicitly disabled ASP.NET certificate
generation. The wallet reference script sets that flag before even its version
check; frozen older references can retain their bytes and receive it from their
caller. No certificate-store access was used.
