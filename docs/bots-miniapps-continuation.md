# Canonical bots and miniapps continuation

This slice provisions app-owned bots as canonical accounts, stores bounded chat
configuration and commands, and publishes miniapp metadata and validated package
links. It contains no command execution or background event consumer. The frozen
base is `a0dd80ab458f557bfbe9b09a60db5c414e54963d`.

## Provenance and compatibility

`ports/develop/bots/source-evidence.json` records the exact repository revisions,
Git blobs, local retained files, SHA-256 values, and license evidence. The authored
DysonNetwork controllers/services are pinned to
`4ee2d0cebb9da8284a92483fee6dce795bf6947c`; the authored Solian bot caller model is
pinned to `ffd0838e5f04cb40bea311a39118ebeedad210c1`. The full retained bot private
and public controllers, bot service, miniapp private/public controllers, miniapp
model/service/storage service, and relevant authored command documentation and
Solian model were reviewed. No new generated shared definitions, SoSYS, NeTo,
Spec, or unlicensed content is imported.

The current authored controllers govern route names. BOT_CHAT_API.md supplies
the complete four-field command and four-field parameter contracts; its older
public route and webhook examples do not define runtime behavior. Bot account
hydration uses the canonical Go account/profile representation plus the source
request's first/middle/last name, gender and birthday fields. Those five extras
are bot profile metadata, not another account identity. Shared model members
whose complete representation is not licensed and established are not invented.
Responses identify this owned projection with `X-Solsynth-Projection`.

The source associates bots and miniapps with a developer project. This owned
continuation additionally requires a canonical app in that same project. Every
private route requires `dev=<publisher-name>&proj=<project-uuid>&app=<app-uuid>`.
The `app` selector and individual command CRUD routes are explicit additions.
Read access requires a joined, unrevoked publisher viewer; mutations require an
editor and the fresh named canonical permission. Slug/profile lengths are bounded
by the owned schema, which is stricter than some legacy request limits.
The permission strings are owned policy names: `developers.bots.create`,
`developers.bots.update`, `developers.bots.delete`, `developers.bots.chat.manage`,
`developers.miniapps.view`, `developers.miniapps.create`,
`developers.miniapps.update`, `developers.miniapps.delete`, and
`developers.miniapps.package.upload`. They mirror the authored controller's
permission boundaries without claiming unresolved shared constant values.

## Runtime and host integration

Apply `botpg.Schema` explicitly after canonical authority, accounts/publishers,
developer apps, and media schemas. Schema installation seeds no accounts,
credentials, permissions, or quotas. Configure publisher capacity explicitly in
`owned_bot_quotas`; an absent, future, expired, or exhausted quota denies creation.

Use the same database pool and transaction guard as the canonical authority and
media adapters:

```go
repo := botpg.NewWithAuthority(db, transactionGuard).WithMedia(mediaRepo)
service := &bots.Service{
    Repository: repo,
    Authority: verifier,
    Bind: transactionGuard.Bind,
}
handler := service.Handler()
```

`bots.RouteMatches(request.URL.Path)` covers `/api/bots`, `/api/private/bots`,
`/api/miniapps`, and `/api/private/miniapps`. The handler accepts canonical bearer
authentication, performs verifier preflight, and binds the original opaque proof
for fresh checks inside each write transaction. The verifier used to produce the
proof must be the exact instance used by the guard. Hosts already binding that
proof can leave `Bind` nil. Preserve the canonical middleware's path rewrite
before dispatching a gateway path to this handler.

`repo.Ready(ctx)` checks the installed schema. `New(db)` supports public reads but
keeps private operations closed; it cannot pass readiness as a configured private
service. `WithMedia` is an initialization-time dependency setter. A missing media
provider returns 501 for package operations and does not affect metadata routes.
No provider flag, scheduler hook, or `ProcessDue` is needed for this slice.

No host file is changed in this delivery. The integrator adds the constructor,
route dispatch, readiness check, schema order, and PG inventory entries while
preserving existing callbacks and worker scheduling.

## Endpoints

| Path | Methods and behavior |
| --- | --- |
| `/api/private/bots` | GET bounded list; POST canonical account/profile/bot creation |
| `/api/private/bots/{id}` | GET, PATCH metadata/active state, DELETE atomic soft deletion |
| `/api/private/bots/{id}/chat` | GET configuration without writes; PUT replacement |
| `/api/private/bots/{id}/chat/manifest` | POST documented configuration patch |
| `/api/private/bots/{id}/commands` | GET all commands; POST a command |
| `/api/private/bots/{id}/commands/{name}` | GET, PUT replacement, DELETE |
| `/api/bots/{id}` | GET active canonical bot projection |
| `/api/bots/{id}/developer` | GET canonical developer/publisher projection |
| `/api/bots/{id}/chat` and `/commands` | GET active public configuration or commands |
| `/api/private/miniapps` | GET bounded list; POST metadata/manifest/stage |
| `/api/private/miniapps/{id}` | GET, PATCH, DELETE |
| `/api/private/miniapps/{id}/package` | Owned PUT `{"file_id":"canonical-media-uuid"}` |
| `/api/miniapps` | GET production discovery with bounded offset/take/search |
| `/api/miniapps/{slug}` | GET current production metadata |
| `/api/miniapps/{slug}/package` | GET canonical validated ZIP bytes with range support |

List limits are 1–100, default 20; offsets are 0–100000. Private and public lists
return `X-Total`. Commands retain their order, have at most 100 entries and 32
parameters per command, and occupy at most 64 KiB. Requests reject duplicate
JSON keys, unknown fields, excess nesting, oversized bodies, duplicate query
values, query credentials, and spoofable identity headers. Internal SQL errors
become sanitized 503 responses.

