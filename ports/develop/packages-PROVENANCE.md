# Develop miniapp package download compatibility candidate

This AGPL-3.0-only source-derived slice implements one route:
`GET /api/miniapps/{slug}/package`. It is neither a production Develop replacement
nor an assertion of full ASP.NET transport compatibility. Product catalog
entity serialization remains partially evidenced; a later documented bot-command
read candidate is described in `bot-commands-PROVENANCE.md`. See
`products-PROVENANCE.md` for the updated boundary assessment.

## Eligible evidence

The baseline is `Solsynth/DysonNetwork@4ee2d0cebb9da8284a92483fee6dce795bf6947c`.
The already-retained `source/` controller, Develop database snapshot, startup,
and license evidence are reused unchanged. Additional owned sources and the
license are retained in `package-source/`; `manifest.json` pins every Git blob
and SHA-256. `products-PROVENANCE.md` describes the separately licensed sources
reviewed while establishing the rejected product/bot alternatives.

| Additional source | Git blob | SHA-256 |
| --- | --- | --- |
| `MiniApp/MiniAppService.cs` | `29c51b9c9e3dec97bb2e2c277dee4b87127dc42a` | `936cdd41a56024ada9ef938729aa693c8091e58ed163e4479f4de6f298ddd489` |
| `MiniApp/MiniAppStorageService.cs` | `b53128728283a6b41802414270b0330f44d94d4a` | `46a596159864208af443f625f0ac1140a6a9ccd2e871d0eda39ac259fbf72edc` |
| `Models/MiniApp.cs` | `a4a059457d5b1149cd3f25a48ae8d97afee6cbc6` | `40ef23c52ab516e5434e52b4f7457d5a57b23754c53c8808a6eb10729e4bf111` |

These files are Develop-owned, including its authored `MiniAppStage` declaration
(`Development`, `Staging`, `Production`, hence production ordinal 2). This does
not establish the unrelated shared `CustomAppStatus` enum. The Go candidate
still requires an explicitly supplied synthetic stage and uses 71 in tests, so
an enum fact does not accidentally enable unresolved production policy.

No NeTo/Spec definitions, schemas, descriptors, generated bindings, or imported
SDK definitions were inspected or reproduced. References to their symbols in
licensed callers are not copied implementations of those dependencies. Package
download does not serialize the miniapp entity or shared ModelBase/Developer.

## Observed operation and failure ordering

`MiniAppPublicController.DownloadPackage`:

1. Looks up the published miniapp by case-insensitive slug. Missing app returns
   404 with assigned code `DEV_MINI_APP_NOT_FOUND`, message `Mini app not found`,
   and status 404.
2. Rejects a null/blank storage key before any increment, even when a URL exists.
   Its assigned error code is `DEV_MINI_APP_NO_PACKAGE`, message `Mini app has no
   published package`, and status 404.
3. Awaits an atomic `download_count = download_count + 1` database update.
4. If `PackageUrl` is non-whitespace, returns a redirect to that stored URL.
5. Otherwise reads the stored object fully into memory and returns its bytes as
   `application/zip`, attachment filename `{miniApp.Slug}.zip`.

The service does not wrap the lookup, counter increment, or object read in one
transaction. The Go SQL adapter therefore issues a separate atomic autocommitted
update. A subsequent storage, invalid-URL, cancellation, or transfer failure does
not roll back an already committed increment. Counter database failures prevent
success. There is no automatic retry that could double count an uncertain
update. Upstream ignores `ExecuteUpdateAsync`'s affected-row count; the adapter
also treats a zero-row update as success. Lookup checks stage/project eligibility;
the subsequent update filters by ID plus the selected soft-delete candidate,
not by stage or project. It does not change updated_at or unrelated metadata.

The SQL lookup includes required project and developer relationships, filters
`deleted_at IS NULL` on miniapps and projects, filters the caller-selected stage,
and compares lowercased slugs. The snapshot has no developer deleted_at column.
Neither deleted_at checks nor relation joins establish the complete behavior of
excluded global filters. Duplicate matching slugs have no source ordering; the
adapter adds none. .NET `ToLowerInvariant`, Go `strings.ToLower`, and PostgreSQL
`lower` can differ for Unicode/collations. ASCII case behavior is tested; full
Unicode casing parity is not claimed.

