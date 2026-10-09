# Composed post references

This owned continuation accepts the authored composer `embeds` list and legacy
`survey_id`, `fund_id`, `meet_id`, `live_stream_id`, `notable_day_id`,
`calendar_event_id`, location text/POINT fields and article `thumbnail_id`.
The authenticated post routes and aliases use the same canonical transaction,
publisher editor and realm checks, media links, timeline, automod and scheduling.

Retained licensed evidence: DysonNetwork `Post/PostActionController.cs` in
`inventory/operation-sources/DysonNetwork.Sphere/` (request fields, creation lines
250–279 and 390–695, update 1270–1603); retained PostService under
`ports/sphere/posts/scheduling-evidence/` (metadata/lifecycle); authored Solian
`ports/sphere/posts/upstream/lib__posts__widgets__compose__compose_shared.dart`
(composer list, canonical IDs, thumbnail). Existing owned canonical stores and
their individual provenance remain the reference for visibility and lifecycle.
No excluded Shared/protocol definitions or generated bindings were imported.

Behavior and intentional differences:

- Metadata has `meta.embeds` entries containing the original canonical `type` and
  `id`, or explicitly composed location `name`, `address`, `wkt`. Target snapshots,
  caller-supplied opaque metadata, arbitrary URLs and downstream credentials are
  never accepted. Existing blog previews retain their separate verified-domain
  provider path and exact link projection; mixed blog/native embeds fail closed.
- A full list replaces the previous list, including an empty list. Omitted lists
  preserve links. Explicit legacy null removes that kind. Ambiguous full-list and
  legacy writes fail. One entry per kind, at most 16. Location supports the owned
  bounded POINT geometry, not the source's arbitrary PostGIS geometries.
- Account-owned funds, meets and calendar events must belong to the composing
  actor. Surveys and live streams must belong to the source publisher. Surveys
  bound to another post cannot be redistributed. This narrower ownership scope
  deliberately avoids inventing cross-account delegation or new audience grants.
- The entire target scope is observed before target locking; mismatched owners or
  publishers fail. The existing actor/proof, sorted realm/publisher and post locks
  are retained. Target rows lock in kind/ID order before media; all ownership,
  lifecycle and visibility checks repeat after locks and media waits. Source
  publisher/realm editor rights and targets are also rechecked after automod and
  schedule-row waits, immediately before final original-proof validation. No new
  account, realm or publisher lock can be introduced by these accepted targets.
- Read projections check current targets in the caller's post snapshot. Deleted,
  expired or terminal targets disappear together with their identifiers. Closed
  funds require creator/invite visibility; calendar uses owner/public/mutual-friend
  visibility and blocks. A post supplies no nearby-origin proof, so meet references
  follow canonical participant/friend visibility and are absent anonymously.
  Private live streams require current canonical publisher editor membership,
  including current organization-realm membership. Stream references also honor
  the individual/realm owner's current eligibility and block policy. Public,
  nested, thread and list projections share this path. No cached private target
  metadata is persisted into blog metadata or served from a copied snapshot.
- Link creation never changes the target, spends funds, reserves a balance,
  joins a meet, admits a stream viewer, grants survey voting, subscribes a calendar,
  or grants file access. Those canonical operations retain their own authority.
- A thumbnail must be an existing post attachment on an article. Removing that
  attachment without clearing the thumbnail fails atomically. The thumbnail ID
  is projected only when the canonical attachment is still visible. No additional
  private file ownership grant is created.
- Due and explicit publication recheck references. A stale reference blocks a
  scheduled generation; removing it and making fresh publication intent permits
  recovery. Target deletion does not cascade-delete unrelated content. Post
  deletion removes only its associations in the same transaction.

Nine validation cases in `tests/integration/post_composition_test.go` and
`tests/integration/post_composition_review_test.go` are mandatory in both
integration registries. They cover original IDs and replacement replay,
public/nested privacy, no wallet transactions, target ownership rollback,
thumbnail/media rollback, independent target lifecycle, queued mutation after
cancellation, publication expiry/recovery, and an original saved restart fixture.
The restart case uses the exclusive shared helper, saves all seven original
embeds plus owner/outsider credentials and public verifier material before restart,
and only authenticates the original credentials and reads original state afterward.
One-hour synthetic credentials are issued before targets; verification time advances
by elapsed wall time. No credentials are renewed or authority restored afterward.
Ordinary Go tests skip actual PostgreSQL without its guarded disposable DSN;
no local PostgreSQL execution is implied.

Migration: apply `internal/postembedpg/schema.sql` after existing canonical
schemas, with all writers on this owned lock protocol. No startup DDL or data
migration runs. Rolling back the application can leave the additive reference
rows in place; old hosts reject meaningful new composer fields and will omit the
new projections. Do not remove association tables until reverting application
wiring. This is a continuation of the owned runtime, not full upstream parity or
an approval to deploy.