Miniapp stages are the authored Development=0, Staging=1, Production=2. Public
miniapps require their own stage 2 and the app's production status 1, plus live
project/publisher ancestry and an active canonical owner. Bots additionally need
their own active flag and active canonical account. This public policy is an
explicit tightening of the older candidate bot-command read, which exposed an
inactive fixture bot.

## Transaction and identity invariants

Provisioning inserts one canonical authority account, one canonical profile, and
one association in the same transaction. Bot automation ID, account ID, profile
ID, app ID, and publisher ID remain distinct. No session, signin identity, secret,
implicit wildcard grant, or consent is created. Normal user routes gain no new
principal type or credential acceptance.

The write boundary resolves immutable owner and bot associations from canonical
tables, then locks the complete actor/owner/target-account set in sorted UUID
order before invoking TransactionGuard. This avoids opposing editors each
holding their actor while waiting for the other's owner account. It then locks
publisher/member, project, and app, checks current ancestry, membership, account
state and permissions, and revalidates the original proof after lock waits and
again before commit. Direct repository calls without the bound private proof
fail. Account IDs from request payloads are never accepted as target identities.

Deactivation disables and versions the canonical account. Reactivation is allowed
only when its current version matches the version disabled by this association;
a subsequent canonical disable/version change cannot be undone by a developer.
Active target disable punishments also deny updates/configuration/command writes.
Deletion disables/versions the account and soft-deletes the association atomically;
repeating deletion has no further version or audit effect. Reserved names/slugs
survive deletion. All domain mutations and their audit records share a transaction.

## Packages and media policy

Upload through the existing canonical media service, then link its file ID with
the owned PUT route. The acting editor must own the ready, explicitly public
file. The existing media transaction resolver retains its row lock through commit;
the existing media `Open` checks current visibility and provides the bytes. This
creates no object store or private-media exception.

Registration checks actual bytes against canonical SHA-256 and size, then validates
a ZIP of at most 5 MiB compressed, 1024 entries, and 32 MiB expanded. It rejects
malformed/CRC-invalid entries, duplicate or unsafe paths, special file types, and
missing/ambiguous manifests. Every entry is decompressed to a discard sink within
the expansion bound. Only the manifest is decoded. Files are never extracted or
executed and manifest URLs are never fetched. Manifest fields use the authored
case-insensitive archive convention with explicit alias-collision rejection.

Package metadata is projected from the current canonical file only while it is
ready/public and matches the validated fingerprint. Each package read freshly
checks miniapp/app stages, ancestry and media visibility, opens through canonical
media, and atomically increments its download counter. Downloads use attachment,
ZIP content type, nosniff, no-store and the canonical hash ETag. A newly private,
deleted, changed, or disabled-owner file cannot supply the package. The original
multipart POST upload path remains an explicit 501 gap; the owned PUT contract is
documented separately and does not pretend to implement that legacy flow.

## Schema changes

Five new tables: `owned_bot_quotas`, `owned_bots`, `owned_bot_chat`,
`owned_bot_miniapps`, `owned_bot_audit`. New owned indexes reserve project bot
slugs, app bot ordering, global miniapp slugs, and miniapp plugin IDs. The sole
canonical-schema addition is redundant unique index
`owned_custom_apps_project_identity ON owned_custom_apps(id,project_id)`, needed
by the association's composite foreign keys. No canonical table column changes.

## Deliberate remaining gaps

Bot API-key list/create/rotate/delete paths return 501 after checking scope.
Reusing the owned app-secret authority safely requires explicit app-to-bot scope
binding, fresh bot/app/publisher/owner checks, expiry/revocation/version behavior,
and a distinct bot-credential lane limited to separately authorized bot operations.
An app secret must not become an ordinary user bearer credential. None is issued
or accepted by this slice.

Subscribed-event names, auto-approval flags and command definitions are metadata.
There is no webhook delivery, autonomous command runner, shell execution, external
messaging, group-chat auto-join or API-key execution. Picture/background and
miniapp icon/background cloud-reference IDs return 501 because their complete
legacy reference DTO/provider mapping remains unresolved. Miniapp package execution
and external entry-URL fetching remain outside this slice. Unimplemented shared
DTO members are not represented as fabricated data.

## Verification

Local `go test -race ./ports/develop/bots ./internal/botpg`, focused `go vet`, and
compilation of all integration tests pass. No local PostgreSQL process, socket,
production service, real account, or live credential was used. Actual SQL execution
and process restart remain required in the guarded parent CI runner.

Add these exact tests to the mandatory before-restart inventory:

- `TestPGBotsCanonicalLifecycleAndCommands`
- `TestPGBotsScopeIsolationHTTPAndFreshAuthority`
- `TestPGBotsQuotaConcurrencyAndAtomicRollback`
- `TestPGBotsOpposingEditorsAndOwnerDisable`
- `TestPGBotsRevocationAndControlledDeactivation`
- `TestPGBotsPolicyTimeBoundariesAndPublisherRevocation`
- `TestPGBotsMiniappCanonicalPackagesAndPrivacy`
- `TestPGBotsRestartPersistence`

Add `TestPGBotsRestartPersistence` to the mandatory after-restart inventory too.
Its persisted state contains only synthetic IDs and a package digest; the package
store is confined to the disposable runner root. Fixtures use explicit quotas,
synthetic accounts, named publisher selectors, original verifier/guard pairs, and
microsecond timestamps for time-window values. They contain no worker batch calls
or SQL CASE timestamp expressions. Concurrency coverage includes quota creation,
opposing editors, owner disable/session revocation across observed row-lock waits,
parallel package reads, rollback with no orphan account, and persisted exact bytes.
