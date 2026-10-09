# Independent continuation review

Date: 2026-10-08. Scope: the locally reconciled main/recovery runtime plus the
new Develop package and Sphere NodeInfo compatibility candidates. This is a
correctness/integration review, not a production approval or security scan.

## Findings and disposition

1. **Fixed: malformed empty-container JSON accepted.** Exchange configuration's
   trailing-comma preprocessing accepted `{,}`, `{"ignored":[,]}` and similar
   invalid syntax. Independent .NET configuration execution rejected these.
   The preprocessor now requires a preceding value; valid trailing commas remain
   accepted. New isolated reference evidence covers ten configuration cases.
2. **Fixed: badge route Unicode casing differed from MVC.** Go `EqualFold`
   accepted long-s/Kelvin-sign variants of ASCII route literals that .NET MVC
   rejected. The route literal matcher now folds ASCII only. Six independently
   executed MVC fixtures cover rejection and valid ASCII controls.
3. **Fixed: package errors dropped known response fields.** The two known
   miniapp/package 404 responses initially used a candidate-only error envelope.
   They now preserve the licensed controller's assigned code, message and status,
   without claiming the unverified complete Shared error pipeline.
4. **Fixed: catalog process smoke expected the wrong watch status.** Stateless
   `--storage=none` makes watch dependencies unavailable (503), before authentication.
   The smoke now uses a real synthetic file adapter to test authentication denial
   (401). The host also reports the actual bound port for ephemeral-port readiness.
   These process changes were reviewed; this reviewer did not run live sockets.
5. **Clarified: source-local behavior is not global middleware parity.** Badge
   documentation no longer infers the complete Passport authentication/permission
   pipeline from a controller without local authorization attributes. Exchange
   documentation scopes its explicit anonymous action and MVC evidence similarly.
   Historical recovery observations are distinguished from current evidence.

No additional functional defect was found in the reviewed candidate runtime.
Production authentication, full global policy, exact candidate transport parity,
real object storage, migrations and whole-platform coverage remain unresolved.
Those limitations are substantive and are not removed by these review results.

## Integration checks

- All 45 non-host Go files in the original baseline manifest remained byte-identical.
  Only the two existing host files changed; NodeInfo and package files are additions.
- Catalog/badge routing retains the protected watch handler and does not add the
  funds-changing exchange operation. File, PostgreSQL and disabled watch storage
  modes retain their distinct dependency/authentication behavior.
- Package lookup, missing-key rejection, committed atomic counter increment and
  redirect/file ordering match the retained owned controller/service. The handler,
  service and repository require explicit synthetic constructors. The local-file
  adapter is identified as synthetic storage; URL/Unicode/header differences are
  documented. MiniAppStage's lawful owned declaration is distinguished from the
  unrelated unresolved CustomAppStatus and shared policies.
- NodeInfo DTOs retain the source's unusual 2.0/2.1 field differences and discovery
  hrefs. The three SQL counts retain source order and lack an added transaction or
  publisher join. Default command/handler/repository paths deny; synthetic enum
  and soft-delete policy selections remain explicit.
- The integration runner requires 27 actual PostgreSQL cases and five after-restart
  assertions. NodeInfo's separate fixture schema avoids cross-suite aggregate
  pollution. New HTTP process scripts do not follow stored redirect destinations.

## Independently executed evidence

Using Go 1.26.4 and .NET SDK 10.0.401 / runtime and ASP.NET Core 10.0.12:

- Reproduced all original exchange reference results: 1,793 catalog/configuration
  cases, six MVC method/path cases and the 1,475-entry scalar-casing map.
- Reproduced all 34 original badge MVC reference cases.
- Created and reproduced the separate 16-case benign regression reference in
  `tests/contract/recovery-regression-oracle`, with exact input/source hashes and
  a Go differential test. Original recovered oracle files were not rewritten.
- Passed targeted race-enabled tests for all three commands, Develop/package,
  NodeInfo, its SQL adapter, the Develop SQL adapter, exchange, recovered
  badge/exchange contract comparisons and the new regression oracle.
- Passed vet for all commands and the affected runtime/reference packages.
- Passed offline integrity checking of 269 retained source/fixture entries,
  nine benign mutation-rejection checks and a missing-file rejection check.
- Python smoke/check scripts compiled and shell integration/reproduction scripts
  passed syntax checking.

No actual PostgreSQL case or live HTTP process was run by this review. The local
socket restriction was respected, without alternate connection attempts. Pending
combined CI must run against the exact published tree before its database and
process results can be claimed. Existing historical CI results apply only to
their recorded commits. The parent continuation verification report owns final
aggregate checks, publication status and any changes after this review.

Only eligible retained owned/licensed material was read. No excluded repository,
protocol definitions, generated binding, production probe, vulnerability
reproduction, credential operation or external write was used for this review.
