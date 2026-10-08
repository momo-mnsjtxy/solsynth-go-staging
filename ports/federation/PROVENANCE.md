# ActivityPub continuation provenance

This independently authored Go adapter uses the retained, licensed DysonNetwork
Sphere controller inventory at commit
`4ee2d0cebb9da8284a92483fee6dce795bf6947c`, the existing owned Go authority,
publisher and text-post schemas, and official protocol documents. It does not
retrieve or depend on excluded Shared definitions, SoSYS, Socommon, Stargate,
unlicensed specifications, generated API definitions, or remote actors.

The source files already retained under
`inventory/operation-sources/DysonNetwork.Sphere/ActivityPub/` establish these
bounded observations:

- `ActivityPubController.cs`: publisher-based actor route, signed inbox boundary,
  public local-object retrieval, outbox and collection routes.
  SHA-256 `db52059524c21b87d272e8f349a01f5103733b9735f97e08957c77c678814888`.
- `WebFingerController.cs`: account resource parsing and publisher actor lookup.
  SHA-256 `9388351edf1995ddf18725d490c8193db2de6488e2c0a6582b221459e3fb042a`.
- `ActivityPubFollowController.cs`: separate authenticated federation relationship
  operations. New owned management routes are documented as additions, not
  claimed as an exact reproduction of that controller's read API.
  SHA-256 `c4911f75f87d88658d7cad1f46bb7fca3712d604d1702251dfaf6f0caf7fd525`.
- `Services/DeliveryRetryJob.cs`: persisted retry attempts and terminal failures.
  SHA-256 `25efae4319b93ab2f56ea80ec1480a1c00a973570b4e8b4add15cd2117c748c5`.

Official references consulted on 2026-10-08:

- [W3C ActivityPub Recommendation](https://www.w3.org/TR/activitypub/): actor,
  public addressing, server-to-server delivery and follow lifecycle semantics.
- [IETF HTTP Signatures draft 12](https://datatracker.ietf.org/doc/html/draft-cavage-http-signatures-12):
  legacy signing-string construction. This implementation deliberately supports
  a bounded RSA-SHA256 interoperability profile; this expired Internet-Draft is
  not represented as RFC 9421 conformance.
- WebFinger's `acct:` resource and JRD response shape are also independently
  evidenced by the retained licensed controller. A direct RFC 7033 fetch was
  unavailable; no unavailable text was treated as evidence.

No live federation target or production service was contacted during this work.
All new Go/SQL files carry AGPL-3.0-only identifiers, consistent with the project.
