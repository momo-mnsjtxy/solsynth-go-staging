# Product catalog and bot command boundary assessment

No product catalog endpoint was implemented or counted as runnable. Follow-up
licensed documentation research established the bot command DTO and a real
synthetic read candidate, documented in `bot-commands-PROVENANCE.md`. The initial
filename-oriented search missed an authored product consumer; the fuller audit
in `../../docs/product-client-contract-audit.md` supersedes that negative claim.

At `Solsynth/DysonNetwork@4ee2d0cebb9da8284a92483fee6dce795bf6947c`, the retained
`source/DysonNetwork.Develop__Identity__CustomAppPublicController.cs` declares
`GET /api/apps/{slug}/products` and `GET /api/apps/{slug}/products/{identifier}`.
`CustomAppService.GetAppBySlugAsync` compares lowercased slugs and includes the
project and developer. `product-source/...AppProductService.cs` proves that list
results include product state, filter by app ID, and sort by identifier; detail
matches the app ID and exact identifier. Neither public method directly invokes
the service's remote file/payment dependencies. It does not follow that those
dependencies are absent elsewhere in the service or that global authentication
and permission middleware allow anonymous production access.

The controller returns shared `SnAppProduct` entities. The Develop-owned EF
snapshot proves SQL columns, relations, and some scalar types. Follow-up reading
of owned `docs/APP_PRODUCTS.md` establishes documented catalog, fulfillment and
state fields, while authored Solian `PaymentOverlayAppProduct` consumes a partial
catalog projection. An authored watch model contributes partial file-reference
evidence too. Additional inherited/back-reference fields, shared annotations,
complete media shape and integer-to-string enum/storage behavior are not yet
established. Returning arbitrary SQL row JSON or treating a permissive consumer
projection as the entire server response would overstate compatibility. The full
updated evidence and its bounded coverage are in the audit linked above. No
generated part, imported SDK definition, NeTo/Spec schema, descriptor, binding,
or implementation was read to fill the remaining gaps.

The neighboring public bot-command controller returns shared `SnBotCommand`
objects (or an empty list). The retained authored `BotAccountService.cs` confirms
local bot/config reads. Authored Solian `bot.dart` does not supply command DTOs,
but follow-up research of owned `docs/BOT_CHAT_API.md` does. The new bot command
candidate reads actual configured data under explicit storage/global-policy
assumptions; it does not fabricate an always-empty response.

The eligible adjacent implementation is miniapp package download: its successful
response is an explicit redirect or ZIP bytes, so no shared entity JSON must be
reconstructed. See `packages-PROVENANCE.md` for the bounded implementation and
its remaining production policy/S3 limitations.

`product-source/manifest.json` records exact repository revisions, Git blob IDs
and SHA-256 digests for inspected additional sources and their licenses.
Previously retained controllers, database snapshot, startup and licensing
evidence remain in `source/manifest.json`; they were reused unchanged. The
CustomAppController snapshot documents rejected adjacent options. The
BotAccountService snapshot also supports the later public command read. Neither
implements private/authenticated or financial operations in this slice.
