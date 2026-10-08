# Exchange catalog: source and contract

This package implements the complete documented anonymous **GET /api/wallets/exchange catalog**, from configuration snapshots through domain parsing to the HTTP response. It does not implement POST currency exchange, wallet operations, authentication or any other Wallet endpoint.

## Licensed application sources

Repository: Solsynth/DysonNetwork, commit `4ee2d0cebb9da8284a92483fee6dce795bf6947c`, root AGPL-3.0 license. Exact source snapshots and license are retained under upstream/ and checked against Git blob hashes:

| Source | Git blob SHA-1 | Bytes |
|---|---|---:|
| DysonNetwork.Wallet/Payment/WalletController.cs | 10650f1a12912532d70f6ea422a69557a508a224 | 61958 |
| docs/SHARED_API_ERRORS.md | 129e74f6870ac375b3f800e273d0c5e1aa9f1c5f | 5254 |
| LICENSE.txt | be3f7b28e564e7dd05eaf59d64adba1a4065ac0e | 34523 |

Relevant controller regions: WalletExchangeOption at lines 536–544; configuration enumeration at 563–578; amount-prefix parsing at 590–619; anonymous GET at 627–654. The repository's Startup/ServiceCollectionExtensions.cs supplies snake_case success JSON. The checked-in appsettings.json has no Payment:CurrencyExchange section; its default catalog is empty. No real deployment configuration was read.

The error contract comes from the licensed own-tree SHARED_API_ERRORS.md, not from its linked Shared implementation. It defines code/message/status and optional detail/traceId/errors/meta; null fields are omitted and traceId is intentionally camelCase. The catalog's error response sets the source-defined code, message and status; optional diagnostics are omitted. No excluded NeTo/Spec/Shared schema, generated binding, binary or implementation is used. Original licensed caller snapshots may mention dependency namespaces, but those types are not imported into the Go build.

## Framework behavior and licenses

.NET runtime v10.0.0 primary-source references (MIT licensed) used to resolve framework behavior:

- https://github.com/dotnet/runtime/blob/v10.0.0/src/libraries/Microsoft.Extensions.Configuration/src/ConfigurationProvider.cs
- https://github.com/dotnet/runtime/blob/v10.0.0/src/libraries/Microsoft.Extensions.Configuration/src/ConfigurationKeyComparer.cs
- https://github.com/dotnet/runtime/blob/v10.0.0/src/libraries/Microsoft.Extensions.Configuration/src/InternalConfigurationRootExtensions.cs
- https://github.com/dotnet/runtime/blob/v10.0.0/src/libraries/Microsoft.Extensions.Configuration.Json/src/JsonConfigurationFileParser.cs
- https://github.com/dotnet/runtime/blob/v10.0.0/src/libraries/System.Private.CoreLib/src/System/Number.Parsing.cs
- https://github.com/dotnet/runtime/blob/v10.0.0/src/libraries/System.Private.CoreLib/src/System/Globalization/OrdinalCasing.Icu.cs
- https://github.com/dotnet/runtime/blob/v10.0.0/src/native/libs/System.Globalization.Native/pal_common.c
- https://github.com/dotnet/runtime/blob/v10.0.0/src/libraries/System.Private.CoreLib/src/System/Collections/Generic/ArraySortHelper.cs

sort.go is a Go translation of the MIT-licensed .NET introsort and retains its notice plus upstream/LICENSE-MIT.txt. Other application-derived files retain AGPL notices. The runtime sort is needed because GetChildren sorts provider keys before case-insensitive deduplication; replacing it with a different unstable sort can change surviving key spelling in layered providers.

## Implemented behavior

- Provider entries use full ':'-delimited keys, case-insensitive identity, and last-provider value resolution. Provider inputs are copied into immutable catalog snapshots
- LoadJSON reads a UTF-8 appsettings-style JSON object, flattens ordered paths, accepts comments/trailing commas, distinguishes null/empty objects from empty arrays, and rejects duplicate scalar paths case-insensitively
- The source method skips malformed source keys, but any malformed/nonpositive target or same-currency pair fails the complete request with HTTP 500 and WALLET_EXCHANGE_CONFIGURATION_INVALID; no partial catalog escapes
- Numeric prefixes use positive .NET decimal rules. Values never pass through float64: 96-bit maximum, scale up to 28 and midpoint-to-even rounding are handled with integer arithmetic. JSON preserves exact value and retained scale, including trailing zeros
- Currency suffixes are arbitrary strings. The source prefix grammar is preserved: `1e3 USD` means amount 1 with currency `e3 USD`, not exponent 1000
- Ordinal case behavior preserves dotless i and long s; Kelvin sign is not ASCII k. Unicode-16 additions absent from Go's Unicode-15 tables are supplied explicitly and checked over every Unicode scalar against the independently executed .NET 10.0.12 oracle
- The route matches case-insensitively with an optional single trailing slash. Actual ASP.NET MVC HttpGet execution establishes HEAD/POST rejection as HTTP 405, Allow: GET, empty body. No implicit HEAD or exchange-transaction support is added
- Successful and application-error responses are JSON with content type application/json; charset=utf-8. JSON escape spelling/whitespace are not byte-for-byte identity promises; decoded fields and exact decimal lexemes are compared

## Verification and boundaries

Independent tests/contract/exchange-oracle fixtures execute only the licensed controller's catalog algorithm and ordinary .NET framework APIs; they do not use excluded Shared assemblies. Independent Go tests compare configuration-file/provider → domain → real HTTP-handler results to those fixtures, preserving decimal numeric lexemes. Framework MVC route/method observations are separate from the domain extraction. The package also has focused unit tests, race checks, and all-scalar casing comparison.

The configuration lifecycle is an explicit startup snapshot. No hot-reload, environment-provider ingestion, full original host lifecycle or existing-data migration is claimed. The CLI can layer multiple JSON snapshots and rebuild deliberately; this package does not watch files. LoadJSON's file interface is explicitly UTF-8, not a promise to reproduce StreamReader's legacy UTF-16/UTF-32 BOM auto-detection. Unicode behavior is pinned to the independently tested .NET runtime corpus; it is not a claim that every historical ICU/NLS/globalization-invariant deployment has identical tables.

No live production request, payment, balance change, external account, authentication adapter, database connection or remote source write is involved in this endpoint.
