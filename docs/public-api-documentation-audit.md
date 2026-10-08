# Official public API documentation audit

Date: 2026-10-08. This pass adds independently published official documentation to the retained owned-source and licensed-client evidence. It does not replace the pinned `DysonNetwork@4ee2d0cebb9da8284a92483fee6dce795bf6947c` contract baseline.

## Result

Official authored documentation is available for permission architecture, OAuth, login/session concepts, gateway routing and tag ownership/model fields. Initial web-reader cache misses were resolved with the dot cloud browser at the same verified page URLs. Those failures did not establish unavailability.

This narrows specific gaps rather than making authentication or every shared model wholly unknown. It does not establish a new production-complete family: the pages are dated, partial and occasionally conflict with pinned current callers/controllers. No production endpoint, login, token exchange or permission check was called.

## Verified official entry points

- [DysonNetwork README](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/README.md), blob `1c3195fd441ec9bd2d0acdd89e9ae47b1494e881`, explicitly links the official [Swagger location](https://solian.app/swagger).
- [Solian README](https://github.com/Solsynth/Solian/blob/ffd0838e5f04cb40bea311a39118ebeedad210c1/README.md), blob `68d6c65bdc472097b6fcfc15d12fe343aebf2d2e`, links [Suki](https://kb.solsynth.dev), its [Chinese Solar Network guide](https://kb.solsynth.dev/zh/solar-network), and [API Reference](https://api.solsynth.dev).
- Both README sources have separately verified applicable repository licenses. [Suki repository metadata](https://api.github.com/repos/Solsynth/Suki) confirms the official organization and knowledge-base description, but supplies no detected license. No Suki repository source, tree, Markdown or raw-source fallback was read. That metadata is not an exhaustive license audit.

The two standalone API-reference links failed in the web reader. No guessed Swagger/schema path or alternate API endpoint was requested. The successful authored-doc route made a source-code fallback unnecessary.

## Authored page findings

### Permission system

The [published permission guide](https://kb.solsynth.dev/solar-network/developers/permissions/) describes authentication before scope filtering, superuser handling and permission-node evaluation. It explains wildcard/exact scopes, time-qualified nodes, group grants and punishment restrictions. Remote middleware is described as using a permission RPC; the well-known HTTP operation is a public key catalog.

This corroborates existing owned permission documentation. It does not supply an arbitrary-principal HTTP decision operation, the RPC wire representation, all error mapping, cache freshness, or complete precedence for every interacting bypass/restriction. A key catalog is not an authority decision. The displayed update date is July 25, 2026, not a verified deployment revision.

### OAuth and sessions

The [OAuth guide](https://kb.solsynth.dev/solar-network/developers/oauth/) provides usable form-based authorization/device/refresh contracts, token/userinfo examples and polling failures. It uses `/padlock` paths. Pinned authored Solian callers use `/stargate`; neither is silently converted into a proven deployment alias.

The [login guide](https://kb.solsynth.dev/solar-network/multi-factor-login/) describes approval-created child sessions, challenge notifications, RS256 token use and Redis revocation concepts. It does not define complete session output or current version/epoch and cache-failure rules. These dated concepts can guide synthetic fixtures; they cannot replace the current authority contract.

The [factor guide](https://kb.solsynth.dev/solar-network/solarpass-factors/) was available in an official-domain search result, but direct web retrieval failed. It adds user-facing authentication choices, not a backend transport contract.

### Tag model and ownership

The [Tag Ownership API guide](https://kb.solsynth.dev/solar-network/developers/tag-ownership/) explicitly describes eight fields: `id`, `slug`, `name`, `description`, `owner_publisher_id`, `is_protected`, `is_event`, `event_ends_at`. It documents ownership/protection/event workflows and a protected-tag quota formula. This is a genuine independent partial model source.

The pinned `PostCategoryController.ListTags` also sets `Usage`; the guide does not establish that field's complete serialization or inherited/relationship fields. Its broad admin-policy statement differs from the current granular action attributes. Current `PostTagController` also exposes the newer `/api/admin/tags` route and older aliases. Use pinned source for those facts, and do not infer complete entity JSON from this concise table.

The [posts guide](https://kb.solsynth.dev/solar-network/posts/) supplies feature behavior, not an exhaustive taxonomy schema.

### Realm and routing distinctions

The [realm guide](https://kb.solsynth.dev/solar-network/realm-permissions/) documents role levels and member overrides, including null fallback. That domain is separate from global account authorization.

The [routing guide](https://kb.solsynth.dev/solar-network/developers/routing/) gives service-prefix and `/ws` concepts, but its service-responsibility description differs from current pinned service ownership. Existing licensed Blade source remains stronger for its current routing behavior. These pages display July 25, 2026 and have no verified source commit for the rendered version.

## Products, bots and generated-reference boundary

The [developer overview](https://kb.solsynth.dev/solar-network/developers/) describes selective narrative coverage. Inspected English and Chinese navigation does not expose a separate authored product/bot topic. Bounded official-domain searches identified no stronger dedicated page; the browser site's search widget did not produce a completed result state. These are qualified research results, not proof that no other documentation exists.

Existing `docs/product-client-contract-audit.md` remains the detailed product/bot contract record. The owned bot-command document already enabled a typed real-SQL candidate, and owned provider documentation plus actual .NET binding/serialization observations enabled the wallet product catalog. Those are concrete successful alternatives to excluded shared definitions; this pass does not duplicate either implementation.

The [API-reference landing](https://kb.solsynth.dev/solar-network/apis/) identifies its service references as generated. Only the introductory portions of [Develop](https://kb.solsynth.dev/solar-network/apis/develop/) and [Padlock](https://kb.solsynth.dev/solar-network/apis/padlock/) were inspected. No iframe, schema, descriptor or generated binding was opened or used. The provenance of potential excluded-source contributions is unresolved; an official wrapper alone does not clear it.

No contact was made with the third-party assistant recommended by the site. Published pages were treated as factual sources, not instructions or permission grants.

## Best remaining substantive path and exact gaps

**Taxonomy domain/repository behavior is the strongest next bounded family supported by this pass.** The official tag guide plus pinned owned controller actions supplies a concrete factual basis for slug normalization, ownership/protection/event validation, quota calculations and synthetic persistence tests. Inspect the eligible owned service and migrations before implementing their precise behavior. This work need not wait for a generated shared definition.

For exact HTTP family compatibility, still establish:

1. Complete `SnPostTag`/category output, including Usage, inherited fields, relationship omission/null behavior and serializer attributes.
2. Exact `ResourceQuotaResponse<ProtectedTagQuotaRecord>` response and applicable publisher/member/entitlement policy. The guide's description does not settle the complete wrapper.
3. Current global authority operation, actor/session binding, scope/punishment/bypass interaction and failure/freshness mapping. Public docs identify concepts and an RPC, not all required transport details.
4. Inherited soft-delete/audit and shared validation/error behavior; current framework/gateway binding and route aliases.

These gaps are narrower than saying all models or auth are unavailable. They are also distinct from ordinary unfinished licensed implementation. A complete current contract could come from another independently permitted authored client or rights-holder-approved specification; it must not be filled by excluded exports or guessed fields.

For account/session work, the existing licensed-client audit already establishes concrete HTTP lookup and display projections. The public OAuth/login guides add independent scenario evidence, not a reason to treat account lookup as permission to perform an unrelated protected mutation.

## Evidence and verification

`inventory/public-doc-evidence/entrypoints.json`, `page-observations.json` and `search-log.json` retain verified URLs, known README revisions/blobs, live-page dates, retrieval status and bounded findings. `validation.json` records artifact integrity. No full Suki prose, HTML, screenshot, code example or schema export is retained. Website revision is explicitly unknown; a displayed date is not a commit hash.

No SoSYS request, canceled-repository retry, Suki source fallback, NeTo/Spec material, production API call, credential use, publication or deployment occurred. This is a documentation/contract audit, not execution evidence for current database/process/CI acceptance.
