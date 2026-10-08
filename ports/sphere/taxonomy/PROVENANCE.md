# Taxonomy provenance

SPDX-License-Identifier: AGPL-3.0-only

Go code is source-informed from Solsynth/DysonNetwork at `4ee2d0cebb9da8284a92483fee6dce795bf6947c`. The retained source and independent factual observations establish a runnable projection, not exhaustive shared-entity compatibility.

## Existing pinned evidence reused without changes

- `inventory/operation-sources/DysonNetwork.Sphere/Post/PostTagController.cs`: public/admin actions, route aliases, local request DTOs, permission constant references, error/status mapping and admin queries.
- `inventory/operation-sources/DysonNetwork.Sphere/Post/PostCategoryController.cs`: public category/tag search/order/count/detail and subscription actions.
- `inventory/operation-sources/DysonNetwork.Sphere/Post/PostCategoryAdminController.cs`: category admin queries/create/update/delete.
- `inventory/operation-sources/DysonNetwork.Sphere/Post/PostCategorySubController.cs`: actor-specific subscription list with Take-before-Skip.
- `inventory/operation-sources/DysonNetwork.Sphere/AppDatabase.cs`: owned registration calls for shared soft-delete and audit hooks. Their existence is observed; their omitted implementations are not reused.
- `ports/sphere/nodeinfo/source/DysonNetwork.Sphere__Migrations__AppDatabaseModelSnapshot.cs`, blob `97288c526bb8dd7c120fd9157d56069663487b4b`: taxonomy, post/link/publisher/subscription columns and relationship metadata. A migration snapshot establishes storage types, not complete entity JSON or runtime soft-delete behavior.
- The existing operation-source and nodeinfo manifests retain commit/blob/SHA-256 and license evidence for those files.

## Newly retained permitted authored code

`source/manifest.json` pins paths, revisions, Git blobs and SHA-256 for:

- Owned `PostTagService.cs`, blob `3a3d3b7f7336cd8fb8d43b13bd5801a334dc2a8d`: normalization, ownership/manager checks, protection/event rules, owned quota-record projection, owner-account fallback and entity mutations.
- Authored Solian `lib/creators/screens/posts/tag_manage.dart`, `lib/posts/pods/post_categories.dart`, and `lib/posts/screens/post_category_detail.dart`, at `ffd0838e5f04cb40bea311a39118ebeedad210c1`: client field accesses, mutation intent, quota usage and optional owner display. These imports do not authorize fetching their excluded SDK dependencies. No generated file was read or retained.

Both repositories' applicable AGPL-3.0 licenses were already verified in the baseline; a license copy is present in `source/LICENSE.txt`. Retained file bytes exactly match their fetched Git blob IDs.

## Independently published documentation

The official [Tag Ownership API](https://kb.solsynth.dev/solar-network/developers/tag-ownership/) supplies factual observation of the eight named fields and ownership/protection/event workflows. See `docs/public-api-documentation-audit.md` and `inventory/public-doc-evidence/page-observations.json` for dates, provenance and limitations. No Suki source/Markdown, full rendered article, generated schema or NeTo/Spec definition was retained. The rendered revision is unknown, and older policy text cannot supersede the pinned controller attributes.

The field projection and independent internal choices are specified in `docs/taxonomy-continuation.md`. The local audit ledger and transactional tombstone/locking policy are new implementation designs, not unattributed copies of omitted shared components.
