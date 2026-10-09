# Product and bot client contract audit

## Result

The public bot command read operation has sufficient lawful documentation for a
typed, default-denied synthetic implementation. Product catalog contracts are
**partially evidenced**, not wholly unavailable: an initially missed authored
Solian payment-overlay consumer parses a catalog projection, and owned backend
documentation describes additional fulfillment/state fields. Complete original
product entity serialization and production JSONB conventions are not established
by the sources reviewed. No product response has been fabricated from SQL rows.

The earlier filename-only search was insufficient and its negative conclusion
is superseded by this audit.

## Pinned sources, licensing and coverage

- DysonNetwork revision `4ee2d0cebb9da8284a92483fee6dce795bf6947c`, with its retained
  AGPL-3.0 license. Read full owned `docs/APP_PRODUCTS.md`,
  `docs/APP_PRODUCTS_AND_APIKEY.md`, `docs/BOT_CHAT_API.md` and
  `docs/BOT_CHAT_MIGRATION.md`, alongside already retained owned controllers,
  services, database snapshot and serializer startup.
- Solian revision `ffd0838e5f04cb40bea311a39118ebeedad210c1`, with its separately
  verified AGPL-3.0 license. Tree enumeration is complete (1,619 entries,
  `truncated=false`); nested license names were also checked. The only separately
  licensed asset subtree found was `assets/icons`, which was not inspected for
  this contract work. No other repository's license was assumed from Solian.
- Verified `refs/heads/v3` still pointed to that exact pinned Solian commit before
  indexed code searches. Search results also identify pinned blob URLs. Every
  query returned `incomplete_results=false`, and all results fit its first
  100-result page. This establishes the reported indexed query results, not an
  absolute proof that dynamically constructed or unindexed code cannot exist.
- Full relevant authored reads include payment-overlay model/callsite,
  profile bot-developer lookup, generic network client and plugin-network JSON
  wrapper, app-profile/authorization parsers, authored chat input/room handling,
  build serializer options, dependency configuration, and authored native-watch
  models. These are preserved with exact blob and SHA-256 evidence.
- A broad exploratory 625-file authored-code/document selection was prepared;
  83 exploratory files were retrieved and hash-verified. It was deprioritized
  when indexed targeted searches exposed the actual consumer and backend docs
  established the bot DTO. It is **not represented as a completed semantic read
  of all 625 files**. Selection and retrieval status are retained explicitly.

`inventory/product-client-evidence/manifest.json` contains full reviewed sources,
licenses, hashes, sizes, revisions and source URLs. `search-results.json` retains
exact indexed searches/results. The independent C# fixture has a separate
manifest and is clearly identified as authored test material.

## Product evidence that is now established

### Owned backend contract

[`APP_PRODUCTS.md`](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/docs/APP_PRODUCTS.md)
explicitly states that the public list/detail return current product payloads
including fulfillment and state. Its tables document:

- Catalog: `id` UUID, `identifier` string, `display_name` string, nullable
  `description`, `currency` string, decimal `price`, nullable object `picture`
  and `background`, string `recurrence` (`none`, `weekly`, `monthly`, `yearly`),
  nullable `group_identifier`.
- Fulfillment: `is_address_required` boolean and `required_scopes` string list.
- State: `is_enabled` boolean, string `stock_mode` (`unlimited`, `daily`, `weekly`,
  `monthly`, `yearly`, `manual`), nullable `stock_quantity`, nullable
  `last_restocked_at`, nullable `last_restocked_quantity`.

Current owned controller/service code establishes list route
`GET /api/apps/{slug}/products`, detail route
`GET /api/apps/{slug}/products/{identifier}`, case-insensitive app slug lookup,
app-scoped reads, exact product identifier equality, included state and list
ordering by identifier. It does not show local enabled/stock filtering on reads.
No order creation, payment, contact access or subscription mutation was performed.

### Authored Solian consumer overlooked by filename search

