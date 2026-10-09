# Wallet product binder/projection reference

This independently executed reference covers the owned
`Payment:Product:GoldCurrency` binder and catalog projection only. It does not
perform checkout, provider calls, money movement, authentication, database access,
HTTP listening, or production probes. No excluded Shared source, constant value,
assembly, protocol schema or generated binding is included.

## Critical default limitation

The owned options class initializes Currency from a symbol in an excluded
assembly. The retained owned API documentation independently demonstrates the
currency string `golds`, including an explicit configuration and response example,
but does **not** explicitly establish the value of that initializer when Currency
is omitted. `OwnedProjection.cs` replaces only that unavailable initializer with
`FixtureDefaults.Currency = "golds"`, which is a **documented reference fixture**,
not verification of the excluded constant or canonical no-configuration default.

Fixtures with explicit Currency values provide independent binding/projection
coverage without depending on that substitution. Other fixture results are
conditional on the declared fixture default. The known owned Identifier,
DisplayName and catalog Key literals are retained verbatim.

## Evidence and adaptation

Source baseline: `Solsynth/DysonNetwork` at
`4ee2d0cebb9da8284a92483fee6dce795bf6947c`, AGPL-3.0-only. Exact sources, owned
documentation and license are in `ports/wallet/productcatalog/source` with its
pinned manifest. `OwnedProjection.cs` extracts only:

- ProductOptions and WalletProductDefinitionOptions, with the fixture substitution
- WalletProductCatalogItem's five fields
- The two nested case-insensitive ToDictionary projections
- The owned catalog key literal

`Program.cs` executes the exact `GetSection("Payment:Product").Get<ProductOptions>()`
and `options?.GoldCurrency ?? new WalletProductDefinitionOptions()` expression.
It uses .NET's real JSON configuration provider, reflection binder, decimal
converter, ordinal comparers and System.Text.Json naming/serialization. Serializer
property and dictionary policies plus NumberHandling match the retained Wallet
startup. NodaTime setup is intentionally excluded because this projection contains
no date/time or NodaTime fields.

The owned API document's mixed-case provider-key example differs from the retained
startup's SnakeCaseLower dictionary policy. This reference follows executable
owned source plus framework behavior; it does not silently preserve that example's
casing as an API guarantee.

## Corpus and interpretation

The 388 cases include:

- Missing/null/empty/scalar/array/object sections and properties
- Explicit and missing Currency, retained null strings, ignored unknown properties
- Nested mapping binding, invalid-decimal omission and null/complex-to-zero values
- Signed decimal values, exponents, rounding, scale, underflow and overflow;
  192 deterministic generated coefficient/scale/exponent combinations
- Case-insensitive JSON configuration duplicates, layering, parent-null overrides,
  numeric-key ordering and layered dictionaries large enough to exercise sorting
- Snake-case word boundaries, Unicode BMP/supplementary behavior and collisions
- Five direct-definition diagnostics showing projection failures/collisions

Most inputs use `configs`, an ordered low-to-high-precedence list of JSON strings.
The five `direct_definition` cases intentionally bypass configuration binding to
exercise the owned projection's handling of null/case-colliding dictionaries.
They must not be described as configuration-reachable behavior, and need not be
supported by a configuration-only Go API. `providers` is a harness input for
optional in-memory experiments; the checked-in corpus uses no such cases.

Each result records `name`, `body`, `error`, and `error_phase`. Errors report only
exception type and bounded phase, not invented HTTP status/error contracts.
The source action does not catch those exceptions; the complete host's resulting
error response is not established by this reference.

`body` is a raw JSON string. In particular, SnakeCaseLower can serialize multiple
distinct dictionary entries using the SAME output property name. Keep object
member order, duplicate keys and exact decimal lexemes when comparing it. Never
parse response bodies into ordinary maps or binary64 values. Equivalent string
escape spellings can be compared with an ordered JSON token stream; a generic
object-map comparison is insufficient.

## Independent implementation-review regressions

`review-cases.json` / `review-expected.json` add six separately recorded checks
and also append those six cases to the main corpus, preserving its original
382 entries. They cover ordinal-equivalent
provider spellings with different UTF-8 widths (Ⱥ / ⱥ), reversed/layered providers,
scalar-plus-descendant binding and null-string-plus-descendant binding. This
exposes why configuration child traversal cannot slice UTF-8 prefixes by the
requested spelling's byte length. The reference executes with certificate
generation disabled, as do all reproduction-script .NET processes.

## Framework naming facts

`naming-bmp.json` freezes all 2,967 BMP characters classified by the executed
framework as Lu (1,132), Ll (1,448), Nd (370) or Zs (17), with their invariant
lowercase result. Omitted characters take the naming state machine's other branch.
`--naming-bmp` regenerates these observations using `char.GetUnicodeCategory` and
`char.ToLowerInvariant`, not Go's Unicode tables. U+0130 remains U+0130 under this
runtime's invariant lowercase. Supplementary UTF-16 surrogate code units are
unclassified; the body corpus separately covers supplementary spelling behavior.

These are independently observed framework data, not copied Shared/model source.
Microsoft .NET framework source is MIT-licensed; its retained license is available
at `ports/wallet/exchangecatalog/upstream/LICENSE-MIT.txt`. The naming state-machine
translation and its source notice, if used in the Go port, are separate from this
fact table. This corpus is pinned to the recorded runtime/globalization baseline,
not a promise about every historical .NET/ICU/NLS environment.

## Reproduction

Executed with SDK 10.0.401, Microsoft.NETCore.App 10.0.12 and
Microsoft.AspNetCore.App 10.0.12, Linux x64, 2026-10-08. Runtime acquisition evidence
is retained in the neighboring exchange oracle. No NuGet sources are enabled.

```sh
DOTNET_BIN=/path/to/dotnet bash tests/contract/wallet-product-oracle/verify.sh
```

Build output is kept outside the repository. The script reproduces and compares
results without rewriting expected fixtures. Exact source/fixture/tool versions
and hashes are in `validation.json`. This is reference evidence only; a separate
Go differential check must demonstrate any Go implementation's behavior.
