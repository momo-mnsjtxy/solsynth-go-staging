# Independent recovery regression reference

A separate review found two bounded correctness mismatches: commas in otherwise
empty JSON containers were accepted, and Go Unicode folding accepted non-ASCII
variants of ASCII badge route literals. Both are corrected and covered here.
The original recovered oracle fixtures remain unchanged.

This reference executes .NET configuration parsing and the retained licensed
badge controller in an in-memory MVC pipeline. Ten configuration fixtures cover
malformed and legitimate trailing commas. Six route fixtures cover long-s and
Kelvin-sign spelling differences and valid ASCII casing. There are no network
listeners, production requests, authentication tests, filesystem-boundary tests,
excluded Shared assemblies, protocol bindings, or external package sources.

The project links the existing `../badges-oracle/BadgesDiscoveryController.cs`
and `ApiError.cs`. The former is the retained AGPL owned-controller adaptation;
the latter derives only from licensed own-tree HTTP error documentation. Source
revision: `4ee2d0cebb9da8284a92483fee6dce795bf6947c`. Root AGPL license applies.
Microsoft framework behavior is executed, not copied into this reference.

Executed locally on 2026-10-08 with SDK 10.0.401, Microsoft.NETCore.App 10.0.12,
Microsoft.AspNetCore.App 10.0.12, Linux x64, and Go 1.26.4. SDK acquisition
provenance is retained in `../exchange-oracle/README.md`. No dependency sources
are enabled in `NuGet.Config`.

Reproduce the reference without rewriting expected results:

```sh
DOTNET_BIN=/path/to/dotnet bash tests/contract/recovery-regression-oracle/verify.sh
go test ./tests/contract/recovery-regression-oracle
```

Results and exact fixture hashes are recorded in `validation.json`. These cases
establish only parser acceptance and literal-route status behavior. They do not
establish full production middleware, all endpoint behavior, or platform parity.
