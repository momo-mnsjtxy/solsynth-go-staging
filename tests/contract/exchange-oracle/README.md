# Wallet exchange catalog reference oracle

This executable contains the catalog enumeration, decimal-prefix parser and DTO projection from the AGPL-licensed [WalletController](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Wallet/Payment/WalletController.cs) lines 536–544, 563–578, 590–619 and 627–654. The three-field error shape is independently represented from the same repository's [HTTP error documentation](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/docs/SHARED_API_ERRORS.md); no link into its excluded Shared implementation was followed.

No excluded protocol definitions, generated bindings, Shared imports/assemblies, credentials, remote service, or database are used. Microsoft.AspNetCore.App supplies only ordinary framework configuration, decimal, JSON and an in-memory MVC request pipeline. MVC is built but never started/listened on a socket. This validates the isolated anonymous catalog, not the money-changing POST endpoint or whole platform.

## Executed baseline

- Official Microsoft SDK 10.0.401, runtime and ASP.NET Core 10.0.12, Debian 13 Linux x64
- SDK downloaded through reviewed tool execution from the official [Linux x64 page](https://dotnet.microsoft.com/en-us/download/dotnet/thank-you/sdk-10.0.401-linux-x64-binaries-targz)
- SHA512 verified against Microsoft release metadata: `51c8b999af9e8dd9998c9edc5944e19a90788862068acd38694e098889054ce8c23d4f0c5cccfa16bf187d044562359e5ee69a9f8ad0bbe913ba90311fbce25b`
- 1,793 executed catalog cases: decimal96/scale28 and half-even boundaries, near-maximum overflow, prefix quirks, Unicode whitespace/UTF-16 edge cases, configuration hierarchy/JSON duplicates, comments/trailing commas, layered-provider ordering, and all 1,475 scalar case mappings emitted by this runtime
- Six actual MVC pipeline cases: GET, HEAD, POST, uppercase path, one trailing slash and double trailing slash

`cases.json` holds synthetic inputs; `expected.json` is actual reference output. Config-loading failures are separated from the controller's configuration-error response. `unicode-map.json` contains each scalar whose `Rune.ToUpperInvariant` result changes and is actually OrdinalIgnoreCase-equal; it is runtime evidence, not a copied implementation table. `mvc-methods.json` confirms [HttpGet] rejects HEAD/POST with 405, Allow GET and an empty body.

The Go contract test loads JSON through temporary files and invokes the Go HTTP handler. It compares status, response shape and numeric lexemes using `json.Number`, retaining scale and large integers rather than converting to binary64. Error comparisons require omitted optional fields, the documented code/message/status and exact documented key casing. This does not prove absent trace IDs are populated by unrelated middleware.

## Reproduce

Run `DOTNET_BIN=/path/to/dotnet bash tests/contract/exchange-oracle/verify.sh` from the workspace. No package sources are enabled; only SDK/framework reference packs are used. Build servers and diagnostic sockets are disabled. The script compares newly executed output to every recorded fixture and fails on differences. Run the ordinary Go contract suite separately. A later runtime/globalization version may change Unicode behavior and requires deliberate revalidation, not silently regenerating expected values.
