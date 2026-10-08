# Canonical developer applications

This continuation implements owner-managed developers, projects, apps, product metadata, board manifests, expiring app credentials and explicit account consent in one owned PostgreSQL model. It uses the existing canonical publisher, membership, authority and account-board rows. It does not read from the legacy discovery fixture tables. `internal/developpg.NewOwned` supplies the supported canonical adapter for the retained `develop.Reader` interface; `develop.NewOwnedHandler` supplies public discovery under the host's credential middleware. Legacy fixture constructors remain bounded test candidates.

## Evidence and independently authored decisions

Retained licensed DysonNetwork revision `4ee2d0cebb9da8284a92483fee6dce795bf6947c`: complete CustomAppController, CustomAppPublicController, CustomAppService, AppProductController, AppProductService, BotAccountService, BoardPublicController, DeveloperController and DevProjectController. Existing `ports/develop/source/manifest.json`, `product-source/manifest.json` and `inventory/operation-sources/manifest.json` retain exact blob IDs and hashes. Authored Solian custom-app/bot models and authorized-app model/caller at `ffd0838e5f04cb40bea311a39118ebeedad210c1` establish the visible client projection; existing source/identity-client manifests retain their exact provenance. Generated bindings, excluded shared protocol/model definitions and canceled repositories were not used.

The owned policy is explicit: status 0 developing, 1 public production, 2 suspended. This is an authored enum and is not a recovered excluded upstream enum. Only production apps are publicly visible and can authenticate client credentials. App slugs remain globally reserved after soft deletion; project slugs remain reserved within a developer. Publisher membership comes from active canonical publisher rows and joined, nonrevoked members (viewer 0, editor 50, manager 100, owner 200). Enrollment requires owner; create requires editor; project update/delete requires manager. Account writes revalidate the original authority proof at their SQL transaction boundary, lock the publisher/membership/project/app, and audit only committed effects. No constructor migrates, enrolls, grants policy, generates credentials or activates production.

The authored API uses source request paths and field names where established. Private project reads now require membership. Public app/product reads hide unpublished apps; disabled products are hidden from public catalog reads. Unknown scopes, renderer kinds, recurrence providers, media-resolution requests, alternate credentials and unsupported identity providers fail closed. Known decimal prices remain exact JSON numbers. Product recurrence supports none, weekly, monthly and yearly. The composed host synchronizes supported digital recurring products into canonical commerce definitions in the same transaction; `recurring-products.md` documents fulfillment and explicit boundaries. External provider catalog synchronization is not claimed. Stock modes support unlimited, manual, daily, weekly, monthly and yearly; manual quantity updates expose durable restock timestamps/quantities. Commerce owns atomic reservation and settlement policy (`product-stock.md`).

## Implemented routes

- GET /api/developers and /api/developers/{name}; POST /api/developers/{name}/enroll
- GET/POST /api/private/projects?dev=; GET/PUT/DELETE /api/private/projects/{id}?dev=
- GET/POST /api/private/apps?dev=&proj=; GET/PATCH/DELETE /api/private/apps/{id}?dev=&proj=
- GET/POST /api/private/apps/{id}/products; GET/PATCH/DELETE /products/{productId}, with dev/proj scope
- GET/POST /api/private/apps/{id}/board; GET/PUT/DELETE /board/{widgetKey}, with dev/proj scope
- GET/POST /api/private/apps/{id}/secrets; GET/DELETE /secrets/{id}; POST /secrets/{id}/rotate, with dev/proj scope
- GET /api/apps; GET /api/apps/{slug}; GET /api/apps/{slug}/products and /products/{identifier}
- GET/PUT/DELETE /api/apps/{slug}/consent: explicitly authored first-party board-consent endpoint, not an OAuth-provider compatibility claim
- GET /api/private/apps/{id}/board/widgets and POST /api/private/apps/{id}/board/payload: strict app-secret endpoints

Management permissions are owned `developers.create`, `developers.projects.create/update/delete`, `developers.apps.create/update/delete`, `developers.apps.products.create/update/delete`, and `developers.apps.secrets.manage`. They are enforced in the transaction guard in addition to publisher roles. Consent requires the account's `accounts.profile.board` permission.

App secrets are random 256-bit opaque tokens with a random record ID. Only an app-domain-separated SHA-256 digest is stored; plaintext is returned on create/rotate only. They expire after 24 hours by default, at most 30 days; numeric time spans such as `01:00:00` and explicit Go durations are supported. Rotation replaces the stored hash in the same record and invalidates the old token immediately. Revocation is durable. Secret scope must be an explicit subset of the app's declared capabilities: `accounts.profile.board`, `orders.create`, `orders.status.manage`, `orders.metrics.read`. Board is the backward-compatible default when declared. App tokens cannot become user sessions, obtain account permissions, or authenticate another app.

Board requests accept exactly one Bearer or X-App-Secret value, reject cookies/query credentials/spoofed account headers, and are dispatched only through the two exact app routes. A payload push checks a live canonical account, public active app ancestry, current secret hash/expiry/scope, account consent/expiry/scope, enabled manifest, payload envelope, matching enabled board item and ownership. Checks and write share a READ COMMITTED transaction; account, app, secret, consent and board locks remain held through commit. Expiry is checked again after board-lock waits. Multiple matching board items require explicit board_item_id rather than guessing. The shared board cache is invalidated after commit when injected with WithBoardCache.

## Composition

Apply `apppg.Schema` after authority/account schemas. Existing `account_board_items` storage must be installed; this module neither duplicates nor migrates it. Host construction:

1. `repo := apppg.NewWithAuthority(db, guard).WithBoardCache(cache)`
2. `svc := &apps.Service{Repository: repo, Authority: proofIdentity{identity}}`
3. Normal routes pass through `bindIdentity` and `bindTransactionAuthority` into `svc.Handler()`.
4. Only `apps.IsAppCredentialRoute` routes use `svc.AppHandler()`; every other identity channel retains the host's normal rejection policy.
5. Inject `repo` into `board.Service.Catalog` for canonical widget placement/validation.

Commerce integrates through `RequireClient(ctx, tx, appIDOrSlug, token, scope) (apps.Client,error)` followed by `LockProduct(ctx, tx, appID, identifier) (apps.Product,error)`. Transactions must be READ COMMITTED, call app authorization before product locks, and recheck app authorization after lengthy waits before mutations to enforce wall-clock expiry. The client carries only canonical AppID and PublisherID. Merchant payout configuration and owned-ledger settlement belong to commerce, not to this app module.

## Validation and remaining limits

Focused Go tests and race tests pass for apps, apppg, develop and developpg. Integration tests compile; they are deliberately skipped without the guarded disposable PostgreSQL DSN. No local network sockets or production credentials were created. Four real-PG tests cover lifecycle, scope/isolation/revocation, concurrent revocation across row-lock waits, and process/database restart persistence; the restart test is intended in both before/after runner phases. The tests use only synthetic ephemeral accounts/tokens and a 0600 fixture file inside the guarded disposable cluster.

Unknown external OIDC/OAuth provider execution, bot account provisioning, custom order payment-provider calls, media resolution, external catalog synchronization and undocumented upstream error/enum exact parity remain outside this module. Their absence does not return placeholder success. Existing board-specific discovery response `/api/apps/board` remains pending rather than inventing its excluded slug-building convention. The canonical app list and individual app/catalog routes are implemented.