## Explicit synthetic boundary and transport differences

The normal handler, service, and SQL repository constructors all deny before
accessing their dependencies. Tests/loopback hosting must explicitly select each
synthetic constructor. Presence of Authorization, Cookie, or X-Test-Account
headers is rejected, including empty values. No fake user header, JWT decoding,
principal, role, or successful remote-auth adapter is supplied. Shared permission
middleware remains unresolved despite the controller's public name.

Successful transfer responses have real stored bytes or the stored redirect URL.
The handler never retrieves or follows that URL, and tests use `.invalid`
destinations. The two 404 responses preserve the three values explicitly assigned
in the controller. Additional shared ApiError members, converters, and full
serialization remain unverified; other errors use a documented local error
envelope and do not disclose database paths, DSNs, or object keys.

Local candidate differences:

- GET only, with no implicit HEAD behavior. Unknown query parameters are ignored;
  they are not copied into redirects. The full source middleware/binding stack
  is not reconstructed.
- Slugs must be nonempty, at most 1024 UTF-8 bytes, with no path separators, NUL,
  CR, or LF. These are local bounds rather than inferred ASP.NET validation.
- Redirects require absolute HTTP(S) URLs without userinfo or control characters;
  relative and non-HTTP forms supported by broader framework behavior are denied
  after the increment. URL text is retained without an outbound request.
- The candidate sets `Cache-Control: no-store`, emits no redirect body, and uses
  Go MIME formatting for Content-Disposition. Exact framework headers, cache,
  error envelope, and non-ASCII filename byte parity are not established.
- Object storage is a real, read-only local-file adapter under an explicitly
  opened synthetic directory. It is **not Minio/S3**. The licensed source uses
  Minio credentials, bucket and GetObject; no fake S3 success, production object
  probe, account, network fetch, or credential adapter was added.
- `os.Root` contains file/symlink resolution inside the root. Absolute paths,
  backslashes, dot segments, NUL and empty segments are rejected, and only regular
  files are read. The directory is operator-controlled synthetic fixture storage,
  never a client-writable filesystem capability.
- Reads buffer completely before HTTP success and are capped at 5 MiB plus an
  overflow check. This number comes from the source upload limit; imposing it on
  historical downloads is an explicit additional candidate limit. Stored ZIP
  bytes are not unzipped, modified, executed, or fabricated by the runtime.

`internal/developpg/testdata/packages.sql` is a disposable projection of observed
columns and required relationships, with unrelated columns omitted. It is not a
production migration or complete schema. The runtime creates no tables and owns
neither database migrations nor source accounts. The CLI requires independent
package-stage/local-directory opt-in in addition to its discovery/DSN opt-in.

## Verification status and required external gate

Unit tests cover fail-closed layers, source sequence/integrity, lookup-key-counter-
storage ordering, safe transport errors, redirect-without-storage, actual local
bytes, file size bounds, cancellation, and root/symlink containment. Source
assertions verify both additional manifests plus pinned reused controller/schema
hashes. These tests are not a .NET differential oracle or real S3 tests.

The required real PostgreSQL tests are:

- `TestPGDevelopPackageDownloadBranches`: actual SQL reads/counters, genuine ZIP
  bytes, redirect, ASCII slug casing, null/blank key and missing app responses.
- `TestPGDevelopPackagePolicyAndFailures`: default-denied constructors, identity
  rejection, synthetic stage/soft-delete/project visibility, persisted increments
  after storage/URL failure, bigint overflow, read-only/closed database failures,
  cancellation, and zero-row update semantics.
- `TestPGDevelopPackageAtomicCounter`: 32 concurrent requests through independent
  pools with no lost increments.
- `TestPGDevelopPackageRestartPersistence`: redirect metadata and committed count
  survive PostgreSQL stop/start; required in before and after phases.

In this task's local executor the focused race-enabled Go tests passed, but
PostgreSQL could not create its Unix socket (`Operation not permitted`), so none
of these real database cases ran here. They are not counted as passed merely
because an unconfigured `go test` skips them. The integration owner must require
non-skipped execution in CI, including the restart phase. Production identity,
global-filter, Unicode and S3 compatibility remain unresolved even if those
synthetic database tests pass.
