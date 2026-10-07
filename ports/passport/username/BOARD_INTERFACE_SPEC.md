# Account board interface specification

## Decision

The board domain can proceed without importing NeTo's implementation: define
local models and ports from the licensed client and controller/migration call
sites below. A behavior-preserving internal service is feasible. A drop-in
HTTP/gRPC replacement is not yet justified because wire descriptors, universal
payload-normalization edge cases and authorization configuration remain unknown.
Keep those dependencies explicit; do not fill them with permissive stubs.

This is source-derived specification work. Licensed server implementations and
licensed client code have been inspected, so this is not a clean-room claim.
No denied SoSYS repository content or unlicensed implementation was retrieved.

## Pinned lawful evidence

Both repositories carry GNU AGPL v3 license files. NeTo is a separate submodule
whose license remains unknown and its implementation has not been used.

- [DysonNetwork license](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/LICENSE.txt)
- [Solian license](https://github.com/Solsynth/Solian/blob/ffd0838e5f04cb40bea311a39118ebeedad210c1/LICENSE.txt)
- [Solian board models, enum, JSON writer/parser and calls](https://github.com/Solsynth/Solian/blob/ffd0838e5f04cb40bea311a39118ebeedad210c1/lib/accounts/widgets/account/board.dart)
- [Self board HTTP controller](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountCurrentController.cs)
- [Public board HTTP controller](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountPublicController.cs)
- [Admin board HTTP controller](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountAdminController.cs#L418-L530)
- [Board service contract consumers](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountBoardService.cs)
- [Board RPC handler](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountServiceGrpc.cs#L320-L361)
- [Initial board DDL](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Migrations/20260708150813_AddAccountBoardItems.cs)
- [Widget-key DDL](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Migrations/20260708152943_AddCustomAppWidgetKeyToAccountBoardItems.cs)
- [Develop widget RPC handler](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/CustomAppServiceGrpc.cs)
- [Develop validation call sites](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/CustomAppService.cs#L366-L455)
- [Public widget discovery DTOs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Develop/Identity/BoardPublicController.cs)
- [Cache group prefix](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountService.cs)

## HTTP JSON: board item

Self/admin replace bodies are arrays of these objects. Names are snake_case.

| Field | Observed type and default | Evidence/constraint |
|---|---|---|
| id | optional nullable UUID string | Self controller generates UUID when absent; admin uses empty UUID and service generates |
| order | integer, omitted defaults to 0 in controller model | Duplicate orders rejected; PostgreSQL integer; no nonnegative check shown |
| kind | integer; omitted defaults to 0 | Solian enum explicitly sends prebuilt=0, customApp=1; unsupported values rejected by service |
| widget_key | optional nullable string | MaxLength(256); prebuilt widget key |
| custom_app_id | optional nullable UUID string | Required for custom-app placement |
| custom_app_widget_key | optional nullable string | MaxLength(256); custom-app placement requires non-whitespace |
| is_enabled | boolean, defaults true | Explicit false must remain false |
| payload | optional nullable JSON object; null/absent becomes empty object | Client sends a map; server Dictionary projection |

Client response parser consumes these fields, defaults absent order/kind to zero,
absent enabled to true and non-map payload to empty map, then sorts by order.
It ignores additional entity fields. This confirms client compatibility but does
not prove the complete server response serialization contract.

Database adds account_id (non-null UUID), created_at and updated_at (non-null
timestamptz), deleted_at (nullable timestamptz), payload (non-null jsonb), id
primary key, a unique (account_id, order) index and a nonunique
(account_id, custom_app_id, custom_app_widget_key) index. No account foreign key
is declared in these two migrations. Later migrations and the complete model
snapshot must be checked before adopting this as current full schema.

## HTTP routes and authorization

Server routes use /api; Solian uses /passport/... through the gateway.

| Method and route | Observed guard | Result |
|---|---|---|
| GET /api/accounts/me/board | Authorize + AccountsProfileBoardManage; CurrentUser required | 200 array; explicit unauthenticated 401 |
| PUT /api/accounts/me/board | Same | 200 ordered array; invalid operation 400 PASSPORT_BOARD_REPLACE_FAILED |
| GET /api/accounts/{name}/board | No action/class Authorize shown | Lookup account; 200 array or shared NotFound error |
| GET /api/admin/accounts/{identifier}/board | Authorize + AccountsView | 200 array or 404 PASSPORT_ACCOUNT_NOT_FOUND |
| PUT /api/admin/accounts/{identifier}/board | Authorize + AccountsBoardManage | 200 array; replace validation 400 PASSPORT_BOARD_REPLACE_FAILED |
| POST /api/admin/accounts/{identifier}/board/items/{itemId:guid}/payload | Authorize + AccountsBoardManage | Body {payload: object}; 200 item; missing item 404 PASSPORT_BOARD_ITEM_NOT_FOUND; invalid payload 400 PASSPORT_BOARD_UPDATE_FAILED |
| DELETE /api/admin/accounts/{identifier}/board/items/{itemId:guid} | Authorize + AccountsBoardManage | Ownership query by account/item; 204; missing account/item 404 |

Symbolic permission names are evidenced; their exact wire strings and evaluation
rules are NOT known from these call sites. Inherited/global middleware can add
guards. Public route must not be assumed universally anonymous until middleware
is verified. Admin DELETE currently saves directly without the board service's
cache purge; preserve/flag this difference rather than silently invent behavior.

The app-private payload flow is described as app-secret authorized by service
comments, but the exact initiating endpoint/auth middleware was not located.
The Passport RPC validates account/app IDs, optional item ID and nonblank widget
key; service then enforces account, app and widget ownership. This does not
establish how callers authenticate to that RPC.

## Widget manifest/discovery JSON

GET /develop/apps/board (server /api/apps/board) returns an array; optional slug
filters one app, unknown/no usable widgets returns an empty array. The public
server DTO supplies id, slug, name, optional description, publisher_name and
board_widgets. Each widget provides:

- key, name, description, slug, is_enabled, renderer_type
- field_types: objects consumed by client with name, type, label, format, required
- required_fields: array of strings
- max_payload_bytes: nullable integer
- allow_multiple: boolean

Client defaults format to empty string, required to false, enabled to true and
allow_multiple to false. These are consumer defaults, not proof of server
omission behavior. Server string lengths/default metadata beyond call sites
remain unverified.

## Payload contract: what is known and unknown

Licensed Develop validates enabled widget, configured board scope and
payload_type equal to object case-insensitively. Empty payload is accepted before
size, required-field and universal-envelope checks. Otherwise it checks serialized
UTF-8 byte size, presence of required keys and the universal normalizer, then
compares each declared field's nested value with string/number/boolean/object/
array/null types. Unknown declared types pass its type matcher.

Its error message establishes that an ordinary field envelope has value, label,
and optional format. Solian can consume nested value strings and treats image
and background as reserved fields; its prebuilt widgets also consume raw values
such as content, file_ids and file_id. These consumer affordances do NOT prove
that every such payload is accepted by the universal server normalizer.

Unknowns requiring a documented contract or licensed tests:
required/null/empty label behavior; reserved-field normalization; allowed formats;
unknown envelope properties; nested depth/size behavior; numeric and JSON escaping
semantics; exact normalization and errors. Do not implement a guessed validator.

## RPC message projections observed at call sites

| Message/service | Known fields / outcomes |
|---|---|
| DyCustomAppService.GetBoardWidget | Request AppId string UUID, WidgetKey string; response App and Widget with Key, IsEnabled, AllowMultiple used by Passport |
| DyCustomAppService.ValidateBoardWidgetPayload | Request AppId, WidgetKey, Payload protobuf Struct; response Valid bool, Message string, Widget, NormalizedPayload Struct |
| DyProfileService.UpdateBoardItemPayload | Request AccountId UUID string, CustomAppId UUID string, optional BoardItemId UUID string, CustomAppWidgetKey string, Payload Struct; response DyAccountBoardItem |

Bad identifier/key formats map to InvalidArgument. Missing board item maps to
NotFound; invalid kind/ownership/payload maps to FailedPrecondition. Widget lookup
NotFound/FailedPrecondition becomes board-validation error; other errors propagate.

Generated descriptors or original public .proto definitions are still needed for
protobuf field numbers, fully-qualified package/service paths, field presence,
exact enum mappings, and complete DyAccountBoardItem conversion. C# property
names do not determine protobuf wire compatibility. No guessed descriptors.

## Proposed local ports, without NeTo code

- BoardRepository: ordered list; retrieve account-owned item or first widget match;
  atomically replace all account items; save/remove item; auditable/soft-delete
  behavior explicitly supplied by the adapter
- WidgetCatalog: get app/widget identity, canonical key, enabled and multiplicity;
  validate/normalize payload through an authoritative provider
- UniversalPayloadNormalizer: explicit dependency for the unverified universal
  envelope contract; absence is an error, never automatic acceptance
- AccountResolver and PermissionEvaluator: use existing verified identity/permission
  adapters; symbolic capabilities remain separate from wire-string mappings
- AccountCache: invalidate group account:{account UUID}; guarantee call sequencing
  after commit; self controller performs an additional account purge
- UUIDFactory: generated IDs only for absent/empty IDs at the evidenced boundaries
- Clock/AuditAdapter: inject creation/update semantics once verified

These can be implemented and tested offline with synthetic fakes today. Persisted
transactional replacement can follow after current schema/audit behavior is
verified. A deployable adapter still requires authoritative protobuf descriptors,
permission mappings, payload normalization specification and full response/error
fixtures. Existing external validators/RPCs may remain behind adapters during a
staged migration, but no successful integration should be claimed until tested.

## Inputs that would remove the remaining gaps

1. Lawfully available board protobuf descriptors or .proto files for the three RPCs
2. Documented universal payload schema/normalization examples and rejection cases
3. Exact permission keys and middleware contract (including RPC caller identity)
4. Sanitized response fixtures for public/self/admin board reads and errors
5. Current schema/audit/soft-delete contract plus synthetic database fixtures

These can be supplied as interface documentation or appropriately licensed tests;
NeTo implementation access is not inherently required.