[`lib/payments/payment_overlay.dart`](https://github.com/Solsynth/Solian/blob/ffd0838e5f04cb40bea311a39118ebeedad210c1/lib/payments/payment_overlay.dart),
blob `6c893ab7ae82ecbd18179be2f47ddb57d306b388`, defines
`PaymentOverlayAppProduct` and calls `/develop/apps/$appSlug/products`.

Its hand-written parser consumes `id`, `identifier`, `display_name` (also accepts
`displayName`), `description`, `currency`, `price`, and `picture`. The response is
treated as a list; non-map entries are discarded by that client, and each map is
passed through the authored parser. The parser turns numeric price into an int
and defaults missing scalar values. Those are **consumer choices**, not proof
that the server's decimal type or default values should be changed. It delegates
picture parsing to the imported `SnCloudFileReference` type.

### Native authored reference model

[`ios/SolianWatchApp/Models/Models.swift`](https://github.com/Solsynth/Solian/blob/ffd0838e5f04cb40bea311a39118ebeedad210c1/ios/SolianWatchApp/Models/Models.swift),
blob `33ab300bd334e5ad0b2d473a3c7b57304d519d90`, contains authored cloud-file and
lightweight cloud-file-reference models. The latter explicitly describes a slim
sticker/pack reference. This is useful independent partial media-shape evidence,
not proof that it is exhaustive or identical to the product entity's shared
server file-reference serialization. The `group_identifier` match in this file
belongs to subscription data, not a complete app-product entity.

### Remaining specific gaps

The docs and consumers do not establish whether additional product/app/state
relationship or inherited timestamp/identity fields serialize, all shared
per-property converters/ignore annotations, complete file-reference shapes, or
the mapping from stored enum integers to the documented string values. The
controller serializes a shared entity rather than an owned explicit response
record. Those gaps are narrower than the original claim that no model evidence
exists. A partial consumer parser is not an exhaustive server response schema.

## Bot command operation

[`BOT_CHAT_API.md`](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/docs/BOT_CHAT_API.md)
explicitly documents four command fields and four parameter fields, including
nullable types. Current source supplies route and read control flow. This enables
the complete candidate read operation, including configured commands, missing
config `[]`, missing-bot 404 and real SQL errors. It does not require inventing
remote account success or always returning an empty list.

The old docs route `/api/bots/public/...`, configuration webhook fields, and
migration defaults are stale compared with current owned code and snapshot.
They are retained as evidence of document drift and are not copied blindly.
The implemented route is `/api/bots/{botId}/commands`; config/private/messaging
operations and webhooks were not implemented as a side effect.

The authored Solian `bot.dart` is a different bot/account model, not a command
DTO. Its profile callsite requests `/develop/bots/{id}/developer`; it does not
demonstrate command parsing. Indexed searches found no `SnBotCommand` or
`bots/commands` match at the verified current pin. This is negative indexed
corroboration, not an assertion that every possible consumer is absent.

See `ports/develop/bot-commands-PROVENANCE.md` for the explicit strict lowercase
JSON fixture format, null/default decisions, unknown-field rejection, default
policy denial, synthetic .NET comparisons and required PostgreSQL tests.

## Generic JSON and SDK boundary

The authored generic Dio/network and plugin request wrappers carry JSON data;
they do not define product or bot-command schemas. Solian `build.yaml` selects
snake_case generated property naming for its own annotated models. No generated
part was read to fill missing types, and this build option does not establish
an imported model's declared fields.

Pinned `pubspec.yaml` resolves `solar_network_sdk` to
`socommon/packages/solar_network_sdk`. Pinned `.gitmodules` locates the submodule
at `src.solsynth.dev/SoSYS/Socommon.git`, gitlink
`6b870980cfa1bfd66c094ff03a119b285ee2ba29`. That source is **not accessed due to the
existing unresolved permission/canceled-source boundary**. No metadata retry,
mirror, alternate forge, SDK-definition retrieval or workaround was attempted.
Its license has not been determined here; it is not labeled license-absent or
proven unavailable. A separately licensed authored client would be eligible
after its own access and license checks; the NeTo/Spec restriction is not applied
as a blanket ban on all independent client models.

## Verification limits

No production endpoint probing, real account, external communication, credential
transmission, financial transaction, publication or cloud task was performed.
No database process/socket retries were made in this continuation. The documented
DTO C# fixture ran in-process; Go source-integrity, race and recorder tests pass.
Database tests compile but require non-skipped CI execution before an actual
PostgreSQL pass can be claimed. Synthetic fixtures establish their explicitly
selected contract, not full runtime equivalence with excluded shared code.
