# Badge discovery MVC reference

The owned AGPL `BadgesDiscoveryController.cs` at DysonNetwork revision `4ee2d0cebb9da8284a92483fee6dce795bf6947c` is executed in a framework-only ASP.NET Core MVC pipeline. Its excluded Shared import is removed. The replacement `ApiError` DTO is written only from the owned `docs/SHARED_API_ERRORS.md` HTTP contract, including optional-field null omission and the camel-case `traceId` override. No excluded source, protocol schema, generated binding or assembly is used.

The .NET SDK/runtime baseline is 10.0.401 / 10.0.12, checksum-verified as documented in the neighboring exchange oracle. The MVC application is built and invoked directly with in-memory HTTP contexts; it never starts a network listener. Temporary SVG bytes are original synthetic test data, not any upstream icon artwork. All temporary directories are removed when execution finishes.

Thirty-four actual framework cases verify:

- Manifest version, all 23 ordered records and explicit nullable `icon_url`/`series`
- Unconfigured/whitespace/missing/file paths versus existing empty or populated directories
- Public cache headers on success and icon-not-found errors, content types, exact raw SVG bytes and file content length
- Documented error code/status/message, camel-case `traceId`, and omission of null optional fields
- GET, HEAD/POST rejection, case-insensitive route segments and single trailing slash, case-sensitive Linux filenames
- Range and conditional headers with the source's disabled range processing and absent ETag/Last-Modified metadata

The Go differential test compares all returned headers, JSON structure (including nulls), and raw file bytes. Safe-root containment in the Go implementation is an intentional hardening difference; this harness does not attempt to reproduce filesystem-boundary bypasses. It does not assert byte-for-byte artwork parity for replacement assets, protected endpoint behavior, whole-platform parity, or unrelated hosting middleware.

Run `DOTNET_BIN=/path/to/dotnet bash tests/contract/badges-oracle/verify.sh` to rebuild and execute the reference and compare recorded results. No external NuGet sources are enabled. Run `go test ./tests/contract -run Badges` with the workspace toolchain separately to check the Go handler. Build outputs are kept outside the source tree.
