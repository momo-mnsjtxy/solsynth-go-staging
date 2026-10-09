# Wallet product catalog: documented configuration candidate

This AGPL-derived package implements the configuration/projection behavior behind
`GET /api/wallet-products/catalog`. It never creates checkout sessions, deposits,
orders, credentials, notifications or external provider requests. The loopback host
requires `--synthetic-wallet-product-catalog` and ordinary constructors remain
policy-disabled. This is not a production Wallet replacement.

## Lawful contract sources

The exact sources in `source/manifest.json` are pinned to owned DysonNetwork
revision `4ee2d0cebb9da8284a92483fee6dce795bf6947c` with its applicable AGPL license:

- `WalletProductController.GetCatalog`: local five-field DTO and nested dictionary
  copies using ordinal-ignore-case comparers.
- `WalletProductService.GetGoldsResupplyPackDefinition`: binds `Payment:Product`,
  selects GoldCurrency and uses the locally declared definition defaults.
- `WalletProductOptions`: identifier `wallet.golds_resupply_pack`, display name
  `Golds Resupply Pack`, nested decimal dictionaries and symbolic currency default.
- Wallet serializer startup: snake_case property AND dictionary-key policies.
- `WALLET_PROVIDER_PURCHASE_APIS.md`: independently authored public/no-auth catalog
  documentation, complete response example, configured `golds` currency, and
  provider mapping semantics. Additional wallet documents supply related context.

These are owned licensed callers/docs, not copied excluded model/protocol source.
The constant behind `WalletCurrency.GoldenPoint` was NOT read or assumed licensed.
The documentation shows `golds` in configured examples and deposit descriptions;
it does not prove the literal used when configuration omits Currency. `New` thus
requires an explicit Currency setting (including explicit null, which the binder
preserves). `NewWithSyntheticDefaultCurrency` accepts a clearly caller-selected
fixture default. The command's optional `--synthetic-wallet-product-default-currency`
is similarly labelled, rather than silently supplying a production constant.

The docs display PascalCase provider references, but actual owned startup applies
SnakeCaseLower to nested dictionaries. Executable source/framework behavior is
preserved and this documentation/example discrepancy is explicit.

## Configuration and serialization

JSON provider parsing and ordinal/configuration-key ordering reuse the separately
validated exchange implementation. The reusable immutable Configuration view
merges provider values while preserving lower-provider descendants, and matches
hierarchy segments ordinally instead of using incorrect UTF-8 prefix byte counts.
Inputs and returned values are copied. This is a startup snapshot, not ASP.NET's
complete environment-provider, host or hot-reload lifecycle.

Binding preserves absent versus null/empty/complex string settings. Nested
mapping values use signed .NET-compatible decimals, zero, exponent syntax,
96-bit/scale-28 bounds and midpoint-to-even rounding without float64. Invalid
scalar decimal leaves are omitted, while null/complex leaves become zero, as
observed in the independent framework reference. This endpoint has no purchase
validation predicate, so nonpositive mapping values are not silently removed.

Nested dictionary order and duplicate JSON names AFTER snake-case conversion
are preserved with an ordered serializer. A Go map would silently lose this
behavior. Clients may themselves handle duplicate JSON keys differently; no
consumer behavior or stricter production config validation is inferred here.

The naming state machine is translated from MIT-licensed dotnet/runtime
`JsonSeparatorNamingPolicy.cs` at v10.0.0 (exact blob recorded). Its license is
retained. BMP letter/digit/space categories and lowercase mappings come from
2,967 independently executed .NET framework character observations, embedded as
`naming-bmp.json` and checked against the retained oracle. Supplementary scalar
letters remain unchanged, matching the source's UTF-16-char behavior. No excluded
schema/generated binding supplied this table. Other historical framework or
ICU/NLS versions are not claimed equivalent.

## Independent evidence

`tests/contract/wallet-product-oracle/` contains a fresh executed .NET framework
binder/projection reference, raw ordered response bodies, reproduction script,
exact hashes and versions. Five direct-definition injection diagnostics are
separate from reachable configuration inputs. Go compares every reachable config
case via ordered JSON tokens, preserving duplicates, nulls and decimal lexemes.
The independently selected `golds` reference default is explicitly NOT proof of
the excluded symbolic initializer. The source integrity gate freezes the oracle
and verifies the embedded naming data; it does not regenerate expected values.

## Runtime boundary

`--wallet-product-config` accepts repeatable local JSON snapshots, low to high
precedence. Supplying these options without explicit synthetic opt-in is rejected.
Missing unresolved currency prevents startup rather than producing a guessed
catalog. The enabled handler rejects identity-bearing requests and implements
only the GET catalog route; payment routes remain unimplemented. Source docs
state the catalog is public, but complete global middleware, gateway and current
production HTTP behavior remain unverified. Local error envelopes are candidate
errors, not claimed upstream middleware parity. No database is needed for this
operation; actual compiled-process smoke remains part of the pending private CI.
